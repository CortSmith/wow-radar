package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

const (
	ingestKey = "ingest-secret-key-0001"
	viewKey   = "view-secret-key-000001"
)

func noEnv(string) (string, bool) { return "", false }

// server runs a real host on a random port until the test ends.
type server struct {
	t        *testing.T
	base     string
	internal string // base URL of the internal listener; empty when it is not running
	stop     func()
}

func startServer(t *testing.T, args ...string) *server { return startServerWithout(t, nil, args...) }

// startServerWithout is startServer minus the named modules.
func startServerWithout(t *testing.T, omit []string, args ...string) *server {
	t.Helper()
	args = append([]string{"--listen", "127.0.0.1:0"}, args...)
	if !slices.Contains(omit, "writeapi") { // the flag exists only while the write API does
		args = append(args, "--internal-listen", "127.0.0.1:0")
	}
	args, lookup := secretsToEnv(args)
	host, err := buildHostWithout(args, lookup, io.Discard, omit...)
	if err != nil {
		t.Fatal(err)
	}
	return runHost(t, host)
}

func runHost(t *testing.T, host *core.Host) *server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Run(ctx) }()
	select {
	case <-host.Ready():
	case err := <-done:
		cancel()
		t.Fatalf("host stopped early: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("host never became ready")
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("host shutdown: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	s := &server{t: t, base: "http://" + host.Addr(), stop: stop}
	if addr := host.ClassAddr(core.ClassInternal); addr != "" {
		s.internal = "http://" + addr
	}
	return s
}

// do sends the request to the internal listener for /internal/ paths and to the public one otherwise.
func (s *server) do(method, path, body string, headers ...string) (int, []byte) {
	s.t.Helper()
	if strings.HasPrefix(path, "/internal/") && s.internal != "" {
		return s.doAt(s.internal, method, path, body, headers...)
	}
	return s.doAt(s.base, method, path, body, headers...)
}

// doPublic always uses the public listener.
func (s *server) doPublic(method, path, body string, headers ...string) (int, []byte) {
	s.t.Helper()
	return s.doAt(s.base, method, path, body, headers...)
}

func (s *server) doAt(base, method, path, body string, headers ...string) (int, []byte) {
	s.t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func (s *server) ping(batch string, contacts ...string) (domain.ApplyResult, int) {
	s.t.Helper()
	body := fmt.Sprintf(`{"rs":"pvp","batchId":%q,"source":"e2e","contacts":[%s],"charts":[{"mapId":1,"name":"Zone One","x0":0,"x1":100,"y0":0,"y1":100}],"deaths":[]}`,
		batch, strings.Join(contacts, ","))
	code, data := s.do("POST", "/internal/v1/ping", body)
	var res domain.ApplyResult
	_ = json.Unmarshal(data, &res)
	return res, code
}

func (s *server) sweep(query string) domain.SweepResult {
	s.t.Helper()
	code, data := s.do("GET", "/api/v1/sweep?"+query, "")
	if code != 200 {
		s.t.Fatalf("sweep %s: %d %s", query, code, data)
	}
	var res domain.SweepResult
	if err := json.Unmarshal(data, &res); err != nil {
		s.t.Fatal(err)
	}
	return res
}

func contactJSON(guid, name string, mapID int, lastSeen int64, events int) string {
	return fmt.Sprintf(`{"guid":%q,"name":%q,"raw":%q,"reaction":"hostile","level":60,"x":10,"y":20,"mapId":%d,"zone":"Zone","posTime":%d,"firstSeen":%d,"lastSeen":%d,"d":{"events":%d},"fixes":[{"t":%d,"mapId":%d,"x":10,"y":20,"kind":"exact"}]}`,
		guid, name, name, mapID, lastSeen, lastSeen-1000, lastSeen, events, lastSeen, mapID)
}

func names(cs []domain.Contact) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

func TestEndToEnd(t *testing.T) {
	tests := []struct {
		name string
		args func(t *testing.T) []string
	}{
		{"memstore", func(*testing.T) []string { return []string{"--store", "memstore"} }},
		{"sqlite", func(t *testing.T) []string {
			return []string{"--db-path", filepath.Join(t.TempDir(), "e2e.db")}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { endToEnd(t, startServer(t, tc.args(t)...)) })
	}
}

func endToEnd(t *testing.T, s *server) {
	now := time.Now().UnixMilli()
	res, code := s.ping("b1", contactJSON("Player-1-A", "Ann", 1, now-60000, 5), contactJSON("Player-1-B", "Bob", 2, now-50000, 3))
	if code != 200 || res.Received != 2 || res.Changed != 3 || res.Duplicate {
		t.Fatalf("first ping: %d %+v", code, res)
	}

	all := s.sweep("scope=all&since=0")
	if !all.Changed || names(all.Contacts) != "Bob,Ann" && names(all.Contacts) != "Ann,Bob" || len(all.Contacts) != 2 {
		t.Fatalf("all sweep: %+v", all)
	}
	zone := s.sweep("scope=zone&mapId=1&since=0")
	if names(zone.Contacts) != "Ann" {
		t.Fatalf("zone sweep: %s", names(zone.Contacts))
	}
	player := s.sweep("scope=player&guid=Player-1-B&since=0&trail=1")
	if names(player.Contacts) != "Bob" || len(player.Trail) != 1 {
		t.Fatalf("player sweep: %+v", player)
	}

	if dup, _ := s.ping("b1", contactJSON("Player-1-A", "Ann", 1, now-60000, 5)); !dup.Duplicate || dup.Changed != 0 {
		t.Fatalf("duplicate ping changed data: %+v", dup)
	}
	if again := s.sweep("scope=player&guid=Player-1-A&since=0"); again.Contacts[0].Events != 5 {
		t.Fatalf("duplicate was applied: events = %d", again.Contacts[0].Events)
	}

	cursor := all.Now - 5000
	idle := s.sweep(fmt.Sprintf("scope=all&since=%d", now))
	if idle.Changed || len(idle.Contacts) != 0 {
		t.Fatalf("idle delta: %+v", idle)
	}
	s.ping("b2", contactJSON("Player-1-A", "Ann", 1, now-1000, 4))
	delta := s.sweep(fmt.Sprintf("scope=all&since=%d", now-30000))
	if names(delta.Contacts) != "Ann" || delta.Contacts[0].Events != 9 {
		t.Fatalf("delta sweep (cursor %d): %s events %+v", cursor, names(delta.Contacts), delta.Contacts)
	}

	checkReadRoutes(t, s)
}

func checkReadRoutes(t *testing.T, s *server) {
	for path, want := range map[string]string{
		"/api/v1/contacts?q=ann":          `"total":1`,
		"/api/v1/charts":                  `"Zone One"`,
		"/api/v1/status":                  `"rulesets"`,
		"/api/v1/trail?guid=Player-1-A":   `"kind":"exact"`,
		"/api/v1/bounty":                  `[]`,
		"/healthz":                        `ok`,
		"/api/v1/sweep?scope=zone&rs=zzz": `invalid_request`,
	} {
		_, data := s.do("GET", path, "")
		if !strings.Contains(string(data), want) {
			t.Errorf("GET %s = %s, want it to contain %s", path, data, want)
		}
	}
}

func TestKeysProtectBothAPIs(t *testing.T) {
	s := startServer(t, "--store", "memstore", "--ingest-keys", ingestKey, "--view-keys", viewKey)
	ping := `{"rs":"pvp","batchId":"k1","contacts":[]}`
	tests := []struct {
		name, method, path, body string
		headers                  []string
		want                     int
	}{
		{"ping no key", "POST", "/internal/v1/ping", ping, nil, 401},
		{"ping view key", "POST", "/internal/v1/ping", ping, []string{"X-Ingest-Key", viewKey}, 401},
		{"ping ingest key", "POST", "/internal/v1/ping", ping, []string{"X-Ingest-Key", ingestKey}, 200},
		{"read no key", "GET", "/api/v1/status", "", nil, 401},
		{"read ingest key", "GET", "/api/v1/status", "", []string{"X-View-Key", ingestKey}, 401},
		{"read view key", "GET", "/api/v1/status", "", []string{"X-View-Key", viewKey}, 200},
		{"read bearer", "GET", "/api/v1/status", "", []string{"Authorization", "Bearer " + viewKey}, 200},
		{"healthz open", "GET", "/healthz", "", nil, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if code, body := s.do(tc.method, tc.path, tc.body, tc.headers...); code != tc.want {
				t.Fatalf("status %d, want %d: %s", code, tc.want, body)
			}
		})
	}
}

func TestHostWithoutReadAPI(t *testing.T) {
	s := startServerWithout(t, []string{"readapi", "static"}, "--store", "memstore")
	if code, _ := s.do("GET", "/healthz", ""); code != 200 {
		t.Fatalf("healthz = %d", code)
	}
	if res, code := s.ping("r1", contactJSON("Player-1-A", "Ann", 1, time.Now().UnixMilli(), 1)); code != 200 || res.Changed != 2 {
		t.Fatalf("ping = %d %+v", code, res)
	}
	if code, _ := s.do("GET", "/api/v1/status", ""); code != 404 {
		t.Fatalf("read route should be gone, got %d", code)
	}
}

func TestHostWithoutWriteAPI(t *testing.T) {
	s := startServerWithout(t, []string{"writeapi", "static"}, "--store", "memstore")
	if code, _ := s.do("GET", "/healthz", ""); code != 200 {
		t.Fatalf("healthz = %d", code)
	}
	if code, _ := s.do("GET", "/api/v1/status", ""); code != 200 {
		t.Fatalf("status = %d", code)
	}
	if code, _ := s.doPublic("POST", "/internal/v1/ping", "{}"); code != 404 {
		t.Fatalf("write route should be gone, got %d", code)
	}
}

func TestBuildHostErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"unknown store", []string{"--store", "oracle"}, nil, "unknown store"},
		{"store from env", nil, map[string]string{"RADAR_STORE": "bogus"}, "unknown store"},
		{"bad flag", []string{"--nope"}, nil, "flag provided"},
		{"short key", []string{"--store", "memstore"}, map[string]string{"RADAR_VIEW_KEYS": "tiny"}, ""},
		{"key by flag", []string{"--store", "memstore", "--view-keys", "x"}, nil, "RADAR_VIEW_KEYS"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(k string) (string, bool) { v, ok := tc.env[k]; return v, ok }
			host, err := buildHost(tc.args, lookup, io.Discard)
			if tc.name == "short key" {
				if err != nil {
					t.Fatal(err)
				}
				if err := host.Hooks.RunStart(context.Background()); err == nil {
					t.Fatal("short key accepted")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestStoreFlagForms(t *testing.T) {
	for _, args := range [][]string{{"--store=memstore"}, {"-store", "memstore"}} {
		host, err := buildHost(args, noEnv, io.Discard)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if got := host.Stores.Names(); len(got) != 1 || got[0] != storeMemstore {
			t.Fatalf("%v: stores %v", args, got)
		}
	}
	host, err := buildHost(nil, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := host.Stores.Names(); len(got) != 1 || got[0] != storeSQLite {
		t.Fatalf("default store = %v, want sqlite", got)
	}
}

func TestRunKeygen(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"keygen"}, noEnv, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	key := strings.TrimSpace(out.String())
	if len(key) != 43 || errOut.Len() != 0 {
		t.Fatalf("key %q stderr %q", key, errOut.String())
	}
}

func TestRunVersionAndUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"version"}, noEnv, &out, &errOut); code != 0 || !strings.Contains(out.String(), version) {
		t.Fatalf("version: %d %q", code, out.String())
	}
	errOut.Reset()
	if code := run(context.Background(), []string{"frobnicate"}, noEnv, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage") {
		t.Fatalf("unknown command: %d %q", code, errOut.String())
	}
}

func TestRunServeStopsOnContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"--store", "memstore", "--listen", "127.0.0.1:0"}, noEnv, io.Discard, io.Discard)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"--store", "nope"}, noEnv, io.Discard, &errOut); code != 1 || errOut.Len() == 0 {
		t.Fatalf("bad config: exit %d %q", code, errOut.String())
	}
}

func TestBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "live.db")
	s := startServer(t, "--db-path", dbPath)
	if _, code := s.ping("bk1", contactJSON("Player-1-A", "Ann", 1, time.Now().UnixMilli(), 2)); code != 200 {
		t.Fatalf("ping = %d", code)
	}
	target := filepath.Join(dir, "copy.db")
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"backup", "--db-path", dbPath, target}, noEnv, &out, &errOut); code != 0 {
		t.Fatalf("backup exit %d: %s", code, errOut.String())
	}
	s.stop()
	copied := startServer(t, "--db-path", target)
	code, data := copied.do("GET", "/api/v1/contacts?q=ann", "")
	if code != 200 || !strings.Contains(string(data), `"total":1`) {
		t.Fatalf("backup content: %d %s", code, data)
	}
}

func TestBackupErrors(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "exists.db")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"no target", []string{"backup", "--db-path", existing}, nil},
		{"missing db", []string{"backup", "--db-path", filepath.Join(dir, "none.db"), filepath.Join(dir, "o.db")}, nil},
		{"missing db via env", []string{"backup", filepath.Join(dir, "o.db")}, map[string]string{"RADAR_DB_PATH": filepath.Join(dir, "none.db")}},
		{"bad flag", []string{"backup", "--zzz"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(k string) (string, bool) { v, ok := tc.env[k]; return v, ok }
			var errOut bytes.Buffer
			if code := run(context.Background(), tc.args, lookup, io.Discard, &errOut); code == 0 || errOut.Len() == 0 {
				t.Fatalf("exit %d stderr %q", code, errOut.String())
			}
		})
	}
	// An existing target must be refused by the store and reported.
	dbPath := filepath.Join(dir, "real.db")
	startServer(t, "--db-path", dbPath).stop()
	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"backup", "--db-path", dbPath, existing}, noEnv, io.Discard, &errOut); code == 0 {
		t.Fatal("backup over an existing file succeeded")
	}
}

