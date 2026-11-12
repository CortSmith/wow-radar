package e2e

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

const ingestKey = "e2e-ingest-key-123456"

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func noEnv(string) (string, bool) { return "", false }

// testDB is an in-process database: the memory store behind a tiny handler that decodes a Ping and
// applies it (the real write API lives in another module and is not needed here).
type testDB struct {
	srv    *httptest.Server
	store  core.Store
	mu     sync.Mutex
	pings  int
	failOn int // answer 503 to this many requests first
}

func newTestDB(t *testing.T) *testDB {
	t.Helper()
	host := core.NewHost(core.WithLogger(quiet()))
	if err := host.Register(DB()...); err != nil {
		t.Fatal(err)
	}
	store, err := host.Stores.Default()
	if err != nil {
		t.Fatal(err)
	}
	db := &testDB{store: store}
	db.srv = httptest.NewServer(http.HandlerFunc(db.handle))
	t.Cleanup(db.srv.Close)
	return db
}

func (d *testDB) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/internal/v1/ping" || r.Header.Get("X-Ingest-Key") != ingestKey {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	d.mu.Lock()
	d.pings++
	fail := d.failOn > 0
	if fail {
		d.failOn--
	}
	d.mu.Unlock()
	if fail {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	var ping domain.Ping
	if err := json.NewDecoder(r.Body).Decode(&ping); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := d.store.ApplyPing(r.Context(), ping)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	core.WriteJSON(w, http.StatusOK, res)
}

func (d *testDB) sweep(t *testing.T, rs domain.Ruleset) domain.SweepResult {
	t.Helper()
	res, err := d.store.Sweep(context.Background(), domain.Sector{RS: rs, Scope: domain.ScopeAll, ActiveWithinMs: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

type ingest struct {
	host *core.Host
	url  string
}

// startIngest runs an ingest host on a free port with fast flushes; it is stopped at test end.
func startIngest(t *testing.T, mods []core.Module, extra ...string) *ingest {
	t.Helper()
	host := core.NewHost(core.WithLogger(quiet()))
	if err := host.Register(mods...); err != nil {
		t.Fatal(err)
	}
	// The fixtures are old logs: accept them (the bounds themselves are tested in hostile_test.go).
	args := append([]string{"--listen=127.0.0.1:0", "--logparse-allow-replay=true"}, extra...)
	for _, m := range mods {
		if m.Name() == "contactx" || m.Name() == "bountyx" {
			args = append(args, "--"+m.Name()+"-flush=100ms")
		}
	}
	if err := host.Config.Load(args, relayKeyEnv(extra)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Run(ctx) }()
	select {
	case <-host.Ready():
	case err := <-done:
		t.Fatalf("host stopped early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("host not ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("ingest did not stop")
		}
	})
	return &ingest{host: host, url: "http://" + host.Addr()}
}

func (in *ingest) stream(t *testing.T, query, body string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, in.url+"/v1/stream"+query, strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sum map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&sum); err != nil || resp.StatusCode != 200 {
		t.Fatalf("status %d err %v", resp.StatusCode, err)
	}
	return sum
}

func sessionLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../../internal/mod/logparse/testdata/session.log")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("timed out waiting for %s", what)
	}
}

func relayArgs(db *testDB) []string {
	return []string{"--db-url=" + db.srv.URL, "--relay-backoff-min=20ms", "--relay-backoff-max=100ms"}
}

func TestStreamedLogReachesTheDatabase(t *testing.T) {
	db := newTestDB(t)
	in := startIngest(t, Ingest(), relayArgs(db)...)
	sum := in.stream(t, "?source=laptop", sessionLog(t))
	if sum["lines"].(float64) < 10 || sum["events"].(float64) < 8 || sum["rejected"].(float64) < 2 || sum["contacts"].(float64) < 3 {
		t.Fatalf("summary %v", sum)
	}
	waitFor(t, "contacts in db", func() bool { return len(db.sweep(t, domain.RulesetPvP).Contacts) >= 3 })
	var names []string
	for _, c := range db.sweep(t, domain.RulesetPvP).Contacts {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "Ann Bee") || !strings.Contains(got, "Cat") {
		t.Fatalf("contacts %v", names)
	}
	waitFor(t, "bounty credit", func() bool {
		rows, _ := db.store.Bounty(context.Background(), domain.RulesetPvP, 10)
		return len(rows) > 0
	})
}

func TestRulesetOverrideChoosesThePartition(t *testing.T) {
	db := newTestDB(t)
	in := startIngest(t, Ingest(), relayArgs(db)...)
	in.stream(t, "?source=rp&rs=rp", sessionLog(t))
	waitFor(t, "contacts in rp", func() bool { return len(db.sweep(t, domain.RulesetRP).Contacts) >= 3 })
	if n := len(db.sweep(t, domain.RulesetPvP).Contacts); n != 0 {
		t.Fatalf("pvp partition got %d contacts", n)
	}
}

func TestDatabaseOutageIsBridgedByTheRelay(t *testing.T) {
	db := newTestDB(t)
	db.failOn = 3
	in := startIngest(t, Ingest(), relayArgs(db)...)
	in.stream(t, "?source=laptop", sessionLog(t))
	waitFor(t, "contacts after outage", func() bool { return len(db.sweep(t, domain.RulesetPvP).Contacts) >= 3 })
}

func TestWithoutBountyxStreamStillWorks(t *testing.T) {
	db := newTestDB(t)
	in := startIngest(t, Ingest("bountyx"), relayArgs(db)...)
	sum := in.stream(t, "?source=laptop", sessionLog(t))
	if sum["events"].(float64) < 8 {
		t.Fatalf("summary %v", sum)
	}
	waitFor(t, "contacts", func() bool { return len(db.sweep(t, domain.RulesetPvP).Contacts) >= 3 })
	time.Sleep(300 * time.Millisecond)
	if rows, _ := db.store.Bounty(context.Background(), domain.RulesetPvP, 10); len(rows) != 0 {
		t.Fatalf("bounty rows without bountyx: %v", rows)
	}
}

type recordSink struct {
	mu    sync.Mutex
	pings []domain.Ping
}

func (*recordSink) Name() string { return "record" }

func (r *recordSink) Accept(_ context.Context, p domain.Ping) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pings = append(r.pings, p)
	return nil
}

func (r *recordSink) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pings)
}

func TestWithoutRelayPingsOnlyReachSinks(t *testing.T) {
	sink := &recordSink{}
	in := startIngest(t, Ingest("relay"))
	in.host.Sinks.Add(sink)
	sum := in.stream(t, "?source=laptop", sessionLog(t))
	if sum["lines"].(float64) < 10 {
		t.Fatalf("summary %v", sum)
	}
	waitFor(t, "ping in sink", func() bool { return sink.count() > 0 })
}

func TestSourcesEndpointIsReachable(t *testing.T) {
	db := newTestDB(t)
	in := startIngest(t, Ingest(), relayArgs(db)...)
	resp, err := http.Get(in.url + "/v1/sources")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// relayKeyEnv supplies the relay's key through the environment (a secret is never a flag) when the
// test points the relay at a database.
func relayKeyEnv(extra []string) func(string) (string, bool) {
	for _, a := range extra {
		if strings.HasPrefix(a, "--db-url=") {
			return func(k string) (string, bool) { return ingestKey, k == "RADAR_DB_KEY" }
		}
	}
	return noEnv
}
