package ratelimit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock, safe for concurrent reads.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// perMin builds a budget of n tokens per minute with the given burst.
func perMin(n, burst float64) budget { return budget{rate: n / 60, burst: burst} }

func ok200() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

// rig is a limiter around a trivial handler with a fake clock.
type rig struct {
	clock *fakeClock
	lim   *Limiter
	h     http.Handler
}

func newRig(t *testing.T, mutate func(*limiterConfig), next http.Handler) *rig {
	t.Helper()
	cfg := defaultConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	clock := newClock()
	lim := newLimiter(cfg, clock.Now)
	if next == nil {
		next = ok200()
	}
	return &rig{clock: clock, lim: lim, h: lim.Wrap(next)}
}

func (g *rig) do(method, path, remote string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = remote
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	g.h.ServeHTTP(w, r)
	return w
}

func TestBucketMathAndRefill(t *testing.T) {
	clock := newClock()
	tb := newTable(100, time.Minute)
	k := entryKey{class: classSweep}
	b := perMin(60, 3) // 1 token per second, burst 3
	for i := 0; i < 3; i++ {
		if d := tb.take(k, b, clock.Now(), true); !d.allowed {
			t.Fatalf("burst request %d denied", i)
		}
	}
	d := tb.take(k, b, clock.Now(), true)
	if d.allowed || d.retry != time.Second {
		t.Fatalf("4th request: allowed=%v retry=%v, want denied retry 1s", d.allowed, d.retry)
	}
	clock.Advance(500 * time.Millisecond)
	d = tb.take(k, b, clock.Now(), true)
	if d.allowed || d.retry != time.Second {
		t.Fatalf("after 0.5s: allowed=%v retry=%v", d.allowed, d.retry)
	}
	clock.Advance(500 * time.Millisecond)
	if d = tb.take(k, b, clock.Now(), true); !d.allowed {
		t.Fatal("token should have refilled after 1s")
	}
	clock.Advance(time.Hour)
	for i := 0; i < 3; i++ {
		if d = tb.take(k, b, clock.Now(), true); !d.allowed {
			t.Fatalf("refill must cap at burst, request %d denied", i)
		}
	}
	if d = tb.take(k, b, clock.Now(), true); d.allowed {
		t.Fatal("refill exceeded burst")
	}
}

func TestBucketBackwardsClockDoesNotMintTokens(t *testing.T) {
	clock := newClock()
	tb := newTable(10, time.Minute)
	k := entryKey{class: classPage}
	b := perMin(60, 1)
	tb.take(k, b, clock.Now(), true)
	clock.Advance(-time.Hour)
	if d := tb.take(k, b, clock.Now(), true); d.allowed {
		t.Fatal("a clock that moved backwards granted a token")
	}
}

func TestPerIPBudgetsPerClass(t *testing.T) {
	g := newRig(t, func(c *limiterConfig) {
		c.ip[classSweep] = perMin(30, 2)
		c.ip[classSearch] = perMin(60, 3)
		c.ip[classPage] = perMin(120, 4)
	}, nil)
	const peer = "198.51.100.7:4000"
	cases := []struct {
		path  string
		burst int
	}{{"/api/v1/sweep", 2}, {"/api/v1/contacts", 3}, {"/index.html", 4}}
	for _, c := range cases {
		for i := 0; i < c.burst; i++ {
			if w := g.do("GET", c.path, peer); w.Code != 200 {
				t.Fatalf("%s request %d: %d", c.path, i, w.Code)
			}
		}
		if w := g.do("GET", c.path, peer); w.Code != http.StatusTooManyRequests {
			t.Fatalf("%s over budget: %d, want 429", c.path, w.Code)
		}
	}
	// Exhausting sweep does not touch another class, and another IP has its own budget.
	if w := g.do("POST", "/v1/lines", peer); w.Code != 200 {
		t.Fatalf("ingest shares a bucket with page/sweep/search? got %d", w.Code)
	}
	if w := g.do("GET", "/api/v1/sweep", "198.51.100.8:1"); w.Code != 200 {
		t.Fatalf("second IP got %d", w.Code)
	}
}

