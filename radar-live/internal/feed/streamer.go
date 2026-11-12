package feed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	streamPath         = "/v1/stream"
	keyHeader          = "X-Ingest-Key"
	maxSourceChars     = 48
	defaultSourceName  = "feed"
	maxBatchBytes      = 32 << 10
	maxErrorBodyBytes  = 512
	shutdownGrace      = 5 * time.Second
	expectContinueWait = 2 * time.Second
	defaultBackoffMin  = 500 * time.Millisecond
	defaultBackoffMax  = 30 * time.Second
	rateBatchDivisor   = 4 // a batch is at most a quarter second of the byte budget
)

var unsafeSource = regexp.MustCompile(`[^\w.:-]+`)

// SanitizeSource turns a host name or free text into a source id accepted by radar-ingest
// (letters, digits, "_", ".", ":", "-", at most 48 characters).
func SanitizeSource(s string) string {
	s = strings.Trim(unsafeSource.ReplaceAllString(strings.TrimSpace(s), "-"), "-")
	if len(s) > maxSourceChars {
		s = strings.Trim(s[:maxSourceChars], "-")
	}
	if s == "" {
		return defaultSourceName
	}
	return s
}

// EventKind says what an Event reports.
type EventKind int

// The kinds of Event a Streamer reports to StreamerConfig.Observe.
const (
	EventConnecting   EventKind = iota // a connection to ingest is being opened
	EventSent                          // Lines and Bytes were written to the open connection
	EventEnded                         // the connection ended: Status and Body hold ingest's reply, Err the failure (if any)
	EventBackoff                       // the streamer waits Wait before it connects again
	EventAuthRejected                  // ingest answered 401/403 (Status): the streamer stops and Run returns ErrAuthRejected
)

// Event is one progress report. Observe is called from the streamer's goroutines and must not block.
type Event struct {
	Kind         EventKind
	Lines, Bytes int
	Status       int // HTTP status of the reply, 0 when none arrived
	Body         string
	Err          error
	Wait         time.Duration
}

// StreamerConfig configures a Streamer.
type StreamerConfig struct {
	URL            string // base URL of radar-ingest, e.g. http://127.0.0.1:8788
	Key            string // sent as X-Ingest-Key (empty = no header)
	Source         string // ?source=
	Ruleset        string // ?rs= (empty = let ingest detect it)
	MaxBytesPerSec int    // <= 0 means unlimited
	BackoffMin     time.Duration
	BackoffMax     time.Duration
	Rand           func() float64 // jitter source returning [0, 1); test hook, default math/rand
	Client         *http.Client
	Logf           func(format string, args ...any)
	Observe        func(Event)                                // optional progress callback (see Event)
	Now            func() time.Time                           // test hook
	Sleep          func(context.Context, time.Duration) error // test hook
}

// Streamer posts queued lines to radar-ingest's /v1/stream as one long chunked request and
// reconnects with a backoff that depends on the failure (see backoff). It stops for good when the key is refused. Delivery is at most once: lines that were already written
// to a connection that then broke are not resent, because ingest adds counters and a replay would
// count twice. Lines that were never written are kept and retried.
type Streamer struct {
	cfg     StreamerConfig
	limiter *Limiter
	sleep   func(context.Context, time.Duration) error
	now     func() time.Time
	rand    func() float64
}

// NewStreamer applies defaults to cfg.
func NewStreamer(cfg StreamerConfig) *Streamer {
	if cfg.BackoffMin <= 0 {
		cfg.BackoffMin = defaultBackoffMin
	}
	if cfg.BackoffMax < cfg.BackoffMin {
		cfg.BackoffMax = max(defaultBackoffMax, cfg.BackoffMin)
	}
	if cfg.Client == nil {
		// No overall timeout: the request is meant to stay open for the whole play session.
		cfg.Client = &http.Client{Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, ExpectContinueTimeout: expectContinueWait,
			MaxIdleConns: 1, IdleConnTimeout: 30 * time.Second,
		}}
	}
	cfg.Key = CleanKey(cfg.Key)
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = SleepContext
	}
	now, random := cfg.Now, cfg.Rand
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = rand.Float64
	}
	return &Streamer{cfg: cfg, sleep: sleep, now: now, rand: random, limiter: NewLimiter(cfg.MaxBytesPerSec, cfg.Now, cfg.Sleep)}
}

