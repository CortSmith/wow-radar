package feed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestQueueFIFOAndDropOldest(t *testing.T) {
	q := NewQueue(3)
	for i := 1; i <= 5; i++ {
		q.Push(fmt.Sprint("l", i))
	}
	if q.Len() != 3 || q.Dropped() != 2 {
		t.Fatalf("len %d dropped %d", q.Len(), q.Dropped())
	}
	for _, want := range []string{"l3", "l4", "l5"} {
		got, err := q.Pop(context.Background())
		if err != nil || got != want {
			t.Fatalf("got %q %v, want %q", got, err, want)
		}
	}
}

func TestQueuePopBlocksUntilPushOrCancel(t *testing.T) {
	q := NewQueue(4)
	got := make(chan string, 1)
	go func() {
		l, _ := q.Pop(context.Background())
		got <- l
	}()
	time.Sleep(20 * time.Millisecond)
	q.Push("x")
	select {
	case l := <-got:
		if l != "x" {
			t.Fatalf("got %q", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pop did not wake")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.Pop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestQueuePushFrontKeepsOrder(t *testing.T) {
	q := NewQueue(3)
	q.Push("b")
	q.PushFront("a")
	q.Push("c")
	q.PushFront("z") // full: the newest line is the one dropped to make room
	if q.Dropped() != 1 || q.Len() != 3 {
		t.Fatalf("len %d dropped %d", q.Len(), q.Dropped())
	}
	var got []string
	for q.Len() > 0 {
		l, _ := q.Pop(context.Background())
		got = append(got, l)
	}
	if strings.Join(got, ",") != "z,a,b" {
		t.Fatalf("got %v", got)
	}
}

func TestQueueMinimumSize(t *testing.T) {
	q := NewQueue(0)
	q.Push("a")
	q.Push("b")
	if q.Len() != 1 {
		t.Fatalf("len %d", q.Len())
	}
}

// fakeClock lets limiter tests run without real sleeping.
type fakeClock struct {
	now   time.Time
	slept time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.slept += d
	c.now = c.now.Add(d)
	return nil
}

func TestLimiterUnlimited(t *testing.T) {
	c := &fakeClock{now: time.Unix(0, 0)}
	l := NewLimiter(0, c.Now, c.Sleep)
	if err := l.Wait(context.Background(), 1<<30); err != nil || c.slept != 0 {
		t.Fatalf("slept %v err %v", c.slept, err)
	}
}

func TestLimiterThrottlesToRate(t *testing.T) {
	c := &fakeClock{now: time.Unix(0, 0)}
	l := NewLimiter(1000, c.Now, c.Sleep) // 1000 B/s, burst 1000
	for i := 0; i < 10; i++ {
		if err := l.Wait(context.Background(), 500); err != nil {
			t.Fatal(err)
		}
	}
	// 5000 bytes at 1000 B/s with a 1000 byte burst: 4 s of waiting.
	if c.slept < 3900*time.Millisecond || c.slept > 4100*time.Millisecond {
		t.Fatalf("slept %v", c.slept)
	}
}

func TestLimiterRefillsWhileIdle(t *testing.T) {
	c := &fakeClock{now: time.Unix(0, 0)}
	l := NewLimiter(1000, c.Now, c.Sleep)
	_ = l.Wait(context.Background(), 1000)
	c.now = c.now.Add(10 * time.Second) // idle: the bucket refills but only up to the burst
	_ = l.Wait(context.Background(), 1000)
	if c.slept != 0 {
		t.Fatalf("slept %v", c.slept)
	}
	_ = l.Wait(context.Background(), 1000)
	if c.slept < 900*time.Millisecond {
		t.Fatalf("burst not capped, slept %v", c.slept)
	}
}

func TestLimiterReportsCancel(t *testing.T) {
	boom := errors.New("cancelled")
	c := &fakeClock{now: time.Unix(0, 0)}
	l := NewLimiter(10, c.Now, func(context.Context, time.Duration) error { return boom })
	_ = l.Wait(context.Background(), 10)
	if err := l.Wait(context.Background(), 10); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestSleepContext(t *testing.T) {
	if err := SleepContext(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SleepContext(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// fakeIngest records what the streamer sent.
type fakeIngest struct {
	mu      sync.Mutex
	lines   []string
	conns   int
	keys    []string
	queries []string
	status  func(conn int) int // status to answer before reading (0 = accept)
	cut     func(conn int) int // close the connection after this many lines (0 = never)
}

func (f *fakeIngest) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.conns++
	conn := f.conns
	f.keys = append(f.keys, r.Header.Get("X-Ingest-Key"))
	f.queries = append(f.queries, r.URL.RawQuery)
	f.mu.Unlock()
	if f.status != nil {
		if code := f.status(conn); code != 0 {
			http.Error(w, `{"error":"x"}`, code)
			return
		}
	}
	data := make([]byte, 0, 1024)
	buf := make([]byte, 4096)
	limit := 0
	if f.cut != nil {
		limit = f.cut(conn)
	}
	seen := 0
	for {
		n, err := r.Body.Read(buf)
		data = append(data, buf[:n]...)
		for {
			i := strings.IndexByte(string(data), '\n')
			if i < 0 {
				break
			}
			f.mu.Lock()
			f.lines = append(f.lines, string(data[:i]))
			f.mu.Unlock()
			data = data[i+1:]
			seen++
		}
		if limit > 0 && seen >= limit {
			http.Error(w, `{"error":"cut"}`, http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			break
		}
	}
	_, _ = io.WriteString(w, `{"ended":"eof"}`)
}

func (f *fakeIngest) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...)
}

func (f *fakeIngest) waitLines(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if g := f.got(); len(g) >= n {
			return g
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("only %d of %d lines arrived: %v", len(f.got()), n, f.got())
	return nil
}

func newStreamer(url string) *Streamer {
	return NewStreamer(StreamerConfig{
		URL: url, Key: "k-secret", Source: "my-pc", Ruleset: "pvp",
		BackoffMin: time.Millisecond, BackoffMax: 5 * time.Millisecond,
		Logf: func(string, ...any) {},
	})
}

func runStreamer(t *testing.T, s *Streamer, q *Queue) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, q) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not stop")
		}
	}
}

func TestStreamerSendsLinesWithKeyAndQuery(t *testing.T) {
	ing := &fakeIngest{}
	srv := httptest.NewServer(http.HandlerFunc(ing.handler))
	defer srv.Close()
	q := NewQueue(100)
	stop := runStreamer(t, newStreamer(srv.URL), q)
	defer stop()
	for i := 0; i < 5; i++ {
		q.Push(fmt.Sprint("line ", i))
	}
	got := ing.waitLines(t, 5)
	if got[0] != "line 0" || got[4] != "line 4" {
		t.Fatalf("got %v", got)
	}
	ing.mu.Lock()
	defer ing.mu.Unlock()
	if ing.keys[0] != "k-secret" {
		t.Fatalf("key = %q", ing.keys[0])
	}
	if ing.queries[0] != "rs=pvp&source=my-pc" {
		t.Fatalf("query = %q", ing.queries[0])
	}
}

func TestStreamerStreamPathAppendedOnce(t *testing.T) {
	var path string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path = r.URL.Path
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer srv.Close()
	q := NewQueue(10)
	stop := runStreamer(t, newStreamer(srv.URL+"/"), q)
	defer stop()
	q.Push("a")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		p := path
		mu.Unlock()
		if p != "" {
			if p != "/v1/stream" {
				t.Fatalf("path = %q", p)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no request")
}

func TestStreamerRetriesAfterRejectionWithoutLosingLines(t *testing.T) {
	ing := &fakeIngest{status: func(conn int) int {
		if conn <= 2 {
			return http.StatusServiceUnavailable
		}
		return 0
	}}
	srv := httptest.NewServer(http.HandlerFunc(ing.handler))
	defer srv.Close()
	q := NewQueue(100)
	q.Push("first")
	q.Push("second")
	stop := runStreamer(t, newStreamer(srv.URL), q)
	defer stop()
	got := ing.waitLines(t, 2)
	if got[0] != "first" || got[1] != "second" {
		t.Fatalf("got %v", got)
	}
}

func TestStreamerReconnectsWhenServerEndsStream(t *testing.T) {
	ing := &fakeIngest{cut: func(conn int) int {
		if conn == 1 {
			return 2
		}
		return 0
	}}
	srv := httptest.NewServer(http.HandlerFunc(ing.handler))
	defer srv.Close()
	q := NewQueue(1000)
	stop := runStreamer(t, newStreamer(srv.URL), q)
	defer stop()
	for i := 0; i < 50; i++ {
		q.Push(fmt.Sprint("n", i))
		time.Sleep(time.Millisecond)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		g := ing.got()
		if len(g) > 0 && g[len(g)-1] == "n49" {
			ing.mu.Lock()
			conns := ing.conns
			ing.mu.Unlock()
			if conns < 2 {
				t.Fatalf("expected a reconnect, conns = %d", conns)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("last line never arrived: %v", ing.got())
}

func TestStreamerBacksOffWhenServerDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	q := NewQueue(10)
	q.Push("kept")
	s := newStreamer(url)
	stop := runStreamer(t, s, q)
	time.Sleep(60 * time.Millisecond)
	stop()
	if q.Len() != 1 {
		t.Fatalf("line lost while server was down: len %d", q.Len())
	}
}

func TestStreamerStopsPromptlyOnCancelWhenIdle(t *testing.T) {
	q := NewQueue(10)
	stop := runStreamer(t, newStreamer("http://127.0.0.1:1"), q)
	stop()
}

func TestStreamerAppliesRateLimit(t *testing.T) {
	ing := &fakeIngest{}
	srv := httptest.NewServer(http.HandlerFunc(ing.handler))
	defer srv.Close()
	c := &fakeClock{now: time.Unix(0, 0)}
	cfg := StreamerConfig{URL: srv.URL, Source: "s", MaxBytesPerSec: 100,
		BackoffMin: time.Millisecond, BackoffMax: time.Millisecond, Logf: func(string, ...any) {},
		Now: c.Now, Sleep: c.Sleep}
	q := NewQueue(100)
	stop := runStreamer(t, NewStreamer(cfg), q)
	defer stop()
	for i := 0; i < 5; i++ {
		q.Push(strings.Repeat("x", 99)) // 100 bytes with the newline
	}
	ing.waitLines(t, 5)
	if c.slept < 3*time.Second {
		t.Fatalf("limiter not used, slept %v", c.slept)
	}
}

func TestStreamURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://h:1", "http://h:1/v1/stream?source=s"},
		{"http://h:1/", "http://h:1/v1/stream?source=s"},
		{"http://h:1/base/", "http://h:1/base/v1/stream?source=s"},
	}
	for _, c := range cases {
		got, err := streamURL(c.in, "s", "")
		if err != nil || got != c.want {
			t.Errorf("streamURL(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := streamURL("://bad", "s", ""); err == nil {
		t.Error("bad URL accepted")
	}
	if _, err := streamURL("ftp://h", "s", ""); err == nil {
		t.Error("non-http scheme accepted")
	}
}

func TestSanitizeSource(t *testing.T) {
	cases := map[string]string{
		"my-pc":                 "my-pc",
		"Gaming PC (main)!":     "Gaming-PC-main",
		"":                      "feed",
		"   ":                   "feed",
		strings.Repeat("a", 80): strings.Repeat("a", 48),
	}
	for in, want := range cases {
		if got := SanitizeSource(in); got != want {
			t.Errorf("SanitizeSource(%q) = %q, want %q", in, got, want)
		}
	}
}

// eventLog collects the events a Streamer reports.
type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) add(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) kinds() []EventKind {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []EventKind
	for _, e := range l.events {
		out = append(out, e.Kind)
	}
	return out
}

func (l *eventLog) first(kind EventKind) (Event, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.events {
		if e.Kind == kind {
			return e, true
		}
	}
	return Event{}, false
}

func waitEvent(t *testing.T, l *eventLog, kind EventKind) Event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := l.first(kind); ok {
			return e
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("event %d never arrived, got %v", kind, l.kinds())
	return Event{}
}

func observedStreamer(url string, l *eventLog) *Streamer {
	return NewStreamer(StreamerConfig{
		URL: url, Source: "my-pc", BackoffMin: time.Millisecond, BackoffMax: 5 * time.Millisecond,
		Observe: l.add,
	})
}

func TestStreamerObserveReportsConnectSendAndServerReply(t *testing.T) {
	ing := &fakeIngest{}
	srv := httptest.NewServer(http.HandlerFunc(ing.handler))
	defer srv.Close()
	events := &eventLog{}
	q := NewQueue(10)
	q.Push("abc")
	q.Push("de")
	stop := runStreamer(t, observedStreamer(srv.URL, events), q)
	ing.waitLines(t, 2)
	stop() // a clean stop closes the stream, so the server's summary arrives
	if _, ok := events.first(EventConnecting); !ok {
		t.Fatalf("no connecting event: %v", events.kinds())
	}
	lines, bytes := 0, 0
	events.mu.Lock()
	for _, e := range events.events {
		if e.Kind == EventSent {
			lines += e.Lines
			bytes += e.Bytes
		}
	}
	events.mu.Unlock()
	if lines != 2 || bytes != len("abc\nde\n") {
		t.Fatalf("sent %d lines %d bytes", lines, bytes)
	}
	ended := waitEvent(t, events, EventEnded)
	if ended.Status != http.StatusOK || ended.Body != `{"ended":"eof"}` || ended.Err != nil {
		t.Fatalf("ended = %+v", ended)
	}
}

func TestStreamerObserveReportsFailureAndBackoff(t *testing.T) {
	ing := &fakeIngest{status: func(int) int { return http.StatusServiceUnavailable }}
	srv := httptest.NewServer(http.HandlerFunc(ing.handler))
	defer srv.Close()
	events := &eventLog{}
	q := NewQueue(10)
	q.Push("x")
	stop := runStreamer(t, observedStreamer(srv.URL, events), q)
	defer stop()
	ended := waitEvent(t, events, EventEnded)
	if ended.Status != http.StatusServiceUnavailable || ended.Err == nil {
		t.Fatalf("ended = %+v", ended)
	}
	if b := waitEvent(t, events, EventBackoff); b.Wait <= 0 {
		t.Fatalf("backoff = %+v", b)
	}
}

func TestStreamerObserveReportsNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	events := &eventLog{}
	q := NewQueue(10)
	q.Push("x")
	stop := runStreamer(t, observedStreamer(url, events), q)
	defer stop()
	ended := waitEvent(t, events, EventEnded)
	if ended.Err == nil || ended.Status != 0 {
		t.Fatalf("ended = %+v", ended)
	}
}

// TestKeyNeverReachesLogsOrEvents: neither a rejection nor a dead server puts the key into the
// log callback, the error text or the reported events (secrets policy).
func TestKeyNeverReachesLogsOrEvents(t *testing.T) {
	const key = "test-key-0000000000000000"
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	ing := &fakeIngest{status: func(int) int { return http.StatusServiceUnavailable }}
	rejecting := httptest.NewServer(http.HandlerFunc(ing.handler))
	defer rejecting.Close()
	for _, url := range []string{deadURL, rejecting.URL} {
		var mu sync.Mutex
		var seen strings.Builder
		record := func(s string) { mu.Lock(); seen.WriteString(s + "\n"); mu.Unlock() }
		events := &eventLog{}
		s := NewStreamer(StreamerConfig{
			URL: url, Key: key, Source: "my-pc", BackoffMin: time.Millisecond, BackoffMax: 5 * time.Millisecond,
			Observe: func(e Event) {
				events.add(e)
				record(fmt.Sprintf("%v %v %s", e.Kind, e.Err, e.Body))
			},
			Logf: func(f string, a ...any) { record(fmt.Sprintf(f, a...)) },
		})
		q := NewQueue(10)
		q.Push("x")
		stop := runStreamer(t, s, q)
		waitEvent(t, events, EventEnded)
		waitEvent(t, events, EventBackoff)
		stop()
		mu.Lock()
		if strings.Contains(seen.String(), key) {
			t.Errorf("key leaked for %s: %s", url, seen.String())
		}
		mu.Unlock()
	}
}
