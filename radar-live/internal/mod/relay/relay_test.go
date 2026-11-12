package relay

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"radarlive/internal/domain"
)

type fakeDB struct {
	mu     sync.Mutex
	status []int // statuses to answer with, consumed in order; then 200
	got    []domain.Ping
	keys   []string
	paths  []string
}

func (f *fakeDB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
	f.keys = append(f.keys, r.Header.Get("X-Ingest-Key"))
	status := http.StatusOK
	if len(f.status) > 0 {
		status, f.status = f.status[0], f.status[1:]
	}
	var p domain.Ping
	_ = json.Unmarshal(body, &p)
	f.got = append(f.got, p)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"received":1}`))
}

// ids lists the batch ids of every request received (including failed ones).
func (f *fakeDB) ids() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, p := range f.got {
		ids = append(ids, p.BatchID)
	}
	return ids
}

type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *sleeps) list() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.d...)
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newRelay(t *testing.T, db *fakeDB, mut func(*Config)) (*Relay, *httptest.Server, *sleeps) {
	t.Helper()
	srv := httptest.NewServer(db)
	t.Cleanup(srv.Close)
	cfg := Config{URL: srv.URL, Key: "secret-key-123456", BufferPings: 3, BufferBytes: 1 << 20, BatchPings: 10,
		Timeout: 2 * time.Second, BackoffMin: time.Second, BackoffMax: 4 * time.Second}
	if mut != nil {
		mut(&cfg)
	}
	sl := &sleeps{}
	r, err := New(cfg, quietLog(), Options{Sleep: sl.sleep, Jitter: func(time.Duration) time.Duration { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	return r, srv, sl
}

func ping(id string) domain.Ping {
	return domain.Ping{RS: domain.DefaultRuleset(), BatchID: id, Source: "s"}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("condition not reached in time")
	}
}

func TestSendsPingWithKeyToFixedPath(t *testing.T) {
	db := &fakeDB{}
	r, _, _ := newRelay(t, db, func(c *Config) { c.URL += "/" })
	if err := r.Accept(context.Background(), ping("cx-a-1")); err != nil {
		t.Fatal(err)
	}
	if err := r.sendBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.paths[0] != "/internal/v1/ping" || db.keys[0] != "secret-key-123456" || db.got[0].BatchID != "cx-a-1" {
		t.Fatalf("paths=%v keys=%v got=%v", db.paths, db.keys, db.got)
	}
	if st := r.Stats(); st.Sent != 1 || st.Pending != 0 || st.Bytes != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestRetryKeepsBatchIDAndBacksOff(t *testing.T) {
	db := &fakeDB{status: []int{500, 503, 429, 500, 500}}
	r, _, sl := newRelay(t, db, nil)
	ctx := context.Background()
	_ = r.Accept(ctx, ping("cx-a-1"))
	for i := 0; i < 5; i++ {
		if err := r.sendBatch(ctx); err == nil {
			t.Fatalf("attempt %d should fail", i)
		}
		_ = r.backoff(ctx)
	}
	if err := r.sendBatch(ctx); err != nil {
		t.Fatal(err)
	}
	ids := db.ids()
	if len(ids) != 6 {
		t.Fatalf("attempts %d", len(ids))
	}
	for _, id := range ids {
		if id != "cx-a-1" {
			t.Fatalf("batch id changed: %s", id)
		}
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}
	got := sl.list()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sleeps %v want %v", got, want)
		}
	}
}

func TestBackoffResetsAfterSuccess(t *testing.T) {
	db := &fakeDB{status: []int{500}}
	r, _, sl := newRelay(t, db, nil)
	ctx := context.Background()
	_ = r.Accept(ctx, ping("a"))
	_ = r.sendBatch(ctx)
	_ = r.backoff(ctx)
	if err := r.sendBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if r.attempt != 0 || len(sl.list()) != 1 {
		t.Fatalf("attempt=%d sleeps=%v", r.attempt, sl.list())
	}
}

func TestJitterIsAdded(t *testing.T) {
	r, _, sl := newRelay(t, &fakeDB{}, nil)
	r.jitter = func(limit time.Duration) time.Duration { return limit / 2 }
	r.attempt = 0
	_ = r.backoff(context.Background())
	if got := sl.list()[0]; got != time.Second+time.Second/4 {
		t.Fatalf("got %v", got)
	}
}

func TestDropsOldestWhenFull(t *testing.T) {
	db := &fakeDB{}
	r, _, _ := newRelay(t, db, nil)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		_ = r.Accept(context.Background(), ping(id))
	}
	if st := r.Stats(); st.Dropped != 2 || st.Pending != 3 {
		t.Fatalf("stats %+v", st)
	}
	if err := r.sendBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := db.ids(); len(got) != 3 || got[0] != "c" || got[2] != "e" {
		t.Fatalf("sent %v", got)
	}
}

func TestDropsOldestByBytes(t *testing.T) {
	r, _, _ := newRelay(t, &fakeDB{}, func(c *Config) { c.BufferPings = 100; c.BufferBytes = 300 })
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		_ = r.Accept(context.Background(), ping(id))
	}
	st := r.Stats()
	if st.Dropped == 0 || st.Bytes > 300 || st.Pending == 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestPermanentRejectionIsDropped(t *testing.T) {
	db := &fakeDB{status: []int{400}}
	r, _, _ := newRelay(t, db, nil)
	_ = r.Accept(context.Background(), ping("bad"))
	_ = r.Accept(context.Background(), ping("good"))
	if err := r.sendBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := r.Stats(); st.Rejected != 1 || st.Sent != 1 || st.Pending != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestUnreachableDBKeepsPing(t *testing.T) {
	r, srv, _ := newRelay(t, &fakeDB{}, nil)
	srv.Close()
	_ = r.Accept(context.Background(), ping("a"))
	if err := r.sendBatch(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if r.Stats().Pending != 1 {
		t.Fatal("ping must stay queued")
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("dialed other host") }))
	defer other.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	r, _, _ := newRelay(t, &fakeDB{}, func(c *Config) { c.URL = redir.URL })
	_ = r.Accept(context.Background(), ping("a"))
	if err := r.sendBatch(context.Background()); err != nil {
		t.Fatal(err) // a 307 is a permanent rejection, not a transient error
	}
	if r.Stats().Rejected != 1 {
		t.Fatalf("stats %+v", r.Stats())
	}
}

func TestAcceptRejectsInvalidAndFillsBatchID(t *testing.T) {
	db := &fakeDB{}
	r, _, _ := newRelay(t, db, nil)
	if err := r.Accept(context.Background(), domain.Ping{RS: "nope", BatchID: "x"}); err == nil {
		t.Fatal("expected invalid error")
	}
	if err := r.Accept(context.Background(), ping("")); err != nil {
		t.Fatal(err)
	}
	_ = r.sendBatch(context.Background())
	if ids := db.ids(); len(ids) != 1 || ids[0] == "" {
		t.Fatalf("ids %v", ids)
	}
}

func TestBatchLimit(t *testing.T) {
	db := &fakeDB{}
	r, _, _ := newRelay(t, db, func(c *Config) { c.BatchPings = 2; c.BufferPings = 10 })
	for _, id := range []string{"a", "b", "c"} {
		_ = r.Accept(context.Background(), ping(id))
	}
	_ = r.sendBatch(context.Background())
	if len(db.ids()) != 2 || r.Stats().Pending != 1 {
		t.Fatalf("sent %v", db.ids())
	}
}

func TestRunDeliversAndDrains(t *testing.T) {
	db := &fakeDB{}
	r, _, _ := newRelay(t, db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	_ = r.Accept(ctx, ping("a"))
	waitFor(t, func() bool { return len(db.ids()) == 1 })
	cancel()
	<-done
	_ = r.Accept(context.Background(), ping("b"))
	if err := r.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(db.ids()) != 2 || r.Stats().Pending != 0 {
		t.Fatalf("ids %v", db.ids())
	}
}

func TestDrainGivesUpWhenContextEnds(t *testing.T) {
	db := &fakeDB{status: []int{500, 500, 500, 500}}
	r, _, _ := newRelay(t, db, nil)
	_ = r.Accept(context.Background(), ping("a"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Drain(ctx); err == nil {
		t.Fatal("expected error")
	}
	if r.Stats().Pending != 1 {
		t.Fatal("unsent ping must remain counted")
	}
}

func TestNewValidatesConfig(t *testing.T) {
	good := Config{URL: "http://x", BufferPings: 1, BufferBytes: 1, BatchPings: 1, Timeout: time.Second, BackoffMin: time.Second, BackoffMax: time.Second}
	bad := []func(*Config){
		func(c *Config) { c.URL = "" },
		func(c *Config) { c.URL = "ftp://x" },
		func(c *Config) { c.BufferPings = 0 },
		func(c *Config) { c.BatchPings = 0 },
		func(c *Config) { c.BackoffMin = 0 },
		func(c *Config) { c.Timeout = 0 },
	}
	for i, mut := range bad {
		c := good
		mut(&c)
		if _, err := New(c, quietLog(), Options{}); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
	if _, err := New(good, nil, Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestRunRetriesWithBackoffUntilDBRecovers(t *testing.T) {
	db := &fakeDB{status: []int{500, 500}}
	r, _, sl := newRelay(t, db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	_ = r.Accept(ctx, ping("a"))
	waitFor(t, func() bool { return r.Stats().Sent == 1 })
	if got := sl.list(); len(got) != 2 || got[0] != time.Second || got[1] != 2*time.Second {
		t.Fatalf("sleeps %v", got)
	}
}

func TestDefaultSleepHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); err == nil {
		t.Fatal("expected cancel")
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if d := randomJitter(0); d != 0 {
		t.Fatal(d)
	}
	if d := randomJitter(time.Second); d < 0 || d >= time.Second {
		t.Fatal(d)
	}
}