// TestInternalPingOnlyOnInternalListener proves the listener split: the write API is invisible on
// the public listener and the public routes are invisible on the internal one.
func TestInternalPingOnlyOnInternalListener(t *testing.T) {
	s := startServer(t, "--store", "memstore", "--ingest-keys", ingestKey, "--view-keys", viewKey)
	ping := `{"rs":"pvp","batchId":"split-1","contacts":[]}`
	key := []string{"X-Ingest-Key", ingestKey}
	if code, _ := s.doPublic("GET", "/internal/v1/ping", ""); code != 404 {
		t.Fatalf("public listener knows /internal/v1/ping: GET = %d", code)
	}
	// POST meets the page catch-all (GET /), so the mux answers 405 rather than 404; never a 2xx.
	if code, _ := s.doPublic("POST", "/internal/v1/ping", ping, key...); code != 404 && code != 405 {
		t.Fatalf("public listener served POST /internal/v1/ping: %d", code)
	}
	if code, _ := s.doAt(s.internal, "POST", "/internal/v1/ping", ping, key...); code != 200 {
		t.Fatalf("internal listener ping = %d", code)
	}
	for _, path := range []string{"/api/v1/status", "/"} {
		if code, _ := s.doAt(s.internal, "GET", path, "", "X-View-Key", viewKey); code != 404 {
			t.Fatalf("internal listener served %s: %d", path, code)
		}
	}
	if code, _ := s.doAt(s.internal, "GET", "/healthz", ""); code != 200 {
		t.Fatalf("internal healthz = %d", code)
	}
	if code, _ := s.doPublic("GET", "/healthz", ""); code != 200 {
		t.Fatalf("public healthz = %d", code)
	}
}

