// Package relay is the module that forwards extracted pings to radar-db. It registers a core.Sink
// that never fails and never blocks on the network: pings go into a bounded buffer (oldest dropped
// first, with a counter) and one worker posts them to <db-url>/internal/v1/ping, retrying with
// exponential backoff and jitter. A ping keeps its batch id across retries, so the database applies
// it at most once. The relay only ever dials the configured URL and never follows redirects.
package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"radarlive/internal/domain"
)

const (
	pingPath         = "/internal/v1/ping"
	keyHeader        = "X-Ingest-Key"
	maxResponseBytes = 64 << 10
	jitterDivisor    = 2
	idRandomBytes    = 4
	sinkName         = "relay"
	// seenWindow is how many recent batch ids the relay remembers to ignore replays.
	seenWindow = 4096
	// minBackoff is the smallest accepted retry delay: a tiny value would turn a dead database into a busy loop.
	minBackoff = 10 * time.Millisecond
)

// Config holds the relay's limits; every field is required.
type Config struct {
	URL         string        // base URL of radar-db
	Key         string        // sent as X-Ingest-Key (may be empty when radar-db is open)
	BufferPings int           // max pings held
	BufferBytes int           // max encoded bytes held
	BatchPings  int           // max pings posted per send pass
	Timeout     time.Duration // per request
	BackoffMin  time.Duration
	BackoffMax  time.Duration
}

// Options replaces the clock-dependent parts in tests.
type Options struct {
	Client *http.Client
	Sleep  func(ctx context.Context, d time.Duration) error
	Jitter func(limit time.Duration) time.Duration
}

// Stats are the relay's counters. Dropped counts pings pushed out of a full buffer (drop-oldest),
// Duplicates pings ignored because their batch id was seen lately, Sanitized text repairs made.
type Stats struct {
	Pending    int   `json:"pending"`
	Bytes      int   `json:"bytes"`
	Sent       int64 `json:"sent"`
	Dropped    int64 `json:"dropped"`
	Rejected   int64 `json:"rejected"`
	Retries    int64 `json:"retries"`
	Duplicates int64 `json:"duplicates"`
	Sanitized  int64 `json:"sanitized"`
}

type entry struct {
	seq  uint64
	id   string
	body []byte
}

// Relay buffers pings and posts them to radar-db.
type Relay struct {
	cfg    Config
	url    string
	log    *slog.Logger
	client *http.Client
	sleep  func(ctx context.Context, d time.Duration) error
	jitter func(limit time.Duration) time.Duration

	mu    sync.Mutex
	queue []entry
	seen  map[string]struct{} // recent batch ids, oldest first in seenOrder
	order []string
	bytes int
	stats Stats
	seq   uint64
	run   string
	wake  chan struct{}

	attempt int // consecutive failed passes; only the sending goroutine touches it
}

// New validates cfg and creates a Relay.
func New(cfg Config, log *slog.Logger, opts Options) (*Relay, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	r := &Relay{cfg: cfg, url: strings.TrimRight(cfg.URL, "/") + pingPath, log: log, client: opts.Client,
		sleep: opts.Sleep, jitter: opts.Jitter, wake: make(chan struct{}, 1), run: runID(), seen: map[string]struct{}{}}
	if r.client == nil {
		r.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if r.sleep == nil {
		r.sleep = sleepCtx
	}
	if r.jitter == nil {
		r.jitter = randomJitter
	}
	return r, nil
}

func (c Config) validate() error {
	if err := validateURL(c.URL); err != nil {
		return err
	}
	if c.BufferPings < 1 || c.BufferBytes < 1 || c.BatchPings < 1 {
		return errors.New("relay: buffer and batch sizes must be positive")
	}
	if c.Timeout <= 0 || c.BackoffMin < minBackoff || c.BackoffMax < c.BackoffMin {
		return fmt.Errorf("relay: timeout must be positive and backoff at least %v with max >= min", minBackoff)
	}
	return nil
}

// validateURL accepts only a plain http(s) base URL: a host, optionally a path, and nothing that
// could smuggle credentials into logs or redirect the request (userinfo, query, fragment, spaces).
// Error texts never repeat the value, which may hold a password.
func validateURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return errors.New("relay: db-url is not a valid URL")
	case u.Scheme != "http" && u.Scheme != "https":
		return errors.New("relay: db-url must start with http:// or https://")
	case u.Hostname() == "":
		return errors.New("relay: db-url needs a host")
	case u.User != nil:
		return errors.New("relay: db-url must not contain credentials (use the db-key setting)")
	case u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "?#"):
		return errors.New("relay: db-url must not contain a query or fragment")
	case strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return errors.New("relay: db-url must not contain spaces or control characters")
	}
	return nil
}

func runID() string {
	var b [idRandomBytes]byte
	_, _ = rand.Read(b[:])
	return strconv.FormatUint(uint64(binary.BigEndian.Uint32(b[:])), 36)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func randomJitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	return time.Duration(mrand.Int64N(int64(limit)))
}

// Name implements core.Sink.
func (*Relay) Name() string { return sinkName }

