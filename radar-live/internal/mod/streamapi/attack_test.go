package streamapi

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
)

// fakeClock drives the rate limiter: sleeping advances time, so a flood of "seconds" runs at once.
type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	slept time.Duration
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) total() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.slept
}

func newClockRig(t *testing.T, args ...string) (*rig, *fakeClock) {
	t.Helper()
	r := newRig(t, true, args...)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	r.mod.now = clock.now
	r.mod.sleep = func(_, _ context.Context, d time.Duration) error {
		clock.mu.Lock()
		clock.t = clock.t.Add(d)
		clock.slept += d
		clock.mu.Unlock()
		return nil
	}
	return r, clock
}

func lines(n int) string { return strings.Repeat("ann 1\n", n) }

func TestSourceLineRateSlowsTheReader(t *testing.T) {
	r, clock := newClockRig(t, "--stream-line-rate=100", "--stream-line-burst=100", "--stream-rate-patience=1h")
	_, data := r.post(t, "/v1/stream?source=flood", "", lines(1000))
	sum := decode[summary](t, data)
	if sum.Ended != endedEOF || sum.Lines != 1000 {
		t.Fatalf("%+v", sum)
	}
	// 1000 lines at 100/s with a burst of 100 take about 9 s.
	if got := clock.total(); got < 8500*time.Millisecond || got > 9500*time.Millisecond {
		t.Fatalf("slowed by %v", got)
	}
	if sum.Throttled < 8500 {
		t.Fatalf("throttledMs %d", sum.Throttled)
	}
}

func TestSustainedFloodEndsWithRate(t *testing.T) {
	r, _ := newClockRig(t, "--stream-line-rate=100", "--stream-line-burst=100", "--stream-rate-patience=2s")
	_, data := r.post(t, "/v1/stream?source=flood", "", lines(10_000))
	sum := decode[summary](t, data)
	if sum.Ended != endedRate {
		t.Fatalf("ended %q", sum.Ended)
	}
	if sum.Lines < 100 || sum.Lines > 400 { // burst 100 plus about 2 s of 100/s
		t.Fatalf("accepted %d lines before the cut", sum.Lines)
	}
}

func TestPauseResetsThePatience(t *testing.T) {
	r, clock := newClockRig(t, "--stream-line-rate=100", "--stream-line-burst=100", "--stream-rate-patience=2s")
	pw, resp := r.openStream(t, "?source=bursty")
	for i := 0; i < 3; i++ {
		fmt.Fprint(pw, lines(150))
		want := int64(150 * (i + 1))
		waitUntil(t, func() bool { s := r.sources(t); return len(s) == 1 && s[0].Lines >= want })
		clock.advance(10 * time.Second) // the sender pauses, the bucket refills
	}
	pw.Close()
	if _, sum := readSummary(t, resp); sum.Ended != endedEOF || sum.Lines != 450 {
		t.Fatalf("%+v", sum)
	}
}

func TestSourceByteRate(t *testing.T) {
	r, clock := newClockRig(t, "--stream-line-rate=0", "--stream-byte-rate=1000", "--stream-byte-burst=1000", "--stream-rate-patience=1h")
	_, data := r.post(t, "/v1/stream?source=fat", "", strings.Repeat(strings.Repeat("a", 99)+"\n", 100)) // 10000 bytes
	if sum := decode[summary](t, data); sum.Ended != endedEOF || sum.Lines != 100 {
		t.Fatalf("%+v", sum)
	}
	if got := clock.total(); got < 8*time.Second || got > 10*time.Second {
		t.Fatalf("slowed by %v", got)
	}
}

func (r *rig) withKey(t *testing.T, key, method, path, body string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, r.srv.URL+path, strings.NewReader(body))
	req.Header.Set("X-Ingest-Key", key)
	return do(t, req)
}

func acceptKeys(r *rig) {
	r.host.Auth.Add(core.RoleIngest, core.AuthenticatorFunc(func(q *http.Request) bool {
		return strings.HasPrefix(q.Header.Get("X-Ingest-Key"), "k")
	}))
}

func TestKeyRateIsSharedAcrossSources(t *testing.T) {
	r, clock := newClockRig(t, "--stream-line-rate=0", "--stream-byte-rate=0",
		"--stream-key-line-rate=100", "--stream-key-line-burst=100", "--stream-rate-patience=1h")
	acceptKeys(r)
	for _, src := range []string{"a", "b", "c"} { // three sources of one key share the budget
		r.withKey(t, "k1", http.MethodPost, "/v1/stream?source="+src, lines(100))
	}
	if shared := clock.total(); shared < 1900*time.Millisecond { // 300 lines, burst 100, 100/s
		t.Fatalf("one key slowed by only %v over three sources", shared)
	}
	before := clock.total()
	r.withKey(t, "k2", http.MethodPost, "/v1/stream?source=d", lines(100))
	if clock.total() != before {
		t.Fatal("a second key was slowed by the first one's traffic")
	}
}

