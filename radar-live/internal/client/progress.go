package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"radarlive/internal/feed"
)

// States a Session reports.
const (
	StateIdle       = "idle"
	StateConnecting = "connecting"
	StateUploading  = "uploading"
	StateWaiting    = "waiting" // running, nothing new to send
	StateError      = "error"
)

const (
	activeWindow = 3 * time.Second  // an upload this recent still counts as "uploading"
	rateWindow   = 10 * time.Second // lines/s is averaged over this long
	maxLogEvents = 100
)

// LogEvent is one line of the short event log shown in the UI.
type LogEvent struct {
	TimeMs int64  `json:"timeMs"`
	Text   string `json:"text"`
}

// Status is a snapshot of a Session for the UI. It never contains the ingest key.
type Status struct {
	State        string     `json:"state"`
	Running      bool       `json:"running"`
	Error        string     `json:"error"` // why the last attempt failed; cleared by the next successful upload
	Note         string     `json:"note"`  // a non-fatal remark, e.g. the log folder is missing
	File         string     `json:"file"`  // the log being followed
	LinesSent    int64      `json:"linesSent"`
	BytesSent    int64      `json:"bytesSent"`
	LinesPerSec  float64    `json:"linesPerSec"`
	LastUploadMs int64      `json:"lastUploadMs"` // epoch ms of the last upload, 0 = none yet
	LastReply    string     `json:"lastReply"`    // the server's last summary
	RetryInSec   int        `json:"retryInSec"`   // seconds until the next connection attempt
	Queued       int        `json:"queued"`       // lines read but not yet sent
	Dropped      int64      `json:"dropped"`      // lines discarded because the queue was full
	Events       []LogEvent `json:"events"`
}

type connState int

const (
	connNone connState = iota
	connConnecting
	connOpen
	connFailed
)

type rateSample struct {
	at    time.Time
	lines int
}

// progress turns the streamer's events into a Status. It is safe for concurrent use.
type progress struct {
	now func() time.Time

	mu         sync.Mutex
	running    bool
	conn       connState
	lastError  string
	lastStatus int  // HTTP status of the last failed connection (0 = none arrived)
	fatal      bool // the upload stopped for good (the key was refused); lastError says why
	warning    string
	lastReply  string
	lines      int64
	bytes      int64
	lastSent   time.Time
	retryAt    time.Time
	startedAt  time.Time
	samples    []rateSample
	events     []LogEvent
}

func newProgress(now func() time.Time) *progress { return &progress{now: now} }

// begin resets the counters for a new run.
func (p *progress) begin() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running, p.conn, p.lastError, p.warning, p.lastReply = true, connNone, "", "", ""
	p.lastStatus, p.fatal = 0, false
	p.lines, p.bytes = 0, 0
	p.lastSent, p.retryAt, p.startedAt = time.Time{}, time.Time{}, p.now()
	p.samples, p.events = nil, nil
}

// end marks the run as stopped but keeps the numbers and the log for display.
func (p *progress) end() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = false
	p.conn = connNone
	p.retryAt = time.Time{}
}

// note appends a line to the event log.
func (p *progress) note(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.noteLocked(format, args...)
}

func (p *progress) noteLocked(format string, args ...any) {
	p.events = append(p.events, LogEvent{TimeMs: p.now().UnixMilli(), Text: fmt.Sprintf(format, args...)})
	if over := len(p.events) - maxLogEvents; over > 0 {
		p.events = append([]LogEvent(nil), p.events[over:]...)
	}
}

// setWarning records (or clears, with "") a non-fatal remark such as "log folder not found".
func (p *progress) setWarning(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if text != "" && text != p.warning {
		p.noteLocked("%s", text)
	}
	p.warning = text
}

// fail records an error that stopped the run before any upload.
func (p *progress) fail(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.conn, p.lastError = connFailed, reason
	p.noteLocked("%s", reason)
}

// handle applies one streamer event.
func (p *progress) handle(e feed.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch e.Kind {
	case feed.EventConnecting:
		p.conn, p.retryAt = connConnecting, time.Time{}
		p.noteLocked("Connecting to the server")
	case feed.EventSent:
		p.onSent(e)
	case feed.EventEnded:
		p.onEnded(e)
	case feed.EventBackoff:
		p.onBackoff(e)
	case feed.EventAuthRejected:
		p.onAuthRejected(e)
	}
}

func (p *progress) onSent(e feed.Event) {
	if p.conn != connOpen {
		p.noteLocked("Uploading")
	}
	now := p.now()
	p.conn, p.lastError, p.retryAt = connOpen, "", time.Time{}
	p.lines += int64(e.Lines)
	p.bytes += int64(e.Bytes)
	p.lastSent = now
	p.samples = append(p.samples, rateSample{at: now, lines: e.Lines})
}