// streamURL builds the full /v1/stream URL from the ingest base URL.
func streamURL(base, source, rs string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("ingest url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("ingest url %q must be http:// or https:// with a host", base)
	}
	u.Path = strings.TrimRight(u.Path, "/") + streamPath
	q := url.Values{"source": {source}}
	if rs != "" {
		q.Set("rs", rs)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *Streamer) observe(e Event) {
	if s.cfg.Observe != nil {
		s.cfg.Observe(e)
	}
}

// Run sends lines from q until ctx ends (it then returns ctx.Err()). It fails up front when the
// configured URL is unusable, and with ErrAuthRejected when ingest refuses the key (401/403).
func (s *Streamer) Run(ctx context.Context, q *Queue) error {
	target, err := streamURL(s.cfg.URL, s.cfg.Source, s.cfg.Ruleset)
	if err != nil {
		return err
	}
	wait := newBackoff(s.cfg.BackoffMin, s.cfg.BackoffMax, s.rand)
	for {
		// A connection is opened only when there is something to send, so a quiet game does
		// not make ingest's idle timer close and reopen the stream.
		first, err := q.Pop(ctx)
		if err != nil {
			return err
		}
		sent, out := s.session(ctx, target, q, first)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if out.fatal() {
			s.observe(Event{Kind: EventAuthRejected, Status: out.status})
			return &AuthRejectedError{Status: out.status}
		}
		if sent > 0 {
			wait.reset()
		}
		d := wait.next(out.status, out.retryAfter, s.now())
		if err := out.failure(); err != nil {
			s.cfg.Logf("stream to ingest ended: %v (retrying in %s, %d lines queued)", err, d.Round(time.Millisecond), q.Len())
		}
		s.observe(Event{Kind: EventBackoff, Wait: d})
		if serr := s.sleep(ctx, d); serr != nil {
			return serr
		}
	}
}

// outcome is what the HTTP exchange of one session produced.
type outcome struct {
	status     int
	body       string
	retryAfter string // the Retry-After header of the reply
	err        error
}

// fatal reports a reply that retrying cannot fix: the key was refused.
func (o outcome) fatal() bool {
	return o.err == nil && (o.status == http.StatusUnauthorized || o.status == http.StatusForbidden)
}

// session holds one connection open, starting with the already popped line, and returns how many
// lines it wrote and what the exchange produced.
func (s *Streamer) session(ctx context.Context, target string, q *Queue, first string) (int, outcome) {
	s.observe(Event{Kind: EventConnecting})
	pr, pw := io.Pipe()
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, pr)
	if err != nil {
		return 0, outcome{err: err}
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Expect", "100-continue") // a rejection (bad key, busy source) arrives before any line is sent
	if s.cfg.Key != "" {
		req.Header.Set(keyHeader, s.cfg.Key)
	}
	popCtx, stopPop := context.WithCancel(ctx)
	defer stopPop()
	finished := make(chan outcome, 1)
	go func() {
		out := s.exchange(req)
		pr.CloseWithError(errors.New("ingest closed the stream"))
		stopPop() // wake the writer: the connection is gone
		finished <- out
	}()

	sent := s.pump(ctx, popCtx, q, pw, first)
	_ = pw.Close()
	out := s.await(finished, cancelReq)
	s.observe(Event{Kind: EventEnded, Status: out.status, Body: out.body, Err: out.failure()})
	return sent, out
}

// failure turns a finished exchange into an error (nil for a 200 reply).
func (o outcome) failure() error {
	switch {
	case o.err != nil:
		return o.err
	case o.status != http.StatusOK:
		return fmt.Errorf("ingest answered %d %s", o.status, strings.TrimSpace(o.body))
	}
	return nil
}

// exchange performs the request and reads a bounded part of the reply.
func (s *Streamer) exchange(req *http.Request) outcome {
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return outcome{err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	return outcome{status: resp.StatusCode, body: string(body), retryAfter: resp.Header.Get("Retry-After")}
}

// await waits for the exchange to finish, cutting it off after the shutdown grace.
func (s *Streamer) await(finished <-chan outcome, cancel context.CancelFunc) outcome {
	t := time.NewTimer(shutdownGrace)
	defer t.Stop()
	select {
	case out := <-finished:
		return out
	case <-t.C:
		cancel()
		return <-finished
	}
}

// pump writes batches of lines to w until the connection or ctx ends and returns the lines written.
// A batch that could not be written is put back in the queue in its original order.
func (s *Streamer) pump(ctx, popCtx context.Context, q *Queue, w io.Writer, first string) int {
	sent := 0
	line := first
	for {
		batch := s.collect(q, line)
		if err := s.limiter.Wait(ctx, batch.buf.Len()); err != nil {
			batch.requeue(q)
			return sent
		}
		if _, err := w.Write(batch.buf.Bytes()); err != nil {
			batch.requeue(q)
			return sent
		}
		sent += len(batch.lines)
		s.observe(Event{Kind: EventSent, Lines: len(batch.lines), Bytes: batch.buf.Len()})
		next, err := q.Pop(popCtx)
		if err != nil {
			return sent
		}
		line = next
	}
}

// batch is a group of lines sent in one write.
type batch struct {
	lines []string
	buf   bytes.Buffer
}

func (b *batch) add(line string) {
	b.lines = append(b.lines, line)
	b.buf.WriteString(line)
	b.buf.WriteByte('\n')
}

func (b *batch) requeue(q *Queue) {
	for i := len(b.lines) - 1; i >= 0; i-- {
		q.PushFront(b.lines[i])
	}
}

// collect starts a batch with first and adds whatever else is queued, up to the batch budget.
func (s *Streamer) collect(q *Queue, first string) *batch {
	limit := maxBatchBytes
	if r := s.cfg.MaxBytesPerSec; r > 0 {
		limit = min(limit, max(r/rateBatchDivisor, 1))
	}
	b := &batch{}
	b.add(first)
	for b.buf.Len() < limit {
		line, ok := q.TryPop()
		if !ok {
			break
		}
		b.add(line)
	}
	return b
}