func TestScrapeBudgetTighterThanPage(t *testing.T) {
	d := defaultConfig()
	if !(d.ip[classSweep].rate < d.ip[classPage].rate) || !(d.ip[classSearch].rate < d.ip[classPage].rate) {
		t.Fatalf("sweep/search must be tighter than page: sweep=%v search=%v page=%v",
			d.ip[classSweep], d.ip[classSearch], d.ip[classPage])
	}
	if d.ip[classSweep].rate*60 != 30 || d.ip[classSearch].rate*60 != 60 || d.ip[classPage].rate*60 != 120 {
		t.Fatal("documented defaults (sweep 30, search 60, page 120 per minute) changed")
	}
	if d.key[classWrite].rate*60 != 600 {
		t.Fatal("write default must be 600 per minute per key")
	}
	// One page poll per 60 s is far under the sweep budget.
	if d.ip[classSweep].rate*60 < 5 {
		t.Fatal("sweep budget too small for normal polling")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		method, path string
		want         class
		keyed        bool
	}{
		{"GET", "/", classPage, false},
		{"GET", "/app.js", classPage, false},
		{"GET", "/api/v1/status", classPage, false},
		{"GET", "/api/v1/sweep", classSweep, false},
		{"GET", "/api/v1/contacts", classSearch, false},
		{"GET", "/api/v1/trail", classSearch, false},
		{"GET", "/api/v1/bounty", classSearch, false},
		{"GET", "/api/v1/bounty/player", classSearch, false},
		{"GET", "/api/v1/charts", classSearch, false},
		{"POST", "/internal/v1/ping", classWrite, true},
		{"POST", "/v1/stream", classIngest, true},
		{"POST", "/v1/lines", classIngest, true},
		{"GET", "/v1/sources", classIngest, true},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		got, keyed := classify(r)
		if got != c.want || keyed != c.keyed {
			t.Errorf("%s %s: got (%v,%v), want (%v,%v)", c.method, c.path, got, keyed, c.want, c.keyed)
		}
	}
}

func TestPerKeyLimiter(t *testing.T) {
	g := newRig(t, func(c *limiterConfig) {
		c.key[classWrite] = perMin(60, 2)
		c.ip[classWrite] = perMin(6000, 1000)
	}, nil)
	post := func(remote, key string) int {
		return g.do("POST", "/internal/v1/ping", remote, "X-Ingest-Key", key).Code
	}
	// The same key from many different addresses shares one budget.
	for i := 0; i < 2; i++ {
		if c := post(fmt.Sprintf("198.51.100.%d:1", i+1), "test-key-aaaaaaaaaaaaaaaa"); c != 200 {
			t.Fatalf("request %d: %d", i, c)
		}
	}
	if c := post("198.51.100.99:1", "test-key-aaaaaaaaaaaaaaaa"); c != http.StatusTooManyRequests {
		t.Fatalf("key budget not shared across IPs: %d", c)
	}
	// A different key from the same address is unaffected; Bearer is the same identity as the header.
	if c := post("198.51.100.1:1", "test-key-bbbbbbbbbbbbbbbb"); c != 200 {
		t.Fatalf("other key got %d", c)
	}
	if w := g.do("POST", "/internal/v1/ping", "198.51.100.50:1", "Authorization", "Bearer test-key-aaaaaaaaaaaaaaaa"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("Bearer form of the exhausted key got %d", w.Code)
	}
	// Refill restores the key.
	g.clock.Advance(2 * time.Second)
	if c := post("198.51.100.77:1", "test-key-aaaaaaaaaaaaaaaa"); c != 200 {
		t.Fatalf("after refill: %d", c)
	}
}

func TestKeyRotationDoesNotBypassIPBudget(t *testing.T) {
	g := newRig(t, func(c *limiterConfig) {
		c.ip[classWrite] = perMin(60, 3)
		c.key[classWrite] = perMin(600, 100)
	}, nil)
	denied := 0
	for i := 0; i < 20; i++ {
		if g.do("POST", "/internal/v1/ping", "198.51.100.5:1", "X-Ingest-Key", "guess-"+strconv.Itoa(i)).Code == 429 {
			denied++
		}
	}
	if denied != 17 {
		t.Fatalf("denied %d of 20 rotating-key requests, want 17", denied)
	}
}

func TestKeyIdentityNeverStoresRawKey(t *testing.T) {
	const raw = "test-key-0000000000000000"
	id := keyID(raw)
	if strings.Contains(fmt.Sprintf("%x %s %v", id, id, id), raw) {
		t.Fatal("key id contains the raw key")
	}
	if keyID(raw) != id || keyID(raw+"x") == id {
		t.Fatal("key id must be deterministic and key specific")
	}
	g := newRig(t, nil, nil)
	g.do("POST", "/internal/v1/ping", "198.51.100.1:1", "X-Ingest-Key", raw)
	for _, s := range g.lim.table.shards {
		for k := range s.m {
			if strings.Contains(string(k.id[:]), raw) {
				t.Fatal("raw key found in table")
			}
		}
	}
}

