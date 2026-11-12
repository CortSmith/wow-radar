package relay

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"radarlive/internal/domain"
)

func TestDropMetricsAreExposedThroughModuleStats(t *testing.T) {
	db := &fakeDB{}
	_, srv, _ := newRelay(t, db, nil)
	h, m := startedHost(t, "--db-url="+srv.URL, "--relay-buffer-pings=2", "--relay-backoff-min=10ms")
	srv.Close() // the database is gone: everything piles up
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if err := h.Sinks.Publish(context.Background(), ping(id)); err != nil {
			t.Fatal(err)
		}
	}
	st := m.Stats()
	if st.Dropped != 3 || st.Pending != 2 || st.Bytes == 0 {
		t.Fatalf("%+v", st)
	}
	if v, ok := h.Metrics.Value("radar_relay_dropped_total"); !ok || v != 3 {
		t.Fatalf("radar_relay_dropped_total = %v, %v; want 3", v, ok)
	}
	if v, _ := h.Metrics.Value("radar_relay_pending"); v != 2 {
		t.Fatalf("radar_relay_pending = %v, want 2", v)
	}
	raw, _ := json.Marshal(st)
	for _, key := range []string{`"dropped":3`, `"pending":2`, `"retries"`, `"rejected"`, `"sent"`, `"duplicates"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("stats JSON lacks %s: %s", key, raw)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = m.Stop(ctx)
}

// countingServer answers every request with status and counts them.
func countingServer(t *testing.T, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func TestBackoffCannotSpinTheCPU(t *testing.T) {
	srv, hits := countingServer(t, http.StatusServiceUnavailable)
	r, err := New(Config{URL: srv.URL, BufferPings: 10, BufferBytes: 1 << 20, BatchPings: 10, Timeout: time.Second,
		BackoffMin: 50 * time.Millisecond, BackoffMax: 200 * time.Millisecond}, quietLog(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = r.Accept(ctx, ping("a"))
	r.Run(ctx)
	// Delays 50, 100, 200, 200, ... (plus jitter): well under 12 attempts in a second.
	if got := hits.Load(); got < 2 || got > 12 {
		t.Fatalf("%d attempts in 1 s with 50 ms minimum backoff", got)
	}
}

func TestUnreachableDatabaseDoesNotSpin(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // connection refused from now on
	var attempts atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return http.DefaultTransport.RoundTrip(r)
	})}
	r, err := New(Config{URL: url, BufferPings: 10, BufferBytes: 1 << 20, BatchPings: 10, Timeout: time.Second,
		BackoffMin: 50 * time.Millisecond, BackoffMax: 100 * time.Millisecond}, quietLog(), Options{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_ = r.Accept(ctx, ping("a"))
	r.Run(ctx)
	if got := attempts.Load(); got < 2 || got > 14 {
		t.Fatalf("%d attempts in 0.7 s", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBackoffHasAFloor(t *testing.T) {
	good := Config{URL: "http://x", BufferPings: 1, BufferBytes: 1, BatchPings: 1, Timeout: time.Second, BackoffMin: time.Second, BackoffMax: time.Second}
	for _, d := range []time.Duration{1, time.Microsecond, 9 * time.Millisecond} {
		c := good
		c.BackoffMin, c.BackoffMax = d, time.Second
		if _, err := New(c, quietLog(), Options{}); err == nil {
			t.Fatalf("backoff minimum %v accepted", d)
		}
	}
}

// SSRF: whatever the pings contain, the relay talks to the configured server and to its one path.
func TestRelayOnlyDialsTheConfiguredURL(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("evil host was dialed") }))
	defer evil.Close()
	db := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer db.Close()
	r, err := New(Config{URL: db.URL + "/", BufferPings: 10, BufferBytes: 1 << 20, BatchPings: 10, Timeout: time.Second,
		BackoffMin: time.Second, BackoffMax: time.Second}, quietLog(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	p := domain.Ping{RS: domain.DefaultRuleset(), BatchID: "ssrf-1", Source: evil.URL, Contacts: []domain.PingContact{{
		Contact: domain.Contact{GUID: "Player-1-A", Name: evil.URL + "/steal", Raw: "http://169.254.169.254/latest/meta-data"},
	}}}
	if err := r.Accept(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if err := r.sendBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "POST /internal/v1/ping" {
		t.Fatalf("requests: %v", seen)
	}
}

func TestRedirectToAnotherHostIsNeverFollowed(t *testing.T) {
	var hit atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Store(true) }))
	defer other.Close()
	for _, code := range []int{301, 302, 303, 307, 308} {
		redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/internal/v1/ping", code)
		}))
		r, _, _ := newRelay(t, &fakeDB{}, func(c *Config) { c.URL = redir.URL })
		_ = r.Accept(context.Background(), ping("a"))
		_ = r.sendBatch(context.Background())
		redir.Close()
		if r.Stats().Rejected != 1 {
			t.Fatalf("code %d: %+v", code, r.Stats())
		}
	}
	if hit.Load() {
		t.Fatal("a redirect target was dialed")
	}
}

func TestDBURLMustBeAPlainBaseURL(t *testing.T) {
	good := Config{BufferPings: 1, BufferBytes: 1, BatchPings: 1, Timeout: time.Second, BackoffMin: time.Second, BackoffMax: time.Second}
	for _, u := range []string{"file:///etc/passwd", "gopher://x", "javascript:alert(1)", "http://", "https:///path", "http://user:pw@host:1",
		"http://host:1/?x=1", "http://host:1/#frag", "http://ho st:1", "HTTP://host:1\n", ""} {
		c := good
		c.URL = u
		if _, err := New(c, quietLog(), Options{}); err == nil {
			t.Errorf("db-url %q accepted", u)
		}
	}
	for _, u := range []string{"http://127.0.0.1:8789", "https://db.example.test", "http://[::1]:8789/base"} {
		c := good
		c.URL = u
		if _, err := New(c, quietLog(), Options{}); err != nil {
			t.Errorf("db-url %q refused: %v", u, err)
		}
	}
}

func TestErrorsNeverContainTheKeyOrURLCredentials(t *testing.T) {
	c := Config{URL: "http://user:topsecretpw@host:1", BufferPings: 1, BufferBytes: 1, BatchPings: 1, Timeout: time.Second, BackoffMin: time.Second, BackoffMax: time.Second}
	_, err := New(c, quietLog(), Options{})
	if err == nil || strings.Contains(err.Error(), "topsecretpw") {
		t.Fatalf("%v", err)
	}
}

func TestBatchIDReuseIsANoOp(t *testing.T) {
	db := &fakeDB{}
	r, _, _ := newRelay(t, db, func(c *Config) { c.BufferPings = 100 })
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := r.Accept(ctx, ping("same")); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.sendBatch(ctx)
	if err := r.Accept(ctx, ping("same")); err != nil { // after it was sent, still a replay
		t.Fatal(err)
	}
	_ = r.sendBatch(ctx)
	if ids := db.ids(); len(ids) != 1 {
		t.Fatalf("the database saw %d posts of one batch id", len(ids))
	}
	if st := r.Stats(); st.Duplicates != 5 || st.Sent != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestBatchIDWindowIsBounded(t *testing.T) {
	r, _, _ := newRelay(t, &fakeDB{}, func(c *Config) { c.BufferPings = 10 })
	ctx := context.Background()
	for i := 0; i < seenWindow*3; i++ {
		_ = r.Accept(ctx, ping("id-"+itoa(i)))
	}
	r.mu.Lock()
	n := len(r.seen)
	r.mu.Unlock()
	if n > seenWindow {
		t.Fatalf("%d remembered ids, window %d", n, seenWindow)
	}
	// An id older than the window is accepted again (the database's own window is the second line).
	if err := r.Accept(ctx, ping("id-0")); err != nil || r.Stats().Duplicates != 0 {
		t.Fatalf("old id: %v %+v", err, r.Stats())
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestPoisonedPingsAreRepairedOrRefused(t *testing.T) {
	db := &fakeDB{}
	r, _, _ := newRelay(t, db, func(c *Config) { c.BufferPings = 100 })
	ctx := context.Background()
	contact := func(mut func(*domain.PingContact)) domain.Ping {
		c := domain.PingContact{Contact: domain.Contact{GUID: "Player-1-A", Name: "Alice", Level: 70}}
		mut(&c)
		return domain.Ping{RS: domain.DefaultRuleset(), BatchID: "p" + itoa(int(time.Now().UnixNano()%1e9)), Source: "s", Contacts: []domain.PingContact{c}}
	}
	bad := []func(*domain.PingContact){
		func(c *domain.PingContact) { c.D.DmgTotal = math.NaN() },
		func(c *domain.PingContact) { c.D.HPSum = math.Inf(1) },
		func(c *domain.PingContact) { c.D.Events = -1 },
		func(c *domain.PingContact) { c.D.KB = 1 << 40 },
		func(c *domain.PingContact) { c.D.Assists = math.MaxInt64 },
		func(c *domain.PingContact) { c.D.DmgTotal = 1e300 },
		func(c *domain.PingContact) { c.Level = 9000 },
		func(c *domain.PingContact) { c.X, c.MapID = 1e12, 5 },
	}
	for i, mut := range bad {
		p := contact(mut)
		p.BatchID = "bad-" + itoa(i)
		if err := r.Accept(ctx, p); err == nil {
			t.Errorf("poisoned ping %d was queued", i)
		}
	}
	if r.Stats().Pending != 0 {
		t.Fatalf("pending %d", r.Stats().Pending)
	}
	// Dirty text is repaired, not refused, and counted.
	p := contact(func(c *domain.PingContact) { c.Name = "Al\nice‮" + strings.Repeat("z", 300); c.Raw = "x\x00y" })
	p.BatchID = "dirty-1"
	if err := r.Accept(ctx, p); err != nil {
		t.Fatalf("dirty names refused: %v", err)
	}
	_ = r.sendBatch(ctx)
	got := db.got[len(db.got)-1]
	name := got.Contacts[0].Name
	if strings.ContainsAny(name, "\n‮") || len([]rune(name)) > domain.NameMaxRunes || r.Stats().Sanitized == 0 {
		t.Fatalf("name %q stats %+v", name, r.Stats())
	}
}