func (p *progress) onEnded(e feed.Event) {
	if e.Err != nil {
		p.conn, p.lastStatus = connFailed, e.Status
		if waitsBeforeRetry(e.Status) {
			return // onBackoff explains it once the wait is known
		}
		p.lastError = describeFailure(e)
		p.noteLocked("%s", p.lastError)
		return
	}
	p.conn = connNone
	p.lastReply = describeReply(e.Body)
	p.noteLocked("Server summary: %s", p.lastReply)
}

// onBackoff records when the next attempt happens and, for a 429 or 409, says why it waits.
func (p *progress) onBackoff(e feed.Event) {
	p.retryAt = p.now().Add(e.Wait)
	if !waitsBeforeRetry(p.lastStatus) {
		return
	}
	p.lastError = describeWait(p.lastStatus, e.Wait)
	p.noteLocked("%s", p.lastError)
}

// onAuthRejected ends the run: the server refused the key, so the streamer will not retry.
func (p *progress) onAuthRejected(e feed.Event) {
	p.running, p.fatal, p.conn, p.retryAt = false, true, connFailed, time.Time{}
	p.lastStatus, p.lastError = e.Status, feed.AuthRejectedMessage(e.Status)
	p.noteLocked("%s", p.lastError)
}

// snapshot builds the Status; file, queued and dropped come from the Session.
func (p *progress) snapshot(file string, queued int, dropped int64) Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	st := Status{
		State: p.state(now), Running: p.running, Error: p.lastError, Note: p.warning, File: file,
		LinesSent: p.lines, BytesSent: p.bytes, LinesPerSec: p.rate(now), LastReply: p.lastReply,
		Queued: queued, Dropped: dropped, Events: append([]LogEvent(nil), p.events...),
	}
	if !p.lastSent.IsZero() {
		st.LastUploadMs = p.lastSent.UnixMilli()
	}
	if p.running && p.conn == connFailed && now.Before(p.retryAt) {
		st.RetryInSec = int(math.Ceil(p.retryAt.Sub(now).Seconds()))
	}
	return st
}

func (p *progress) state(now time.Time) string {
	switch {
	case p.fatal:
		return StateError
	case !p.running:
		return StateIdle
	case p.conn == connFailed:
		return StateError
	case p.conn == connConnecting:
		return StateConnecting
	case !p.lastSent.IsZero() && now.Sub(p.lastSent) < activeWindow:
		return StateUploading
	}
	return StateWaiting
}

// rate is lines per second over the last rateWindow (or since the start, if that is shorter).
func (p *progress) rate(now time.Time) float64 {
	cutoff := now.Add(-rateWindow)
	kept := p.samples[:0]
	total := 0
	for _, s := range p.samples {
		if s.at.After(cutoff) {
			kept = append(kept, s)
			total += s.lines
		}
	}
	p.samples = kept
	span := min(rateWindow, now.Sub(p.startedAt))
	if total == 0 || span < time.Second {
		return float64(total)
	}
	return float64(total) / span.Seconds()
}

// describeFailure explains a failed connection in words a player understands. It never includes
// request headers, so the key cannot leak.
func describeFailure(e feed.Event) string {
	switch e.Status {
	case 0:
		return "Cannot reach the server: " + rootCause(e.Err)
	case http.StatusUnauthorized, http.StatusForbidden:
		return feed.AuthRejectedMessage(e.Status)
	case http.StatusNotFound:
		return "The server answered 404. Is the address the ingest server, not the web page?"
	case http.StatusConflict, http.StatusTooManyRequests:
		return describeWait(e.Status, 0)
	}
	return fmt.Sprintf("The server answered HTTP %d.", e.Status)
}

// waitsBeforeRetry reports the statuses whose message names the wait (see describeWait).
func waitsBeforeRetry(status int) bool {
	return status == http.StatusConflict || status == http.StatusTooManyRequests
}

// describeWait explains a 409 or 429 and, when wait is positive, how long the client pauses.
func describeWait(status int, wait time.Duration) string {
	secs := int(math.Ceil(wait.Seconds()))
	if status == http.StatusConflict {
		msg := "Another uploader is already sending with the same source name (409)"
		if secs > 0 {
			msg += fmt.Sprintf("; waiting %d s before trying again", secs)
		}
		return msg + ". Close the other client or radar-feed, or use a different source name."
	}
	msg := "The server is limiting this address (429)"
	if secs > 0 {
		return msg + fmt.Sprintf("; waiting %d s before trying again.", secs)
	}
	return msg + "; it will be tried again later."
}

// rootCause drops the request URL that net/http puts in its errors.
func rootCause(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

// describeReply summarises ingest's JSON reply, or returns the text itself when it is something else.
func describeReply(body string) string {
	var sum struct {
		Lines, Events, Contacts, Rejected int
	}
	if err := json.Unmarshal([]byte(body), &sum); err != nil || sum.Lines+sum.Events+sum.Contacts == 0 {
		return strings.TrimSpace(body)
	}
	return fmt.Sprintf("%d lines, %d events, %d players seen, %d lines not understood", sum.Lines, sum.Events, sum.Contacts, sum.Rejected)
}