func TestOverLimitGets429(t *testing.T) {
	var reached atomic.Int32
	g := newRig(t, func(c *limiterConfig) {
		c.ip[classSweep] = perMin(30, 1)
		c.headers = true
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached.Add(1) }))
	if w := g.do("GET", "/api/v1/sweep", "198.51.100.1:1"); w.Code != 200 || w.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("first: %d remaining=%q", w.Code, w.Header().Get("X-RateLimit-Remaining"))
	}
	w := g.do("GET", "/api/v1/sweep", "198.51.100.1:1")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra != "2" {
		t.Fatalf("Retry-After %q, want 2 (30/min = one token per 2 s)", ra)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] != "rate_limited" || body["message"] == "" {
		t.Fatalf("body %q (%v)", w.Body.String(), err)
	}
	if w.Header().Get("X-RateLimit-Remaining") != "0" || w.Header().Get("X-RateLimit-Limit") != "1" {
		t.Fatalf("rate limit headers on 429: %v", w.Header())
	}
	if reached.Load() != 1 {
		t.Fatalf("handler reached %d times, want 1", reached.Load())
	}
}

func TestRateLimitHeadersOffByDefault(t *testing.T) {
	g := newRig(t, nil, nil)
	if w := g.do("GET", "/", "198.51.100.1:1"); w.Header().Get("X-RateLimit-Limit") != "" {
		t.Fatal("headers must be opt-in")
	}
}

func TestHealthzFromLoopbackIsNeverLimited(t *testing.T) {
	g := newRig(t, func(c *limiterConfig) { c.ip[classPage] = perMin(60, 1) }, nil)
	for i := 0; i < 50; i++ {
		if w := g.do("GET", "/healthz", "127.0.0.1:5"); w.Code != 200 {
			t.Fatalf("loopback healthz %d: %d", i, w.Code)
		}
		if w := g.do("GET", "/healthz", "[::1]:5"); w.Code != 200 {
			t.Fatalf("loopback v6 healthz %d: %d", i, w.Code)
		}
	}
	// A remote caller is limited like a page.
	g.do("GET", "/healthz", "198.51.100.1:1")
	if w := g.do("GET", "/healthz", "198.51.100.1:1"); w.Code != 429 {
		t.Fatalf("remote healthz not limited: %d", w.Code)
	}
}

