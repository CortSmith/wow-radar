package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"radarlive/internal/domain"
)

const fakeToken = "test-token-abcdef0123456789"

// fakeArmory is a Battle.net stand-in: token endpoint, character documents and guild rosters, in the
// (PROVISIONAL) shape the client expects.
type fakeArmory struct {
	status      int // non-zero: every API request answers this (the token endpoint still works)
	tokenStatus int // non-zero: the token endpoint answers this
	badName     string
	rosters     map[string][]string // "realmslug/guildslug" -> member names (realm = the guild's)
	onDetail    func(n int)
	details     atomic.Int32
	tokens      atomic.Int32
	rosterHits  atomic.Int32
	mu          sync.Mutex
	badAuth     int
	lastBearer  string
}

func (f *fakeArmory) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/oauth/token" {
		f.tokens.Add(1)
		id, secret, ok := r.BasicAuth()
		if f.tokenStatus != 0 {
			w.WriteHeader(f.tokenStatus)
			return
		}
		if r.Method != http.MethodPost || !ok || id != fakeClientID || secret != fakeAPIKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","expires_in":86399}`, fakeToken)
		return
	}
	f.mu.Lock()
	f.lastBearer = r.Header.Get("Authorization")
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		f.mu.Lock()
		f.badAuth++
		f.mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if r.URL.Query().Get("namespace") == "" || r.URL.Query().Get("locale") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case len(parts) == 6 && parts[0] == "data" && parts[2] == "guild" && parts[5] == "roster":
		f.rosterHits.Add(1)
		members, ok := f.rosters[parts[3]+"/"+parts[4]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var ms []string
		for _, m := range members {
			ms = append(ms, fmt.Sprintf(`{"character":{"name":%q,"realm":{"slug":%q}},"rank":1}`, m, parts[3]))
		}
		_, _ = fmt.Fprintf(w, `{"members":[%s]}`, strings.Join(ms, ","))
	case len(parts) >= 5 && parts[0] == "profile":
		name := parts[4]
		if len(parts) == 6 {
			switch parts[5] {
			case "equipment":
				_, _ = w.Write([]byte(`{"equipped_items":[{"item":{"id":1},"slot":{"type":"HEAD"},"quality":{"type":"EPIC"},"name":"Synthetic Helm","level":{"value":50}}]}`))
			case "statistics":
				_, _ = w.Write([]byte(`{"health":1000,"power":500,"power_type":{"name":"Mana"}}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
			return
		}
		n := int(f.details.Add(1))
		if f.onDetail != nil {
			f.onDetail(n)
		}
		if f.badName != "" && name == strings.ToLower(f.badName) {
			_, _ = w.Write([]byte("not json"))
			return
		}
		_, _ = w.Write([]byte(`{"level":60,"character_class":{"name":"Mage"},"race":{"name":"Human"},"faction":{"type":"ALLIANCE"},"guild":{"name":"Testers"}}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func fakeName(i int) string {
	s := ""
	for n := i; ; n /= 26 {
		s = string(rune('a'+n%26)) + s
		if n < 26 {
			break
		}
	}
	return "Zz" + s
}

// fakeDB is a radar-db internal listener stub, seeds included.
type fakeDB struct {
	mu        sync.Mutex
	batches   []domain.ArmoryBatch
	runs      []domain.ArmoryRun
	ids       map[string]bool
	status    int // non-zero: batch route answers this
	seedsCode int // non-zero: seeds route answers this
	seeds     []domain.ArmorySeed
	seedPage  int
	seedHits  int
	badKey    atomic.Bool
}

func (d *fakeDB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Ingest-Key") != fakeDBKey || d.badKey.Load() {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case r.URL.Path == "/internal/v1/armory/seeds" && r.Method == http.MethodGet:
		d.seedHits++
		if d.seedsCode != 0 {
			w.WriteHeader(d.seedsCode)
			return
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		size := limit
		if d.seedPage > 0 {
			size = min(size, d.seedPage)
		}
		end := min(start+size, len(d.seeds))
		page := domain.ArmorySeedPage{Items: append([]domain.ArmorySeed{}, d.seeds[min(start, end):end]...)}
		if end < len(d.seeds) {
			page.Next = strconv.Itoa(end)
		}
		_ = json.NewEncoder(w).Encode(page)
	case r.URL.Path == "/internal/v1/armory/batch" && r.Method == http.MethodPost:
		if d.status != 0 {
			w.WriteHeader(d.status)
			return
		}
		var b domain.ArmoryBatch
		_ = json.NewDecoder(r.Body).Decode(&b)
		if d.ids == nil {
			d.ids = map[string]bool{}
		}
		res := domain.ArmoryApplyResult{Duplicate: d.ids[b.BatchID]}
		if !res.Duplicate {
			d.ids[b.BatchID] = true
			d.batches = append(d.batches, b)
			res.Stored = len(b.Characters)
		}
		_ = json.NewEncoder(w).Encode(res)
	case r.URL.Path == "/internal/v1/armory/run" && r.Method == http.MethodPost:
		var run domain.ArmoryRun
		_ = json.NewDecoder(r.Body).Decode(&run)
		d.runs = append(d.runs, run)
		_ = json.NewEncoder(w).Encode(run)
	case r.URL.Path == "/internal/v1/armory/run" && r.Method == http.MethodGet:
		if len(d.runs) == 0 {
			http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(d.runs[len(d.runs)-1])
	default:
		http.NotFound(w, r)
	}
}

func (d *fakeDB) storedNames() map[string]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]int{}
	for _, b := range d.batches {
		for _, c := range b.Characters {
			out[c.Name]++
		}
	}
	return out
}

type rig struct {
	armory *fakeArmory
	db     *fakeDB
	env    map[string]string
	hooks  testHooks
}

// newRig starts the fakes with total character seeds (realm "Testrealm").
func newRig(t *testing.T, total int) *rig {
	t.Helper()
	a := &fakeArmory{}
	d := &fakeDB{}
	for i := 0; i < total; i++ {
		d.seeds = append(d.seeds, domain.ArmorySeed{Kind: domain.ArmorySeedCharacter, Realm: "Testrealm", Name: fakeName(i)})
	}
	as := httptest.NewServer(a)
	ds := httptest.NewServer(d)
	t.Cleanup(as.Close)
	t.Cleanup(ds.Close)
	return &rig{armory: a, db: d,
		env: map[string]string{
			"WOW_ARMORY_API_KEY":        fakeAPIKey,
			"WOW_ARMORY_CLIENT_ID":      fakeClientID,
			"RADAR_ARMORY_DB_KEY":       fakeDBKey,
			"RADAR_ARMORY_BASE_URL":     as.URL,
			"RADAR_ARMORY_TOKEN_URL":    as.URL + "/oauth/token",
			"RADAR_ARMORY_DB_URL":       ds.URL,
			"RADAR_ARMORY_RATE":         "1000",
			"RADAR_ARMORY_LOCATION":     "UTC",
			"RADAR_ARMORY_MAX_RUNTIME":  "1m",
			"RADAR_ARMORY_CONCURRENCY":  "2",
			"RADAR_ARMORY_MAX_REQUESTS": "0",
		},
		hooks: testHooks{allowHTTP: true, sleep: func(context.Context, time.Duration) error { return nil }}}
}

func (r *rig) run(ctx context.Context, args ...string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = run(ctx, args, env(r.env), &o, &e, r.hooks)
	return code, o.String(), e.String()
}

func TestRunOK(t *testing.T) {
	r := newRig(t, 23)
	// a guild seed whose roster has 3 new members and 1 character that is already a seed
	r.db.seeds = append(r.db.seeds, domain.ArmorySeed{Kind: domain.ArmorySeedGuild, Realm: "Testrealm", Guild: "Fake Guild"})
	r.armory.rosters = map[string][]string{"testrealm/fake-guild": {fakeName(2), "Rosterone", "Rostertwo", "Rosterthree"}}
	code, out, errOut := r.run(context.Background())
	if code != 0 {
		t.Fatalf("exit %d out=%q err=%q", code, out, errOut)
	}
	names := r.db.storedNames()
	if len(names) != 26 || names["Rosterone"] != 1 || names[fakeName(2)] != 1 {
		t.Fatalf("stored %d characters, want 26: %v", len(names), names)
	}
	if !strings.Contains(out, "status=ok") || !strings.Contains(out, "stored=26") || strings.Count(out, "\n") != 1 {
		t.Fatalf("summary line: %q", out)
	}
	if got := func() string { r.armory.mu.Lock(); defer r.armory.mu.Unlock(); return r.armory.lastBearer }(); got != "Bearer "+fakeToken {
		t.Fatal("armory did not receive the bearer token")
	}
	if r.armory.tokens.Load() != 1 || r.armory.badAuth != 0 {
		t.Fatalf("token requests %d, bad auth %d: the token must be fetched once and reused", r.armory.tokens.Load(), r.armory.badAuth)
	}
	last := r.db.runs[len(r.db.runs)-1]
	if last.Status != "ok" || last.FinishedAt == 0 {
		t.Fatalf("final run: %+v", last)
	}
	assertNoSecrets(t, out, errOut)
	// Second run: the batch ids differ (new run), the process still ends 0.
	if code, out, _ := r.run(context.Background()); code != 0 {
		t.Fatalf("second run exit %d %q", code, out)
	}
	if len(r.db.storedNames()) != 26 {
		t.Fatal("second run changed the set of characters")
	}
}

func TestRunDefaultRealmAndSkippedSeeds(t *testing.T) {
	r := newRig(t, 0)
	r.db.seeds = []domain.ArmorySeed{
		{Kind: domain.ArmorySeedCharacter, Name: "Nodefault"},
		{Kind: domain.ArmorySeedCharacter, Realm: "Testrealm", Name: "Alice"},
	}
	code, out, _ := r.run(context.Background())
	if code != 0 || !strings.Contains(out, "skipped=1") || len(r.db.storedNames()) != 1 {
		t.Fatalf("exit %d %q", code, out)
	}
	r2 := newRig(t, 0)
	r2.db.seeds = r.db.seeds
	r2.env["RADAR_ARMORY_DEFAULT_REALM"] = "area-52"
	code, out, _ = r2.run(context.Background())
	if code != 0 || strings.Contains(out, "skipped=1") || len(r2.db.storedNames()) != 2 {
		t.Fatalf("with a default realm: exit %d %q", code, out)
	}
	r2.env["RADAR_ARMORY_DEFAULT_REALM"] = "Area 52/../x"
	if code, _, errOut := r2.run(context.Background()); code != 2 || errOut == "" {
		t.Fatalf("a bad default realm must be a config error: %d %q", code, errOut)
	}
}

func TestRunPartialOnPoisonCharacter(t *testing.T) {
	r := newRig(t, 10)
	r.armory.badName = fakeName(3)
	code, out, _ := r.run(context.Background())
	if code != 1 || !strings.Contains(out, "status=partial") || !strings.Contains(out, "failed=1") {
		t.Fatalf("exit %d %q", code, out)
	}
	if len(r.db.storedNames()) != 9 {
		t.Fatalf("stored %d, want 9", len(r.db.storedNames()))
	}
}

func TestRunPartialWhenDBRejectsBatches(t *testing.T) {
	r := newRig(t, 6)
	r.db.status = http.StatusBadRequest
	code, out, _ := r.run(context.Background())
	if code != 1 || !strings.Contains(out, "status=partial") {
		t.Fatalf("exit %d %q", code, out)
	}
}

func TestRunPartialWhenDBIsDegraded(t *testing.T) {
	r := newRig(t, 6)
	r.db.status = http.StatusServiceUnavailable
	code, _, _ := r.run(context.Background())
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
}

// radar-db without the seeds route (404) or failing it ends the run partial, with a note and no crash.
func TestRunPartialWhenSeedsUnavailable(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		r := newRig(t, 6)
		r.db.seedsCode = code
		exit, out, errOut := r.run(context.Background())
		if exit != 1 || !strings.Contains(out, "status=partial") || !strings.Contains(errOut, "seeds") {
			t.Fatalf("%d: exit %d out=%q err=%q", code, exit, out, errOut)
		}
		if r.armory.details.Load() != 0 {
			t.Fatal("characters were fetched without seeds")
		}
	}
}

func TestRunFatalOnArmoryAuth(t *testing.T) {
	r := newRig(t, 6)
	r.armory.status = http.StatusUnauthorized // even a refreshed token is refused
	code, out, errOut := r.run(context.Background())
	if code != 2 || !strings.Contains(out, "status=failed") {
		t.Fatalf("exit %d out=%q", code, out)
	}
	if n := r.armory.tokens.Load(); n < 2 || n > 4 {
		t.Fatalf("%d token requests: one initial and one refresh (plus a straggler) expected", n)
	}
	assertNoSecrets(t, out, errOut)
}

func TestRunFatalOnRejectedClientCredentials(t *testing.T) {
	for name, mut := range map[string]func(r *rig){
		"token endpoint 401": func(r *rig) { r.armory.tokenStatus = http.StatusUnauthorized },
		"wrong secret":       func(r *rig) { r.env["WOW_ARMORY_API_KEY"] = "test-wrong-secret-9999999999" },
		"wrong client id":    func(r *rig) { r.env["WOW_ARMORY_CLIENT_ID"] = "test-wrong-client-9999999999" },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, 6)
			mut(r)
			code, out, errOut := r.run(context.Background())
			if code != 2 || !strings.Contains(out, "status=failed") {
				t.Fatalf("exit %d out=%q err=%q", code, out, errOut)
			}
			assertNoSecrets(t, out, errOut)
			for _, s := range []string{r.env["WOW_ARMORY_API_KEY"], r.env["WOW_ARMORY_CLIENT_ID"]} {
				if strings.Contains(out, s) || strings.Contains(errOut, s) {
					t.Fatalf("output leaks a credential: %q %q", out, errOut)
				}
			}
			if len(r.db.storedNames()) != 0 {
				t.Fatal("something was stored")
			}
		})
	}
}

