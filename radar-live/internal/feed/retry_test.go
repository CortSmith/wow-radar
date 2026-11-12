package feed

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	rndLow  = 0.0      // jitter at its lowest
	rndMid  = 0.5      // symmetric jitter of zero
	rndHigh = 0.999999 // jitter at its highest
)

func sec(n float64) time.Duration { return time.Duration(n * float64(time.Second)) }

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	date := func(d time.Duration) string { return now.Add(d).UTC().Format(http.TimeFormat) }
	tests := []struct {
		name, value string
		want        time.Duration
		ok          bool
	}{
		{"seconds", "120", 120 * time.Second, true},
		{"padded", "  45 ", 45 * time.Second, true},
		{"zero", "0", 0, true},
		{"date", date(90 * time.Second), 90 * time.Second, true},
		{"past date", date(-time.Hour), 0, true},
		{"negative", "-5", 0, false},
		{"garbage", "soon", 0, false},
		{"empty", "", 0, false},
		{"fraction", "1.5", 0, false},
		{"huge", "99999999999999999999", 0, false},
	}
	for _, tc := range tests {
		got, ok := ParseRetryAfter(tc.value, now)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: ParseRetryAfter(%q) = %v, %v; want %v, %v", tc.name, tc.value, got, ok, tc.want, tc.ok)
		}
	}
}

func newTestBackoff(rnd float64) *backoff {
	return newBackoff(500*time.Millisecond, 30*time.Second, func() float64 { return rnd })
}

func waitsFor(b *backoff, n, status int, retryAfter string) []time.Duration {
	var out []time.Duration
	for range n {
		out = append(out, b.next(status, retryAfter, time.Unix(0, 0)))
	}
	return out
}