// Accept queues ping. It fails only for an invalid ping; a full buffer drops the oldest entries.
func (r *Relay) Accept(_ context.Context, ping domain.Ping) error {
	r.mu.Lock()
	r.seq++
	seq := r.seq
	r.mu.Unlock()
	if ping.BatchID == "" {
		ping.BatchID = fmt.Sprintf("rl-%s-%d", r.run, seq)
	}
	repaired := ping.Sanitize() // names come from logs: repair them, never lose a whole ping over a bad name
	if err := ping.Validate(); err != nil {
		return fmt.Errorf("relay: %w", err)
	}
	body, err := json.Marshal(ping)
	if err != nil {
		return fmt.Errorf("relay: encode ping: %w", err)
	}
	r.mu.Lock()
	r.stats.Sanitized += int64(repaired)
	if r.replayLocked(ping.BatchID) {
		r.stats.Duplicates++
		r.mu.Unlock()
		return nil
	}
	r.queue = append(r.queue, entry{seq: seq, id: ping.BatchID, body: body})
	r.bytes += len(body)
	r.trimLocked()
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// replayLocked remembers id and reports whether it was already in the window of recent batch ids.
func (r *Relay) replayLocked(id string) bool {
	if _, dup := r.seen[id]; dup {
		return true
	}
	r.seen[id] = struct{}{}
	r.order = append(r.order, id)
	if len(r.order) > seenWindow {
		delete(r.seen, r.order[0])
		r.order[0] = ""
		r.order = r.order[1:]
		if cap(r.order) > 4*seenWindow { // let the slice shed its dead head now and then
			r.order = append([]string(nil), r.order...)
		}
	}
	return false
}

// trimLocked drops the oldest entries until both caps hold (the newest entry is always kept).
func (r *Relay) trimLocked() {
	for len(r.queue) > 1 && (len(r.queue) > r.cfg.BufferPings || r.bytes > r.cfg.BufferBytes) {
		r.bytes -= len(r.queue[0].body)
		r.queue[0] = entry{}
		r.queue = r.queue[1:]
		r.stats.Dropped++
	}
}

// Stats returns a snapshot of the counters.
func (r *Relay) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.Pending, s.Bytes = len(r.queue), r.bytes
	return s
}

// head returns a copy of the oldest entry.
func (r *Relay) head() (entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queue) == 0 {
		return entry{}, false
	}
	return r.queue[0], true
}

// done removes the entry that was just handled, unless drop-oldest already removed it.
func (r *Relay) done(e entry, sent bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queue) > 0 && r.queue[0].seq == e.seq {
		r.bytes -= len(e.body)
		r.queue[0] = entry{}
		r.queue = r.queue[1:]
	}
	if sent {
		r.stats.Sent++
	} else {
		r.stats.Rejected++
	}
}

// sendBatch posts up to BatchPings pings in order. It stops at the first transient failure and
// returns it; the failed ping stays at the head of the queue with its batch id.
func (r *Relay) sendBatch(ctx context.Context) error {
	for i := 0; i < r.cfg.BatchPings; i++ {
		e, ok := r.head()
		if !ok {
			return nil
		}
		retry, err := r.post(ctx, e)
		if retry {
			r.mu.Lock()
			r.stats.Retries++
			r.mu.Unlock()
			return err
		}
		r.done(e, err == nil)
		if err != nil {
			r.log.Warn("relay: ping rejected by radar-db", "batch", e.id, "err", err.Error())
		}
		r.attempt = 0
	}
	return nil
}

// post sends one entry. retry reports a transient failure; a non-nil error without retry is a
// permanent rejection.
func (r *Relay) post(ctx context.Context, e entry) (retry bool, err error) {
	rctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, r.url, bytes.NewReader(e.body))
	if err != nil {
		return false, fmt.Errorf("relay: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.cfg.Key != "" {
		req.Header.Set(keyHeader, r.cfg.Key)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return true, fmt.Errorf("relay: post: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	switch {
	case resp.StatusCode/100 == 2:
		return false, nil
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return true, fmt.Errorf("relay: radar-db answered %d", resp.StatusCode)
	}
	return false, fmt.Errorf("relay: radar-db answered %d", resp.StatusCode)
}

// backoff counts a failed pass and sleeps min*2^(n-1), capped at BackoffMax, plus jitter.
func (r *Relay) backoff(ctx context.Context) error {
	r.attempt++
	d := time.Duration(float64(r.cfg.BackoffMin) * math.Pow(2, float64(r.attempt-1)))
	if d > r.cfg.BackoffMax || d <= 0 {
		d = r.cfg.BackoffMax
	}
	return r.sleep(ctx, d+r.jitter(d/jitterDivisor))
}

// Run sends queued pings until ctx ends.
func (r *Relay) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if r.Stats().Pending == 0 {
			select {
			case <-ctx.Done():
				return
			case <-r.wake:
			}
			continue
		}
		if err := r.sendBatch(ctx); err != nil {
			r.log.Warn("relay: send failed, will retry", "err", err.Error())
			if r.backoff(ctx) != nil {
				return
			}
		}
	}
}

// Drain sends what is queued until the buffer is empty or ctx ends.
func (r *Relay) Drain(ctx context.Context) error {
	for r.Stats().Pending > 0 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("relay: drain: %d pings unsent: %w", r.Stats().Pending, err)
		}
		if err := r.sendBatch(ctx); err != nil {
			if r.backoff(ctx) != nil {
				return fmt.Errorf("relay: drain: %d pings unsent: %w", r.Stats().Pending, err)
			}
		}
	}
	return nil
}