func TestRunFatalOnDBAuth(t *testing.T) {
	r := newRig(t, 6)
	r.db.badKey.Store(true)
	code, out, errOut := r.run(context.Background())
	if code != 2 {
		t.Fatalf("exit %d out=%q err=%q", code, out, errOut)
	}
	assertNoSecrets(t, out, errOut)
}

func TestRunInterruptedBySignal(t *testing.T) {
	r := newRig(t, 40)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.armory.onDetail = func(n int) {
		if n == 6 {
			cancel() // what SIGTERM does
		}
	}
	code, out, _ := r.run(ctx)
	if code != 1 || !strings.Contains(out, "status=interrupted") {
		t.Fatalf("exit %d %q", code, out)
	}
	if len(r.db.storedNames()) >= 40 {
		t.Fatal("run was not interrupted")
	}
	// The unfinished run resumes the same (UTC) day.
	r.armory.onDetail = nil
	code, out, _ = r.run(context.Background())
	if code != 0 || len(r.db.storedNames()) != 40 {
		t.Fatalf("resume: exit %d stored %d out=%q", code, len(r.db.storedNames()), out)
	}
}

func TestConfigErrorsExit2WithoutSecrets(t *testing.T) {
	for name, drop := range map[string]string{
		"api key": "WOW_ARMORY_API_KEY", "client id": "WOW_ARMORY_CLIENT_ID", "db key": "RADAR_ARMORY_DB_KEY",
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, 1)
			delete(r.env, drop)
			code, out, errOut := r.run(context.Background())
			if code != 2 || errOut == "" || out != "" {
				t.Fatalf("exit %d out=%q err=%q", code, out, errOut)
			}
			assertNoSecrets(t, out, errOut)
		})
	}
	// values the client refuses are config errors too, and the message does not repeat them
	for name, mut := range map[string]func(map[string]string){
		"userinfo base": func(e map[string]string) { e["RADAR_ARMORY_BASE_URL"] = "https://u:p@armory.example.test" },
		"bad region":    func(e map[string]string) { e["RADAR_ARMORY_REGION"] = "U/S" },
		"bad namespace": func(e map[string]string) { e["RADAR_ARMORY_NAMESPACE"] = "a&b=c" },
		"bad path":      func(e map[string]string) { e["RADAR_ARMORY_SUMMARY_PATH"] = "/no/placeholders" },
		"http token":    func(e map[string]string) { e["RADAR_ARMORY_TOKEN_URL"] = "http://oauth.example.test/token" },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, 1)
			r.hooks = testHooks{} // production: no http seam
			mut(r.env)
			code, out, errOut := r.run(context.Background())
			if code != 2 || errOut == "" || out != "" {
				t.Fatalf("exit %d out=%q err=%q", code, out, errOut)
			}
			assertNoSecrets(t, out, errOut)
			for _, leak := range []string{"u:p", "a&b=c", "/no/placeholders"} {
				if strings.Contains(errOut, leak) {
					t.Fatalf("the error repeats a setting value: %q", errOut)
				}
			}
		})
	}
}