func TestAuthFailureBudget(t *testing.T) {
	deny := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-View-Key") != "good-key-0000000000" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	g := newRig(t, func(c *limiterConfig) { c.ip[classAuthFail] = perMin(60, 3) }, deny)
	const bad = "198.51.100.9:1"
	for i := 0; i < 3; i++ {
		if w := g.do("GET", "/api/v1/status", bad, "X-View-Key", "wrong"); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	// Budget spent: even the right key is refused until it refills, then it works.
	if w := g.do("GET", "/api/v1/status", bad, "X-View-Key", "good-key-0000000000"); w.Code != 429 {
		t.Fatalf("after 3 failures: %d, want 429", w.Code)
	}
	if w := g.do("GET", "/api/v1/status", "198.51.100.10:1", "X-View-Key", "good-key-0000000000"); w.Code != 200 {
		t.Fatalf("other IP: %d", w.Code)
	}
	g.clock.Advance(time.Second)
	if w := g.do("GET", "/api/v1/status", bad, "X-View-Key", "good-key-0000000000"); w.Code != 200 {
		t.Fatalf("after refill: %d", w.Code)
	}
}

func TestSpoofedForwardedForDoesNotEvadeLimit(t *testing.T) {
	g := newRig(t, func(c *limiterConfig) { c.ip[classSweep] = perMin(30, 2) }, nil)
	for i := 0; i < 2; i++ {
		g.do("GET", "/api/v1/sweep", "203.0.113.5:1", "X-Forwarded-For", fmt.Sprintf("1.1.1.%d", i))
	}
	if w := g.do("GET", "/api/v1/sweep", "203.0.113.5:1", "X-Forwarded-For", "1.1.1.200"); w.Code != 429 {
		t.Fatalf("rotating X-Forwarded-For from an untrusted peer evaded the limit: %d", w.Code)
	}
}

func TestTrustedLoopbackUsesRealClientBudgets(t *testing.T) {
	trusted, _ := parseTrusted("127.0.0.1")
	g := newRig(t, func(c *limiterConfig) {
		c.trusted = trusted
		c.ip[classSweep] = perMin(30, 1)
	}, nil)
	if w := g.do("GET", "/api/v1/sweep", "127.0.0.1:1", "CF-Connecting-IP", "198.51.100.1"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := g.do("GET", "/api/v1/sweep", "127.0.0.1:2", "CF-Connecting-IP", "198.51.100.1"); w.Code != 429 {
		t.Fatalf("same real client through the tunnel: %d", w.Code)
	}
	if w := g.do("GET", "/api/v1/sweep", "127.0.0.1:3", "CF-Connecting-IP", "198.51.100.2"); w.Code != 200 {
		t.Fatalf("different real client behind the same proxy: %d", w.Code)
	}
}

func TestIPv6Aggregation(t *testing.T) {
	a := ipKey(netip.MustParseAddr("2001:db8:1:2:aaaa:bbbb:cccc:dddd"))
	b := ipKey(netip.MustParseAddr("2001:db8:1:2:1111:2222:3333:4444"))
	c := ipKey(netip.MustParseAddr("2001:db8:1:3::1"))
	if a != b {
		t.Fatal("addresses in one /64 must share a key")
	}
	if a == c {
		t.Fatal("different /64s must not share a key")
	}
	if ipKey(netip.MustParseAddr("192.0.2.1")) == ipKey(netip.MustParseAddr("192.0.2.2")) {
		t.Fatal("IPv4 addresses are keyed individually")
	}
	g := newRig(t, func(c *limiterConfig) { c.ip[classSweep] = perMin(30, 2) }, nil)
	for i := 0; i < 2; i++ {
		g.do("GET", "/api/v1/sweep", fmt.Sprintf("[2001:db8:1:2::%x]:1", i+1))
	}
	if w := g.do("GET", "/api/v1/sweep", "[2001:db8:1:2:ffff::9]:1"); w.Code != 429 {
		t.Fatalf("a /64 rotating addresses evaded the limit: %d", w.Code)
	}
}

func TestTableEvictsIdleEntries(t *testing.T) {
	clock := newClock()
	tb := newTableShards(64, time.Minute, 1)
	b := perMin(60, 5)
	fill := func(prefix int) {
		for i := 0; i < 64; i++ {
			tb.take(entryKey{class: classPage, id: [16]byte{byte(prefix), byte(i)}}, b, clock.Now(), true)
		}
	}
	fill(1)
	if tb.Len() == 0 {
		t.Fatal("nothing tracked")
	}
	clock.Advance(2 * time.Minute)
	before := tb.overflowHits()
	fill(2)
	if tb.overflowHits() != before {
		t.Fatal("expired entries were not evicted to make room")
	}
	if tb.Len() > 64 {
		t.Fatalf("table holds %d > cap", tb.Len())
	}
}

func TestTableBoundedUnderHashFlood(t *testing.T) {
	clock := newClock()
	const cap = 1000
	tb := newTable(cap, time.Hour)
	b := perMin(60, 5)
	for i := 0; i < 50*cap; i++ {
		var id [16]byte
		id[0], id[1], id[2], id[3] = byte(i), byte(i>>8), byte(i>>16), byte(i>>24)
		tb.take(entryKey{class: classPage, id: id}, b, clock.Now(), true)
		if i%997 == 0 && tb.Len() > cap {
			t.Fatalf("table grew to %d > cap %d", tb.Len(), cap)
		}
	}
	if n := tb.Len(); n > cap || n < cap/2 {
		t.Fatalf("table size %d, want close to but not above %d", n, cap)
	}
}

func TestTableFullFailsSafe(t *testing.T) {
	g := newRig(t, func(c *limiterConfig) {
		c.maxEntries = 64
		c.ttl = time.Hour
		c.ip[classSweep] = perMin(60, 2) // overflow bucket: burst 2*overflowFactor
	}, nil)
	// Fill the table with distinct attacker addresses.
	for i := 0; i < 5000; i++ {
		g.do("GET", "/api/v1/sweep", fmt.Sprintf("10.%d.%d.1:1", i/250, i%250))
	}
	if n := g.lim.table.Len(); n > 64 {
		t.Fatalf("table grew to %d", n)
	}
	if g.lim.table.overflowHits() == 0 {
		t.Fatal("overflow bucket never used")
	}
	// New, untracked identities now share one global bucket: eventually denied, even though each is brand new.
	denied := 0
	for i := 0; i < 5000; i++ {
		if g.do("GET", "/api/v1/sweep", fmt.Sprintf("172.16.%d.%d:1", i/250, i%250)).Code == 429 {
			denied++
		}
	}
	if denied < 4000 {
		t.Fatalf("only %d of 5000 new identities denied while the table is full", denied)
	}
	if g.lim.table.Len() > 64 {
		t.Fatal("table grew while full")
	}
}

func TestConcurrentUse(t *testing.T) {
	var passed atomic.Int64
	g := newRig(t, func(c *limiterConfig) {
		c.ip[classSweep] = perMin(60, 100)
		c.maxEntries = 256
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { passed.Add(1) }))
	var wg sync.WaitGroup
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				// 8 shared addresses (contended) plus unique ones (insertions/overflow).
				remote := fmt.Sprintf("192.0.2.%d:1", i%8)
				if i%3 == 0 {
					remote = fmt.Sprintf("10.%d.%d.%d:1", w, i/250, i%250)
				}
				g.do("GET", "/api/v1/sweep", remote, "X-Ingest-Key", "k"+strconv.Itoa(i%5))
			}
		}(w)
	}
	wg.Wait()
	if passed.Load() < 100 {
		t.Fatalf("only %d requests passed", passed.Load())
	}
	if n := g.lim.table.Len(); n > 256 {
		t.Fatalf("table %d > cap", n)
	}
}