func TestDefaultListenAddresses(t *testing.T) {
	host, err := buildHost(nil, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := host.Config.String(core.SettingListen); got != ":8787" {
		t.Fatalf("public listen default = %q", got)
	}
	if got := host.Config.String("internal-listen"); got != "127.0.0.1:8789" {
		t.Fatalf("internal listen default = %q", got)
	}
}

func TestLimitsModuleIsRemovable(t *testing.T) {
	s := startServerWithout(t, []string{"limits"}, "--store", "memstore")
	if code, _ := s.doPublic("GET", "/healthz", ""); code != 200 {
		t.Fatalf("healthz without limits = %d", code)
	}
}

func TestLimitsFlagsAreAccepted(t *testing.T) {
	s := startServer(t, "--store", "memstore", "--limits-max-conns", "64", "--limits-max-inflight", "32")
	if code, _ := s.doPublic("GET", "/healthz", ""); code != 200 {
		t.Fatalf("healthz = %d", code)
	}
}

func TestGuardUnban(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bans.json")
	env := func(k string) (string, bool) {
		if k == "RADAR_ABUSE_BAN_FILE" {
			return file, true
		}
		return "", false
	}
	do := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := run(context.Background(), append([]string{"guard"}, args...), env, &out, &errOut)
		return code, out.String(), errOut.String()
	}
	if code, out, _ := do("ban", "203.0.113.7", "1h"); code != 0 || !strings.Contains(out, "banned 203.0.113.7") {
		t.Fatalf("ban: %d %q", code, out)
	}
	if code, out, _ := do("list"); code != 0 || !strings.Contains(out, "203.0.113.7") {
		t.Fatalf("list: %d %q", code, out)
	}
	if code, out, _ := do("unban", "203.0.113.7"); code != 0 || !strings.Contains(out, "unbanned") {
		t.Fatalf("unban: %d %q", code, out)
	}
	if code, out, _ := do("list"); code != 0 || !strings.Contains(out, "no active bans") {
		t.Fatalf("list after unban: %d %q", code, out)
	}
	if code, _, errOut := do("unban"); code != 1 || !strings.Contains(errOut, "usage") {
		t.Fatalf("bad args: %d %q", code, errOut)
	}
}