func TestBatchOverTheRateIsRefusedWith429(t *testing.T) {
	r, _ := newClockRig(t, "--stream-line-rate=10", "--stream-line-burst=10", "--stream-rate-patience=1s")
	resp, data := r.post(t, "/v1/lines", "", lines(500))
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(data), `"rate"`) || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	if resp, _ = r.post(t, "/v1/lines", "", lines(5)); resp.StatusCode != 200 {
		t.Fatalf("small batch after refusal: %d", resp.StatusCode)
	}
}

func TestRateSettingsHaveEnvNamesAndValidate(t *testing.T) {
	r := newRig(t, false)
	for key, want := range map[string]string{SettingLineRate: "RADAR_STREAM_LINE_RATE", SettingByteRate: "RADAR_STREAM_BYTE_RATE",
		SettingKeyLineRate: "RADAR_STREAM_KEY_LINE_RATE", SettingMaxPerKey: "RADAR_STREAM_MAX_PER_KEY"} {
		if got := r.host.Config.EnvName(key); got != want {
			t.Fatalf("%s -> %s", key, got)
		}
	}
	bad := newRig(t, false, "--stream-line-rate=-5")
	if err := bad.mod.Start(context.Background()); err == nil {
		t.Fatal("negative rate accepted")
	}
}

func TestMaxConcurrentStreamsPerKeyAndGlobal(t *testing.T) {
	r := newRig(t, true, "--stream-max-per-key=2", "--stream-max-streams=3", "--max-sources=10")
	acceptKeys(r)
	open := func(key, src string) (*io.PipeWriter, chan *http.Response) {
		pr, pw := io.Pipe()
		req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/stream?source="+src, pr)
		req.Header.Set("X-Ingest-Key", key)
		out := make(chan *http.Response, 1)
		go func() {
			if resp, err := http.DefaultClient.Do(req); err == nil {
				out <- resp
			}
		}()
		return pw, out
	}
	a, ca := open("k1", "a")
	b, cb := open("k1", "b")
	waitUntil(t, func() bool { return len(r.sources(t)) == 2 })
	resp, data := r.withKey(t, "k1", http.MethodPost, "/v1/lines?source=c", "x 1\n")
	if resp.StatusCode != 429 || !strings.Contains(string(data), "too_many_streams") {
		t.Fatalf("per key: %d %s", resp.StatusCode, data)
	}
	c, cc := open("k2", "d") // third stream overall, other key: allowed
	waitUntil(t, func() bool { return len(r.sources(t)) == 3 })
	resp, data = r.withKey(t, "k3", http.MethodPost, "/v1/lines?source=e", "x 1\n")
	if resp.StatusCode != 429 || !strings.Contains(string(data), "too_many_streams") {
		t.Fatalf("global: %d %s", resp.StatusCode, data)
	}
	a.Close()
	readSummary(t, ca)
	if resp, _ = r.withKey(t, "k1", http.MethodPost, "/v1/lines?source=c", "x 1\n"); resp.StatusCode != 200 {
		t.Fatalf("slot not freed: %d", resp.StatusCode)
	}
	b.Close()
	c.Close()
	readSummary(t, cb)
	readSummary(t, cc)
}

func TestManySimultaneousConnectionsStayBounded(t *testing.T) {
	r := newRig(t, true, "--max-sources=8", "--stream-max-streams=8", "--stream-max-per-key=0")
	var wg sync.WaitGroup
	var mu sync.Mutex
	var writers []*io.PipeWriter
	ok, refused := 0, 0
	for i := 0; i < 120; i++ {
		pr, pw := io.Pipe()
		writers = append(writers, pw)
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/v1/stream?source=s%d", r.srv.URL, i), pr)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
			mu.Lock()
			if resp.StatusCode == http.StatusTooManyRequests {
				refused++
			} else {
				ok++
			}
			mu.Unlock()
		}()
	}
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return refused >= 112 })
	if n := len(r.sources(t)); n != 8 {
		t.Fatalf("%d live sources, cap 8", n)
	}
	for _, w := range writers {
		w.Close()
	}
	wg.Wait()
	waitUntil(t, func() bool { return len(r.sources(t)) == 0 })
	if ok != 8 {
		t.Fatalf("ok %d", ok)
	}
}