func TestBackoffNetworkErrorsKeepTheFastSchedule(t *testing.T) {
	got := waitsFor(newTestBackoff(rndMid), 8, 0, "")
	want := []time.Duration{sec(0.5), sec(1), sec(2), sec(4), sec(8), sec(16), sec(30), sec(30)}
	if !equalDurations(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestBackoffJitterBounds(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		lowest, high time.Duration // wait for the lowest and the highest random value
	}{
		{"network", 0, sec(0.4), sec(0.6)},
		{"server error", http.StatusBadGateway, sec(0.4), sec(0.6)},
		{"429", http.StatusTooManyRequests, sec(30), sec(36)},
		{"409", http.StatusConflict, sec(10), sec(12)},
	}
	for _, tc := range tests {
		lo := newTestBackoff(rndLow).next(tc.status, "", time.Unix(0, 0))
		hi := newTestBackoff(rndHigh).next(tc.status, "", time.Unix(0, 0))
		if lo != tc.lowest {
			t.Errorf("%s: lowest = %v, want %v", tc.name, lo, tc.lowest)
		}
		if hi < tc.high-time.Millisecond || hi > tc.high {
			t.Errorf("%s: highest = %v, want about %v", tc.name, hi, tc.high)
		}
	}
}

func TestBackoffTooManyRequestsHonoursRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name, header string
		want         time.Duration
	}{
		{"no header uses the floor", "", sec(30)},
		{"small value is raised to the floor", "5", sec(30)},
		{"seconds above the floor", "120", sec(120)},
		{"capped at 15 minutes", "86400", sec(900)},
		{"http date", now.Add(200 * time.Second).Format(http.TimeFormat), sec(200)},
		{"garbage", "later", sec(30)},
		{"negative", "-10", sec(30)},
	}
	for _, tc := range tests {
		if got := newTestBackoff(rndLow).next(http.StatusTooManyRequests, tc.header, now); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestBackoffTooManyRequestsWithoutHintGrowsSlowlyToTheCap(t *testing.T) {
	got := waitsFor(newTestBackoff(rndLow), 8, http.StatusTooManyRequests, "")
	want := []time.Duration{sec(30), sec(60), sec(120), sec(240), sec(480), sec(900), sec(900), sec(900)}
	if !equalDurations(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestBackoffSourceBusyWaitsTenSecondsDoublingToTwoMinutes(t *testing.T) {
	got := waitsFor(newTestBackoff(rndLow), 7, http.StatusConflict, "")
	want := []time.Duration{sec(10), sec(20), sec(40), sec(80), sec(120), sec(120), sec(120)}
	if !equalDurations(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestBackoffResetStartsOver(t *testing.T) {
	b := newTestBackoff(rndMid)
	waitsFor(b, 4, 0, "")
	waitsFor(b, 3, http.StatusTooManyRequests, "")
	b.reset()
	if got := b.next(0, "", time.Unix(0, 0)); got != sec(0.5) {
		t.Fatalf("network after reset = %v", got)
	}
	if got := b.next(http.StatusTooManyRequests, "", time.Unix(0, 0)); got != sec(33) {
		t.Fatalf("429 after reset = %v", got)
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// scriptedTransport answers each request with the next scripted reply (the last one repeats).
type scriptedTransport struct {
	mu      sync.Mutex
	replies []reply
	keys    []string
}

type reply struct {
	status int
	header string // Retry-After
	err    error
}

func (s *scriptedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	_ = r.Body.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, r.Header.Get(keyHeader))
	rep := s.replies[min(len(s.keys), len(s.replies))-1]
	if rep.err != nil {
		return nil, rep.err
	}
	h := http.Header{}
	if rep.header != "" {
		h.Set("Retry-After", rep.header)
	}
	return &http.Response{StatusCode: rep.status, Header: h, Body: io.NopCloser(strings.NewReader(`{"error":"x"}`)), Request: r}, nil
}

func (s *scriptedTransport) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}

// scriptedResult is what scriptedRun observed.
type scriptedResult struct {
	waits  []time.Duration
	tr     *scriptedTransport
	queue  *Queue
	events *eventLog
	err    error
}

// scriptedRun runs a streamer on the scripted replies with a fake sleep that records every wait
// and cancels the run after maxSleeps of them.
func scriptedRun(key string, replies []reply, maxSleeps int, rnd float64) *scriptedResult {
	res := &scriptedResult{tr: &scriptedTransport{replies: replies}, events: &eventLog{}, queue: NewQueue(10)}
	res.queue.Push("line")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewStreamer(StreamerConfig{
		URL: "http://ingest.test", Key: key, Source: "pc", Client: &http.Client{Transport: res.tr},
		BackoffMin: 500 * time.Millisecond, BackoffMax: 30 * time.Second,
		Rand:    func() float64 { return rnd },
		Now:     func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Observe: res.events.add,
		Sleep: func(_ context.Context, d time.Duration) error {
			res.waits = append(res.waits, d)
			if len(res.waits) >= maxSleeps {
				cancel()
				return context.Canceled
			}
			return nil
		},
	})
	res.err = s.Run(ctx, res.queue)
	return res
}

func TestStreamerStopsOnAuthRejection(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		res := scriptedRun("test-key", []reply{{status: status}}, 5, rndMid)
		if !errors.Is(res.err, ErrAuthRejected) {
			t.Fatalf("%d: err = %v", status, res.err)
		}
		wantText := "The server rejected the ingest key (401). The upload stopped; fix the key and click Start again."
		if status == http.StatusUnauthorized && res.err.Error() != wantText {
			t.Fatalf("message = %q", res.err)
		}
		if res.tr.calls() != 1 || len(res.waits) != 0 {
			t.Fatalf("%d: calls %d waits %v, want one call and no waiting", status, res.tr.calls(), res.waits)
		}
		if res.queue.Len() != 1 {
			t.Fatalf("%d: queued line lost, len %d", status, res.queue.Len())
		}
		ev, ok := res.events.first(EventAuthRejected)
		if !ok || ev.Status != status {
			t.Fatalf("%d: no EventAuthRejected: %+v", status, ev)
		}
		if _, ok := res.events.first(EventBackoff); ok {
			t.Fatalf("%d: backoff reported after a fatal rejection", status)
		}
	}
}

func TestStreamerWaitsPerStatus(t *testing.T) {
	tests := []struct {
		name  string
		reply reply
		want  []time.Duration
	}{
		{"network error", reply{err: errors.New("dial failed")}, []time.Duration{sec(0.4), sec(0.8), sec(1.6)}},
		{"server error", reply{status: http.StatusServiceUnavailable}, []time.Duration{sec(0.4), sec(0.8), sec(1.6)}},
		{"404", reply{status: http.StatusNotFound}, []time.Duration{sec(0.4), sec(0.8), sec(1.6)}},
		{"429", reply{status: http.StatusTooManyRequests}, []time.Duration{sec(30), sec(60), sec(120)}},
		{"429 Retry-After", reply{status: http.StatusTooManyRequests, header: "300"}, []time.Duration{sec(300), sec(300), sec(300)}},
		{"409", reply{status: http.StatusConflict}, []time.Duration{sec(10), sec(20), sec(40)}},
	}
	for _, tc := range tests {
		res := scriptedRun("k", []reply{tc.reply}, 3, rndLow)
		if !errors.Is(res.err, context.Canceled) {
			t.Fatalf("%s: err = %v", tc.name, res.err)
		}
		if !equalDurations(res.waits, tc.want) {
			t.Errorf("%s: waits %v want %v", tc.name, res.waits, tc.want)
		}
	}
}

func TestStreamerReportsTheWaitInTheBackoffEvent(t *testing.T) {
	res := scriptedRun("k", []reply{{status: http.StatusTooManyRequests}}, 1, rndLow)
	if b, ok := res.events.first(EventBackoff); !ok || b.Wait != sec(30) {
		t.Fatalf("backoff = %+v", b)
	}
}

func TestStreamerTrimsTheKey(t *testing.T) {
	res := scriptedRun("  test-key-123\r\n", []reply{{status: http.StatusServiceUnavailable}}, 1, rndMid)
	if len(res.tr.keys) == 0 || res.tr.keys[0] != "test-key-123" {
		t.Fatalf("sent keys %q", res.tr.keys)
	}
}

func TestCleanKey(t *testing.T) {
	for in, want := range map[string]string{"abc": "abc", " abc\n": "abc", "\r\n\tabc  ": "abc", "a b": "a b", " \n": ""} {
		if got := CleanKey(in); got != want {
			t.Errorf("CleanKey(%q) = %q, want %q", in, got, want)
		}
	}
}