func TestGuardFallsBackToDatabaseDirectory(t *testing.T) {
	db := filepath.Join(t.TempDir(), "radar.db")
	env := func(k string) (string, bool) {
		if k == "RADAR_DB_PATH" {
			return db, true
		}
		return "", false
	}
	var out bytes.Buffer
	if code := run(context.Background(), []string{"guard", "ban", "203.0.113.7", "1h"}, env, &out, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(db), "abuse-bans.json")); err != nil {
		t.Fatal(err)
	}
}

func TestAbuseModuleIsRemovable(t *testing.T) {
	s := startServerWithout(t, []string{"abuse"}, "--store", "memstore")
	if code, _ := s.doPublic("GET", "/healthz", ""); code != 200 {
		t.Fatalf("healthz without abuse = %d", code)
	}
}

func TestRefusesOpenAuthOnPublicListener(t *testing.T) {
	host, err := buildHost([]string{"--store", "memstore", "--listen", "0.0.0.0:0", "--internal-listen", "127.0.0.1:0"}, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "REQUIRE_KEYS") {
		t.Fatalf("want refusal, got %v", err)
	}
}

func TestKeyGuessingLocksOutTheClient(t *testing.T) {
	banFile := filepath.Join(t.TempDir(), "bans.json")
	s := startServer(t, "--store", "memstore", "--ingest-keys", ingestKey, "--view-keys", viewKey,
		"--abuse-ban-loopback=true", "--abuse-ban-file", banFile)
	// Failed auth looks the same for the view and the write API.
	c1, b1 := s.doPublic("GET", "/api/v1/status", "", "X-View-Key", "wrong-key-0000000000")
	c2, b2 := s.do("POST", "/internal/v1/ping", `{}`, "X-Ingest-Key", "wrong-key-0000000000")
	if c1 != 401 || c2 != 401 || string(b1) != string(b2) {
		t.Fatalf("failures differ: %d %q vs %d %q", c1, b1, c2, b2)
	}
	for i := 0; i < 3; i++ {
		s.doPublic("GET", "/api/v1/status", "", "X-View-Key", "wrong-key-0000000000")
	}
	if code, _ := s.doPublic("GET", "/api/v1/status", "", "X-View-Key", viewKey); code != http.StatusTooManyRequests {
		t.Fatalf("locked-out client got %d", code)
	}
}

// secretsToEnv moves the key options out of args into an environment lookup: secrets are never flags,
// so the tests hand them over the way production does.
func secretsToEnv(args []string) ([]string, func(string) (string, bool)) {
	env := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ingest-keys":
			env["RADAR_INGEST_KEYS"] = args[i+1]
			i++
		case "--view-keys":
			env["RADAR_VIEW_KEYS"] = args[i+1]
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}