func TestSecretFlagRefusedWithoutEcho(t *testing.T) {
	for _, arg := range []string{"--api-key=" + fakeAPIKey, "--client-secret=" + fakeAPIKey, "--client-id=" + fakeClientID} {
		r := newRig(t, 1)
		code, out, errOut := r.run(context.Background(), arg)
		if code != 2 {
			t.Fatalf("%s: exit %d", arg, code)
		}
		assertNoSecrets(t, out, errOut)
	}
}

func TestHTTPArmoryRefusedInProduction(t *testing.T) {
	r := newRig(t, 1)
	r.hooks = testHooks{}
	code, out, errOut := r.run(context.Background())
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	assertNoSecrets(t, out, errOut)
}

func TestVersion(t *testing.T) {
	var o, e bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, env(nil), &o, &e, testHooks{}); code != 0 || !strings.Contains(o.String(), "radar-armory") {
		t.Fatalf("%d %q", code, o.String())
	}
}

func TestHelpIsConfigError(t *testing.T) {
	var o, e bytes.Buffer
	if code := run(context.Background(), []string{"-h"}, env(goodEnv()), &o, &e, testHooks{}); code != 2 || !strings.Contains(e.String(), "usage") {
		t.Fatalf("%d %q", code, e.String())
	}
}

func assertNoSecrets(t *testing.T, outs ...string) {
	t.Helper()
	for _, s := range outs {
		for _, secret := range []string{fakeAPIKey, fakeClientID, fakeDBKey, fakeToken} {
			if strings.Contains(s, secret) {
				t.Fatalf("output leaks a secret: %q", s)
			}
		}
	}
}

func TestCapturedSummaryHasCountsOnly(t *testing.T) {
	r := newRig(t, 3)
	_, out, _ := r.run(context.Background())
	if strings.Contains(out, "Zz") || strings.Contains(out, "Testrealm") {
		t.Fatalf("character data in summary: %q", out)
	}
}
