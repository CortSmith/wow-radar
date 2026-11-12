package client

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/feed"
)

// fakeClock is a settable clock shared by the tests of the package.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestProgress() (*progress, *fakeClock) {
	clock := newFakeClock()
	p := newProgress(clock.Now)
	p.begin()
	return p, clock
}

func TestProgressStartsWaiting(t *testing.T) {
	p, _ := newTestProgress()
	st := p.snapshot("", 0, 0)
	if st.State != StateWaiting || !st.Running {
		t.Fatalf("status = %+v", st)
	}
}

func TestProgressIdleBeforeBegin(t *testing.T) {
	p := newProgress(newFakeClock().Now)
	if st := p.snapshot("", 0, 0); st.State != StateIdle || st.Running {
		t.Fatalf("status = %+v", st)
	}
}

func TestProgressUploadThenWaiting(t *testing.T) {
	p, clock := newTestProgress()
	p.handle(feed.Event{Kind: feed.EventConnecting})
	if st := p.snapshot("", 0, 0); st.State != StateConnecting {
		t.Fatalf("state = %s", st.State)
	}
	p.handle(feed.Event{Kind: feed.EventSent, Lines: 3, Bytes: 120})
	st := p.snapshot("log.txt", 7, 2)
	if st.State != StateUploading || st.LinesSent != 3 || st.BytesSent != 120 || st.File != "log.txt" || st.Queued != 7 || st.Dropped != 2 {
		t.Fatalf("status = %+v", st)
	}
	if st.LastUploadMs != clock.Now().UnixMilli() {
		t.Fatalf("lastUploadMs = %d", st.LastUploadMs)
	}
	clock.Advance(activeWindow + time.Second)
	if st := p.snapshot("", 0, 0); st.State != StateWaiting {
		t.Fatalf("state after quiet period = %s", st.State)
	}
}

func TestProgressLinesPerSecondOverTheRateWindow(t *testing.T) {
	p, clock := newTestProgress()
	p.handle(feed.Event{Kind: feed.EventSent, Lines: 50, Bytes: 1})
	clock.Advance(5 * time.Second)
	if got := p.snapshot("", 0, 0).LinesPerSec; got != 10 {
		t.Fatalf("rate after 5 s = %v, want 10", got)
	}
	clock.Advance(2 * rateWindow)
	if got := p.snapshot("", 0, 0).LinesPerSec; got != 0 {
		t.Fatalf("rate after the window = %v, want 0", got)
	}
}

func TestProgressErrorKeepsReasonAndCountsDown(t *testing.T) {
	p, clock := newTestProgress()
	p.handle(feed.Event{Kind: feed.EventEnded, Status: http.StatusServiceUnavailable, Err: errors.New("ingest answered 503")})
	p.handle(feed.Event{Kind: feed.EventBackoff, Wait: 4 * time.Second})
	st := p.snapshot("", 0, 0)
	if st.State != StateError || !strings.Contains(st.Error, "HTTP 503") || st.RetryInSec != 4 {
		t.Fatalf("status = %+v", st)
	}
	clock.Advance(1500 * time.Millisecond)
	if got := p.snapshot("", 0, 0).RetryInSec; got != 3 {
		t.Fatalf("retry in %d, want 3 (rounded up)", got)
	}
	p.handle(feed.Event{Kind: feed.EventConnecting})
	st = p.snapshot("", 0, 0)
	if st.State != StateConnecting || st.Error == "" || st.RetryInSec != 0 {
		t.Fatalf("while reconnecting: %+v", st)
	}
	p.handle(feed.Event{Kind: feed.EventSent, Lines: 1, Bytes: 1})
	if st := p.snapshot("", 0, 0); st.State != StateUploading || st.Error != "" {
		t.Fatalf("after success: %+v", st)
	}
}

func TestProgressShowsServerSummary(t *testing.T) {
	p, _ := newTestProgress()
	body := `{"source":"pc","lines":120,"events":100,"contacts":4,"rejected":2,"durationMs":5000,"ended":"eof"}`
	p.handle(feed.Event{Kind: feed.EventEnded, Status: http.StatusOK, Body: body})
	st := p.snapshot("", 0, 0)
	for _, want := range []string{"120 lines", "100 events", "4 players"} {
		if !strings.Contains(st.LastReply, want) {
			t.Fatalf("reply %q lacks %q", st.LastReply, want)
		}
	}
	if st.State == StateError {
		t.Fatalf("a clean end is not an error: %+v", st)
	}
}

func TestProgressUnparsableReplyIsShownTrimmed(t *testing.T) {
	p, _ := newTestProgress()
	p.handle(feed.Event{Kind: feed.EventEnded, Status: http.StatusOK, Body: "  plain text  "})
	if got := p.snapshot("", 0, 0).LastReply; got != "plain text" {
		t.Fatalf("reply = %q", got)
	}
}