func TestSlowlorisDribbleIsCut(t *testing.T) {
	r := newRig(t, true, "--stream-idle=5s", "--stream-line-timeout=250ms")
	pw, resp := r.openStream(t, "?source=slow")
	go func() { // one byte every 50 ms, never a newline: the idle timer never fires
		for i := 0; i < 200; i++ {
			if _, err := pw.Write([]byte("x")); err != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	start := time.Now()
	status, sum := readSummary(t, resp)
	if status != 200 || sum.Ended != endedSlow {
		t.Fatalf("%d %+v", status, sum)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("cut took %v", time.Since(start))
	}
	pw.Close()
}

func TestClientThatNeverSendsOrClosesIsCutByIdle(t *testing.T) {
	r := newRig(t, true, "--stream-idle=150ms")
	_, resp := r.openStream(t, "?source=mute")
	_, sum := readSummary(t, resp)
	if sum.Ended != endedIdle || sum.Lines != 0 {
		t.Fatalf("%+v", sum)
	}
	waitUntil(t, func() bool { return len(r.sources(t)) == 0 })
}

func TestTenThousandTinyLinesPerSecond(t *testing.T) {
	r := newRig(t, true, "--stream-line-rate=1000", "--stream-line-burst=1000", "--stream-rate-patience=300ms")
	start := time.Now()
	_, data := r.post(t, "/v1/stream?source=tiny", "", strings.Repeat("\n", 200_000))
	sum := decode[summary](t, data)
	if sum.Ended != endedRate {
		t.Fatalf("%+v", sum)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestBinaryGarbageIsRejectedNotFatal(t *testing.T) {
	r := newRig(t, true, "--stream-byte-rate=0", "--stream-key-byte-rate=0", "--stream-line-rate=0", "--stream-key-line-rate=0")
	rng := rand.New(rand.NewPCG(1, 2))
	junk := make([]byte, 4<<20)
	for i := range junk {
		junk[i] = byte(rng.UintN(256))
	}
	resp, data := r.post(t, "/v1/stream?source=junk", "", string(junk))
	sum := decode[summary](t, data)
	if resp.StatusCode != 200 || sum.Ended != endedEOF || sum.Lines == 0 {
		t.Fatalf("%d %+v", resp.StatusCode, sum)
	}
	if resp, _ = r.post(t, "/v1/stream?source=after", "", "ann 1\n"); resp.StatusCode != 200 {
		t.Fatalf("server unhealthy after garbage: %d", resp.StatusCode)
	}
}

// endless yields n bytes of 'a' and never a newline.
type endless struct{ n int }

func (e *endless) Read(p []byte) (int, error) {
	if e.n <= 0 {
		return 0, io.EOF
	}
	k := min(len(p), e.n)
	for i := range p[:k] {
		p[i] = 'a'
	}
	e.n -= k
	return k, nil
}

func TestOverlongLineDoesNotGrowMemory(t *testing.T) {
	r := newRig(t, true, "--stream-byte-rate=0", "--stream-key-byte-rate=0", "--stream-max-bytes=1073741824")
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/stream?source=long", &endless{n: 256 << 20})
	resp, data := do(t, req)
	runtime.ReadMemStats(&after)
	sum := decode[summary](t, data)
	if resp.StatusCode != 200 || sum.Rejected != 1 || sum.Lines != 1 || sum.Events != 0 {
		t.Fatalf("%d %+v", resp.StatusCode, sum)
	}
	// 256 MiB went through; the reader may hold at most the line cap plus its buffers. The client
	// side of the loopback also allocates, hence the generous bound.
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 96<<20 {
		t.Fatalf("allocated %d MiB for a 256 MiB line", grown>>20)
	}
}

func TestSourcesReportsRejectedAndThrottled(t *testing.T) {
	r, _ := newClockRig(t, "--stream-line-rate=100", "--stream-line-burst=100", "--stream-rate-patience=1h")
	pw, resp := r.openStream(t, "?source=watch")
	fmt.Fprint(pw, "bad\n"+lines(300))
	waitUntil(t, func() bool { s := r.sources(t); return len(s) == 1 && s[0].Lines == 301 })
	s := r.sources(t)[0]
	if s.Rejected != 1 || s.ThrottledMs == 0 {
		t.Fatalf("%+v", s)
	}
	pw.Close()
	readSummary(t, resp)
}

func TestUnauthenticatedEndlessBodyIsRefusedAtOnce(t *testing.T) {
	r := newRig(t, true)
	acceptKeys(r)
	pr, pw := io.Pipe() // a body that never ends and never sends
	defer pw.Close()
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/stream?source=x", pr)
	done := make(chan int, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- -1
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	select {
	case code := <-done:
		if code != http.StatusUnauthorized {
			t.Fatalf("status %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the refusal waited for the endless body")
	}
}