func TestConcurrentBucketIsExact(t *testing.T) {
	clock := newClock()
	tb := newTable(100, time.Hour)
	k := entryKey{class: classWrite}
	b := perMin(60, 1000)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if tb.take(k, b, clock.Now(), true).allowed {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 1000 {
		t.Fatalf("admitted %d, want exactly the burst 1000", allowed.Load())
	}
}

func TestMemoryBound(t *testing.T) {
	clock := newClock()
	const cap = 100_000
	tb := newTable(cap, time.Hour)
	b := perMin(60, 5)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < 2_000_000; i++ {
		var id [16]byte
		id[0], id[1], id[2], id[3] = byte(i), byte(i>>8), byte(i>>16), byte(i>>24)
		id[4] = byte(i % 5)
		tb.take(entryKey{class: class(i % int(numClasses)), id: id}, b, clock.Now(), true)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	grown := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if tb.Len() > cap {
		t.Fatalf("table %d > cap", tb.Len())
	}
	if grown > 40<<20 {
		t.Fatalf("2M distinct identities grew the heap by %d MiB, want < 40 (cap %d)", grown>>20, cap)
	}
}

func TestDisabledLimiterDoesNothing(t *testing.T) {
	cfg := defaultConfig()
	cfg.ip[classSweep] = perMin(1, 1)
	cfg.enabled = false
	lim := newLimiter(cfg, newClock().Now)
	h := lim.Wrap(ok200())
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/sweep", nil))
		if w.Code != 200 {
			t.Fatalf("disabled limiter returned %d", w.Code)
		}
	}
}

func TestStreamingResponsesStillFlush(t *testing.T) {
	g := newRig(t, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("wrapped writer lost http.Flusher")
			return
		}
		f.Flush()
		if _, ok := w.(interface{ Unwrap() http.ResponseWriter }); !ok {
			t.Error("wrapped writer lost Unwrap")
		}
	}))
	g.do("POST", "/v1/stream", "198.51.100.1:1")
}

func BenchmarkAllow(b *testing.B) {
	g := newLimiter(defaultConfig(), newClock().Now)
	h := g.Wrap(ok200())
	b.RunParallel(func(pb *testing.PB) {
		r := httptest.NewRequest("GET", "/api/v1/sweep", nil)
		r.RemoteAddr = "198.51.100.1:1"
		w := httptest.NewRecorder()
		for pb.Next() {
			h.ServeHTTP(w, r)
		}
	})
}

func BenchmarkTableTake(b *testing.B) {
	tb := newTable(100_000, time.Minute)
	clock := newClock()
	bud := perMin(1e9, 1e9)
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			tb.take(entryKey{class: classPage, id: [16]byte{byte(i), byte(i >> 8)}}, bud, clock.Now(), true)
		}
	})
}