func TestProgressEndKeepsNumbersButGoesIdle(t *testing.T) {
	p, _ := newTestProgress()
	p.handle(feed.Event{Kind: feed.EventSent, Lines: 5, Bytes: 50})
	p.end()
	st := p.snapshot("", 0, 0)
	if st.State != StateIdle || st.Running || st.LinesSent != 5 {
		t.Fatalf("status = %+v", st)
	}
}

func TestProgressWarningIsLoggedOncePerChange(t *testing.T) {
	p, _ := newTestProgress()
	p.setWarning("folder missing")
	p.setWarning("folder missing")
	if st := p.snapshot("", 0, 0); st.Note != "folder missing" || len(st.Events) != 1 {
		t.Fatalf("status = %+v", st)
	}
	p.setWarning("")
	if st := p.snapshot("", 0, 0); st.Note != "" {
		t.Fatalf("note not cleared: %+v", st)
	}
}

func TestProgressFailIsAnError(t *testing.T) {
	p, _ := newTestProgress()
	p.fail("bad address")
	if st := p.snapshot("", 0, 0); st.State != StateError || st.Error != "bad address" {
		t.Fatalf("status = %+v", st)
	}
}

func TestDescribeFailure(t *testing.T) {
	tests := []struct {
		name string
		ev   feed.Event
		want string
	}{
		{"unauthorized", feed.Event{Status: 401}, "The server rejected the ingest key (401)."},
		{"forbidden", feed.Event{Status: 403}, "refused"},
		{"busy source", feed.Event{Status: 409}, "same source name"},
		{"rate limited", feed.Event{Status: 429}, "limiting this address"},
		{"not found", feed.Event{Status: 404}, "not the web page"},
		{"server error", feed.Event{Status: 503}, "HTTP 503"},
		{"network", feed.Event{Err: errors.New("dial tcp 10.0.0.1:1: refused")}, "Cannot reach the server"},
		{"no detail", feed.Event{}, "unknown error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeFailure(tc.ev); !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestProgressEventLogIsBounded(t *testing.T) {
	p, _ := newTestProgress()
	for i := 0; i < maxLogEvents+20; i++ {
		p.note("event %d", i)
	}
	events := p.snapshot("", 0, 0).Events
	if len(events) != maxLogEvents || !strings.HasSuffix(events[len(events)-1].Text, "event 119") {
		t.Fatalf("%d events, last %q", len(events), events[len(events)-1].Text)
	}
}

const wantAuthMessage = "The server rejected the ingest key (401). The upload stopped; fix the key and click Start again."

func TestProgressAuthRejectedIsATerminalError(t *testing.T) {
	p, _ := newTestProgress()
	p.handle(feed.Event{Kind: feed.EventConnecting})
	p.handle(feed.Event{Kind: feed.EventEnded, Status: 401, Err: errors.New("ingest answered 401")})
	p.handle(feed.Event{Kind: feed.EventAuthRejected, Status: 401})
	st := p.snapshot("", 0, 0)
	if st.State != StateError || st.Running || st.Error != wantAuthMessage || st.RetryInSec != 0 {
		t.Fatalf("status = %+v", st)
	}
	if last := st.Events[len(st.Events)-1].Text; last != wantAuthMessage {
		t.Fatalf("last event %q", last)
	}
	p.begin() // Start again
	if st := p.snapshot("", 0, 0); st.State != StateWaiting || !st.Running || st.Error != "" {
		t.Fatalf("after restart: %+v", st)
	}
}

func TestProgressAuthRejectedForbiddenMessage(t *testing.T) {
	p, _ := newTestProgress()
	p.handle(feed.Event{Kind: feed.EventAuthRejected, Status: 403})
	if got := p.snapshot("", 0, 0).Error; !strings.Contains(got, "(403)") || !strings.Contains(got, "click Start again") {
		t.Fatalf("error = %q", got)
	}
}

func TestProgressExplainsTheWaitAfterAThrottle(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{429, "The server is limiting this address (429); waiting 45 s before trying again."},
		{409, "Another uploader is already sending with the same source name (409); waiting 45 s before trying again."},
	}
	for _, tc := range tests {
		p, _ := newTestProgress()
		p.handle(feed.Event{Kind: feed.EventEnded, Status: tc.status, Err: errors.New("x")})
		p.handle(feed.Event{Kind: feed.EventBackoff, Wait: 45 * time.Second})
		st := p.snapshot("", 0, 0)
		if st.State != StateError || !strings.HasPrefix(st.Error, tc.want) || st.RetryInSec != 45 {
			t.Errorf("%d: status = %+v", tc.status, st)
		}
	}
}
