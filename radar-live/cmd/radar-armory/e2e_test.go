package main

// End-to-end and hostile tests of the whole armory chain (stage 15, task R8):
//
//	fake Battle.net (httptest: token endpoint, character documents, guild rosters) -> radar-armory run() ->
//	seed gateway (stubs GET /internal/v1/armory/seeds, which another stage adds to radar-db, and forwards
//	everything else) -> REAL radar-db binary (real sqlstore on a temp dir, internal listener) -> public read API.
//
// They live in this package (and not in test/e2e) because run() is unexported; the radar-db side is the
// real built binary, like test/e2e does, so no module package is imported here (architecture rule).
// Everything is synthetic: names are generated, keys are random per test.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	_ "modernc.org/sqlite"

	"radarlive/internal/domain"
)

const e2eRealm = "Testrealm"

var (
	e2eBinDir  string
	e2eBinOnce sync.Once
	e2eBinErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if e2eBinDir != "" {
		_ = os.RemoveAll(e2eBinDir)
	}
	os.Exit(code)
}

func e2eKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func e2eRadarDBBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds radar-db; skipped with -short")
	}
	e2eBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "radar-armory-e2e-")
		if err != nil {
			e2eBinErr = err
			return
		}
		e2eBinDir = dir
		name := "radar-db"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", filepath.Join(dir, name), "radarlive/cmd/radar-db")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			e2eBinErr = fmt.Errorf("go build radar-db: %v\n%s", err, out)
		}
	})
	if e2eBinErr != nil {
		t.Fatal(e2eBinErr)
	}
	name := "radar-db"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(e2eBinDir, name)
}

type e2eBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *e2eBuf) Write(p []byte) (int, error) { b.mu.Lock(); defer b.mu.Unlock(); return b.b.Write(p) }
func (b *e2eBuf) String() string              { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func e2eFreeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := l.Addr().String()
	_ = l.Close()
	return a
}

// e2eStack is a real radar-db process (sqlite store) with its keys and data dir.
type e2eStack struct {
	t         *testing.T
	bin       string
	dbPath    string
	pub, intl string
	ingestKey string
	viewKey   string
	cmd       *exec.Cmd
	out       *e2eBuf
}

func newE2EStack(t *testing.T) *e2eStack {
	t.Helper()
	bin := e2eRadarDBBinary(t)
	keygen := func() string {
		out, err := exec.Command(bin, "keygen").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	s := &e2eStack{t: t, bin: bin, dbPath: filepath.Join(t.TempDir(), "radar-live.db"),
		pub: e2eFreeAddr(t), intl: e2eFreeAddr(t), ingestKey: keygen(), viewKey: keygen(), out: &e2eBuf{}}
	s.start()
	t.Cleanup(s.stop)
	return s
}

func (s *e2eStack) start() {
	s.t.Helper()
	cmd := exec.Command(s.bin, "--listen", s.pub, "--internal-listen", s.intl, "--db-path", s.dbPath)
	cmd.Stdout, cmd.Stderr = s.out, s.out
	cmd.Env = append(os.Environ(), "RADAR_INGEST_KEYS="+s.ingestKey, "RADAR_VIEW_KEYS="+s.viewKey)
	if err := cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	s.cmd = cmd
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + s.pub + "/healthz"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatalf("radar-db did not become healthy:\n%s", s.out.String())
}

// stop kills the process (the hard way: no graceful flush, the worst case for the data).
func (s *e2eStack) stop() {
	if s.cmd == nil {
		return
	}
	_ = s.cmd.Process.Kill()
	_ = s.cmd.Wait()
	s.cmd = nil
	if s.t.Failed() {
		s.t.Logf("--- radar-db output ---\n%s", s.out.String())
	}
}

func (s *e2eStack) get(path string, into any) int {
	s.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+s.pub+path, nil)
	req.Header.Set("X-View-Key", s.viewKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if into != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(data, into); err != nil {
			s.t.Fatalf("%s: %v: %.200s", path, err, data)
		}
	}
	return resp.StatusCode
}

// readAll fetches the given names through the public /characters route (groups of 50) and returns
// them by name. Names that are unknown are simply absent.
func (s *e2eStack) readAll(names []string) map[string]domain.ArmoryCharacter {
	s.t.Helper()
	out := map[string]domain.ArmoryCharacter{}
	for i := 0; i < len(names); i += 50 {
		group := names[i:min(i+50, len(names))]
		var res struct {
			Characters []domain.ArmoryCharacter `json:"characters"`
		}
		q := url.Values{"realm": {e2eRealm}, "names": {strings.Join(group, ",")}}
		if code := s.get("/api/v1/armory/characters?"+q.Encode(), &res); code != http.StatusOK {
			s.t.Fatalf("characters: HTTP %d", code)
		}
		for _, c := range res.Characters {
			out[c.Name] = c
		}
	}
	return out
}

// rows opens the (stopped) database and returns the integrity verdict, schema version and row counts.
func (s *e2eStack) inspect() (version int, tables map[string]int, names [][2]string) {
	s.t.Helper()
	s.stop()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(s.dbPath))
	if err != nil {
		s.t.Fatal(err)
	}
	defer db.Close()
	var verdict string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&verdict); err != nil || verdict != "ok" {
		s.t.Fatalf("integrity_check: %q %v", verdict, err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		s.t.Fatal(err)
	}
	tables = map[string]int{}
	for _, tb := range []string{"armory_characters", "armory_snapshots", "armory_batches", "armory_runs"} {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + tb).Scan(&n); err != nil {
			s.t.Fatalf("table %s: %v", tb, err)
		}
		tables[tb] = n
	}
	rs, err := db.Query("SELECT realm, name FROM armory_characters")
	if err != nil {
		s.t.Fatal(err)
	}
	defer rs.Close()
	for rs.Next() {
		var r, n string
		if err := rs.Scan(&r, &n); err != nil {
			s.t.Fatal(err)
		}
		names = append(names, [2]string{r, n})
	}
	return version, tables, names
}

// assertNoKeys fails when any of the secrets appears in a text or in the database files.
func (s *e2eStack) assertNoKeys(secrets []string, texts ...string) {
	s.t.Helper()
	all := append([]string{s.out.String()}, texts...)
	for _, f := range []string{s.dbPath, s.dbPath + "-wal"} {
		if data, err := os.ReadFile(f); err == nil {
			all = append(all, string(data))
		}
	}
	for _, text := range all {
		for _, k := range secrets {
			if k != "" && strings.Contains(text, k) {
				s.t.Fatalf("a secret leaked into output, log or database (len %d)", len(text))
			}
		}
	}
}

// ---- fake Battle.net armory (PROVISIONAL shapes, like the client) ----

type e2eRef struct{ Realm, Name string }

// e2eGuild is the guild every roster test uses: seed guild "Testers" on realm Testrealm.
const e2eGuild = "Testers"

type e2eArmory struct {
	clientID, secret string
	token            string
	pageSize         int
	entries          []e2eRef // the character seeds, in order (served by the seed gateway)
	guilds           []domain.ArmorySeed
	rosterNames      []string                                                            // members of testrealm/testers
	special          map[string]func(w http.ResponseWriter, r *http.Request)             // by LOWERCASE character name, summary path
	subSpecial       map[string]func(w http.ResponseWriter, r *http.Request, sub string) // sub-resource paths of a special character (default 404)
	storm            atomic.Bool                                                         // every API request answers 429
	onDetail         func(n int)
	guildOverride    func(guild string, w http.ResponseWriter, r *http.Request) bool // hostile rosters (by lowercase guild slug)

	mu       sync.Mutex
	seedHits map[string]int // seed page requests by cursor (gateway)
	detHits  map[string]int // summary requests by lowercase name
	uris     []string       // API request URIs (token endpoint excluded)
	badAuth  int
	total    atomic.Int64 // API requests (token endpoint excluded)
	details  atomic.Int64 // character summary requests
	rosters  atomic.Int64 // roster requests
	tokens   atomic.Int64
}

func newE2EArmory(key string, good int) *e2eArmory {
	a := &e2eArmory{clientID: "e2e-client-" + key[:16], secret: key, token: "e2e-token-" + key[8:40], pageSize: 50,
		seedHits: map[string]int{}, detHits: map[string]int{}, special: map[string]func(http.ResponseWriter, *http.Request){},
		subSpecial: map[string]func(http.ResponseWriter, *http.Request, string){}}
	for i := 0; i < good; i++ {
		a.entries = append(a.entries, e2eRef{e2eRealm, e2eName(i)})
	}
	return a
}

// e2eName is letters only ("Syn" + base 26), which the read API accepts.
func e2eName(i int) string {
	s := ""
	for n := i; ; n /= 26 {
		s = string(rune('a'+n%26)) + s
		if n < 26 {
			break
		}
	}
	return "Syn" + s
}

// insert places an entry into the seed list at roughly position pos.
func (a *e2eArmory) insert(pos int, ref e2eRef) {
	pos = min(pos, len(a.entries))
	a.entries = append(a.entries[:pos], append([]e2eRef{ref}, a.entries[pos:]...)...)
}

// addRoster makes seed guild Testers return n members that are not among the character seeds.
func (a *e2eArmory) addRoster(n int) {
	a.guilds = append(a.guilds, domain.ArmorySeed{Kind: domain.ArmorySeedGuild, Realm: e2eRealm, Guild: e2eGuild})
	for i := 0; i < n; i++ {
		a.rosterNames = append(a.rosterNames, e2eName(5000+i))
	}
}

func (a *e2eArmory) goodNames() []string {
	var out []string
	for _, e := range a.entries {
		if _, odd := a.special[strings.ToLower(e.Name)]; !odd && e.Realm == e2eRealm && strings.HasPrefix(e.Name, "Syn") {
			out = append(out, e.Name)
		}
	}
	return append(out, a.rosterNames...)
}

// seedItems is the full seed feed: characters first, then guilds (the order radar-db promises).
func (a *e2eArmory) seedItems() []domain.ArmorySeed {
	var out []domain.ArmorySeed
	for _, e := range a.entries {
		out = append(out, domain.ArmorySeed{Kind: domain.ArmorySeedCharacter, Realm: e.Realm, Name: e.Name})
	}
	return append(out, a.guilds...)
}

func e2eHash(name string) int {
	h := 0
	for _, r := range strings.ToLower(name) {
		h = h*31 + int(r)
	}
	if h < 0 {
		h = -h
	}
	return h
}

func e2eGoodSummary(name string) string {
	h := e2eHash(name)
	return fmt.Sprintf(`{"level":%d,"character_class":{"name":"Mage"},"race":{"name":"Human"},"faction":{"type":"ALLIANCE","name":"Alliance"},`+
		`"guild":{"name":"Testers"},"last_login_timestamp":1700000000000}`, 1+h%80)
}

// e2eSubBody is the sub-resource document of a normal character.
func e2eSubBody(sub, name string) (string, bool) {
	h := e2eHash(name)
	switch sub {
	case "equipment":
		return fmt.Sprintf(`{"equipped_items":[{"item":{"id":%d},"slot":{"type":"HEAD"},"quality":{"type":"EPIC"},"name":"Synthetic Helm","level":{"value":%d}}]}`, h%100000, 1+h%200), true
	case "specializations":
		return `{"specialization_groups":[{"is_active":true,"specializations":[{"specialization":{"name":"Frost"},"talents":[{"id":1,"rank":3}]}]}]}`, true
	case "statistics":
		return fmt.Sprintf(`{"health":%d,"power":%d,"power_type":{"name":"Mana"}}`, 1000+h%9000, 500+h%5000), true
	case "pvp-summary":
		return fmt.Sprintf(`{"honorable_kills":%d,"rating":%d}`, h%1000, h%3000), true
	}
	return "", false
}

func (a *e2eArmory) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/oauth/token" {
		a.tokens.Add(1)
		id, sec, ok := r.BasicAuth()
		if r.Method != http.MethodPost || !ok || id != a.clientID || sec != a.secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","expires_in":86399}`, a.token)
		return
	}
	a.total.Add(1)
	a.mu.Lock()
	a.uris = append(a.uris, r.RequestURI)
	a.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+a.token {
		a.mu.Lock()
		a.badAuth++
		a.mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if a.storm.Load() {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.EscapedPath(), "/"), "/")
	if len(parts) == 6 && parts[0] == "data" && parts[2] == "guild" && parts[5] == "roster" {
		a.rosters.Add(1)
		if a.guildOverride != nil && a.guildOverride(parts[4], w, r) {
			return
		}
		if parts[3] != "testrealm" || parts[4] != "testers" || len(a.rosterNames) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var ms []string
		for _, m := range a.rosterNames {
			ms = append(ms, fmt.Sprintf(`{"character":{"name":%q,"realm":{"slug":"testrealm"}},"rank":1}`, m))
		}
		_, _ = fmt.Fprintf(w, `{"members":[%s]}`, strings.Join(ms, ","))
		return
	}
	if len(parts) < 5 || parts[0] != "profile" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	name, _ := url.PathUnescape(parts[4])
	sub := ""
	if len(parts) == 6 {
		sub = parts[5]
	}
	h, odd := a.special[name]
	if sub == "" {
		a.mu.Lock()
		a.detHits[name]++
		a.mu.Unlock()
		n := int(a.details.Add(1))
		if a.onDetail != nil {
			a.onDetail(n)
		}
		if odd {
			h(w, r)
			return
		}
		_, _ = w.Write([]byte(e2eGoodSummary(name)))
		return
	}
	if odd {
		if sh, ok := a.subSpecial[name]; ok {
			sh(w, r, sub)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if body, ok := e2eSubBody(sub, name); ok {
		_, _ = w.Write([]byte(body))
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (a *e2eArmory) snapshotHits() (seeds, det map[string]int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	seeds, det = map[string]int{}, map[string]int{}
	for k, v := range a.seedHits {
		seeds[k] = v
	}
	for k, v := range a.detHits {
		det[k] = v
	}
	return
}

// e2eGateway is what radar-armory talks to as "radar-db" in the stress tests: it serves the seed route itself
// from the fake armory's seed list (so tests can shape the feed freely) and forwards every other request, byte
// for byte, to the REAL radar-db internal listener. TestE2ERealSeedsRoute and TestE2ERealFirstNightFlow use the
// real seed route instead; RADAR_ARMORY_E2E_REAL_SEEDS=1 forwards it in every test.
type e2eGateway struct {
	a         *e2eArmory
	ingestKey string
	proxy     *httputil.ReverseProxy
	real      bool
}

func newE2EGateway(t *testing.T, a *e2eArmory, stack *e2eStack) *httptest.Server {
	target, _ := url.Parse("http://" + stack.intl)
	g := &e2eGateway{a: a, ingestKey: stack.ingestKey, proxy: httputil.NewSingleHostReverseProxy(target), real: os.Getenv("RADAR_ARMORY_E2E_REAL_SEEDS") == "1"}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return srv
}

func (g *e2eGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/internal/v1/armory/seeds" || g.real {
		g.proxy.ServeHTTP(w, r)
		return
	}
	if r.Header.Get("X-Ingest-Key") != g.ingestKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	start, _ := strconv.Atoi(cursor)
	g.a.mu.Lock()
	g.a.seedHits[cursor]++
	g.a.mu.Unlock()
	items := g.a.seedItems()
	end := min(start+min(limit, g.a.pageSize), len(items))
	page := domain.ArmorySeedPage{Items: append([]domain.ArmorySeed{}, items[min(start, end):end]...)}
	if end < len(items) {
		page.Next = strconv.Itoa(end)
	}
	_ = json.NewEncoder(w).Encode(page)
}

// ---- run helper ----

// e2eHostRT records every host a request was sent to and applies a short header timeout (stalls).
type e2eHostRT struct {
	rt    http.RoundTripper
	mu    sync.Mutex
	hosts map[string]int
}

func newE2EHostRT() *e2eHostRT {
	return &e2eHostRT{rt: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 400 * time.Millisecond}, hosts: map[string]int{}}
}

func (h *e2eHostRT) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.Lock()
	h.hosts[r.URL.Host]++
	h.mu.Unlock()
	return h.rt.RoundTrip(r)
}

func (h *e2eHostRT) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for k := range h.hosts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type e2eRig struct {
	armory    *e2eArmory
	armorySrv *httptest.Server
	gateway   *httptest.Server
	stack     *e2eStack
	env       map[string]string
	hooks     testHooks
	rt        *e2eHostRT
}

func newE2ERig(t *testing.T, a *e2eArmory, stack *e2eStack) *e2eRig {
	t.Helper()
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	gw := newE2EGateway(t, a, stack)
	rt := newE2EHostRT()
	return &e2eRig{armory: a, armorySrv: srv, gateway: gw, stack: stack, rt: rt,
		env: map[string]string{
			"WOW_ARMORY_API_KEY":        a.secret,
			"WOW_ARMORY_CLIENT_ID":      a.clientID,
			"RADAR_ARMORY_DB_KEY":       stack.ingestKey,
			"RADAR_ARMORY_BASE_URL":     srv.URL,
			"RADAR_ARMORY_TOKEN_URL":    srv.URL + "/oauth/token",
			"RADAR_ARMORY_DB_URL":       gw.URL,
			"RADAR_ARMORY_RATE":         "1000",
			"RADAR_ARMORY_LOCATION":     "UTC",
			"RADAR_ARMORY_MAX_RUNTIME":  "2m",
			"RADAR_ARMORY_CONCURRENCY":  "2",
			"RADAR_ARMORY_MAX_REQUESTS": "0",
		},
		hooks: testHooks{allowHTTP: true, transport: rt, sleep: func(context.Context, time.Duration) error { return nil }}}
}

func (r *e2eRig) secrets() []string {
	return []string{r.armory.secret, r.armory.clientID, r.armory.token, r.stack.ingestKey}
}

func (r *e2eRig) run(ctx context.Context) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = run(ctx, nil, env(r.env), &o, &e, r.hooks)
	return code, o.String(), e.String()
}

var summaryRE = regexp.MustCompile(`(\w+)=(\S+)`)

func e2eSummary(out string) map[string]string {
	m := map[string]string{}
	for _, g := range summaryRE.FindAllStringSubmatch(out, -1) {
		m[g[1]] = g[2]
	}
	return m
}

func e2eAtoi(s string) int { n, _ := strconv.Atoi(s); return n }

// ---- tests ----

func e2eStatusHandler(code int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func e2eRawHandler(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
}

// 330 synthetic characters over 7 pages with hidden, not-found and poison entries.
func TestE2EArmoryRunIdempotentAndPersistent(t *testing.T) {
	stack := newE2EStack(t)
	a := newE2EArmory(e2eKey(t), 330)
	a.addRoster(12) // guild seed Testers expands to 12 more characters; a second guild without a roster is skipped silently
	a.guilds = append(a.guilds, domain.ArmorySeed{Kind: domain.ArmorySeedGuild, Realm: e2eRealm, Guild: "Norosterguild"})
	for i := 0; i < 8; i++ {
		hn, gn := "Hidden"+string(rune('a'+i)), "Gone"+string(rune('a'+i))
		a.special[strings.ToLower(hn)], a.special[strings.ToLower(gn)] = e2eStatusHandler(http.StatusForbidden), e2eStatusHandler(http.StatusNotFound)
		a.insert(20+i*37, e2eRef{e2eRealm, hn})
		a.insert(40+i*41, e2eRef{e2eRealm, gn})
	}
	for i, p := range []string{"Poisona", "Poisonb"} {
		a.special[strings.ToLower(p)] = e2eRawHandler("this is not json {")
		a.insert(100+i*90, e2eRef{e2eRealm, p})
	}
	rig := newE2ERig(t, a, stack)
	good := a.goodNames()
	if len(good) != 342 {
		t.Fatalf("setup: %d good", len(good))
	}

	code, out, errOut := rig.run(context.Background())
	sum := e2eSummary(out)
	if code != 1 || sum["status"] != "partial" || sum["failed"] != "2" || sum["enumerated"] != "360" {
		t.Fatalf("first run: exit %d out=%q err=%q", code, out, errOut)
	}
	if n := a.rosters.Load(); n != 2 || a.tokens.Load() != 1 {
		t.Fatalf("roster requests %d (want 2), token requests %d (want 1)", n, a.tokens.Load())
	}
	got := stack.readAll(good)
	if len(got) != 342 {
		t.Fatalf("read API returns %d of 342 characters", len(got))
	}
	for _, name := range good[:5] {
		var one domain.ArmoryCharacter
		if c := stack.get("/api/v1/armory/character?realm="+e2eRealm+"&name="+name, &one); c != 200 || one.Name != name || one.Class != "Mage" || one.Status != "ok" || one.Level < 1 || one.PowerType != "Mana" || one.Health < 1000 {
			t.Fatalf("character %s: %d %+v", name, c, one)
		}
	}
	if c := got[good[3]]; !bytes.Contains(c.Gear, []byte("Synthetic Helm")) || !bytes.Contains(c.PvP, []byte("rating")) {
		t.Fatalf("blobs not stored: %+v", c)
	}
	odd := stack.readAll([]string{"Hiddena", "Gonea", "Poisona"})
	if odd["Hiddena"].Status != "hidden" || odd["Gonea"].Status != "notfound" {
		t.Fatalf("hidden/notfound: %+v", odd)
	}
	if _, ok := odd["Poisona"]; ok {
		t.Fatal("a poison character was stored")
	}
	if c := stack.get("/api/v1/armory/character?realm="+e2eRealm+"&name=Neverheardof", nil); c != http.StatusNotFound {
		t.Fatalf("unknown character: %d", c)
	}
	if c := stack.get("/api/v1/armory/character?realm="+e2eRealm+"&name=Syna", nil); c != 200 {
		t.Fatalf("known character: %d", c)
	}
	// The read API needs a view key.
	resp, err := http.Get("http://" + stack.pub + "/api/v1/armory/character?realm=" + e2eRealm + "&name=" + good[0])
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no key: %d", resp.StatusCode)
	}
	stack.assertNoKeys(rig.secrets(), out, errOut)

	// Second run: same data. Everything except the fetch time is unchanged.
	code2, out2, errOut2 := rig.run(context.Background())
	if code2 != 1 || e2eSummary(out2)["failed"] != "2" {
		t.Fatalf("second run: exit %d %q %q", code2, out2, errOut2)
	}
	strip := func(m map[string]domain.ArmoryCharacter) map[string]string {
		o := map[string]string{}
		for k, c := range m {
			c.FetchedAt = 0
			b, _ := json.Marshal(c)
			o[k] = string(b)
		}
		return o
	}
	got2 := stack.readAll(good)
	if len(got2) != 342 {
		t.Fatalf("after the second run %d characters", len(got2))
	}
	before, after := strip(got), strip(got2)
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("second run changed %s:\n%s\n%s", k, v, after[k])
		}
	}
	stack.assertNoKeys(rig.secrets(), out2, errOut2)

	// A hard kill and restart keeps the data.
	stack.stop()
	stack.start()
	reopened := stack.readAll(good)
	if len(reopened) != 342 {
		t.Fatalf("after restart %d characters", len(reopened))
	}
	if b1, b2 := strip(got2), strip(reopened); fmt.Sprint(b1) != fmt.Sprint(b2) {
		t.Fatal("restart changed the data")
	}

	// The file itself: schema v2, intact, one row per character (358 ok/hidden/notfound), one snapshot each per day.
	version, tables, names := stack.inspect()
	if version != 2 {
		t.Fatalf("user_version %d, want 2", version)
	}
	if tables["armory_characters"] != 358 {
		t.Fatalf("rows %v", tables)
	}
	if tables["armory_snapshots"] != 342 || tables["armory_runs"] < 2 || len(names) != 358 {
		t.Fatalf("tables %v names %d", tables, len(names))
	}
	stack.assertNoKeys(rig.secrets())
}

// A run killed halfway resumes and finishes without refetching the finished pages.
func TestE2EArmoryKilledRunResumes(t *testing.T) {
	stack := newE2EStack(t)
	a := newE2EArmory(e2eKey(t), 330)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.onDetail = func(n int) {
		if n == 140 {
			cancel() // what SIGTERM does mid-run
		}
	}
	rig := newE2ERig(t, a, stack)
	good := a.goodNames()

	code, out, _ := rig.run(ctx)
	if code != 1 || e2eSummary(out)["status"] != "interrupted" {
		t.Fatalf("killed run: exit %d %q", code, out)
	}
	part := stack.readAll(good)
	if len(part) == 0 || len(part) >= 330 {
		t.Fatalf("after the kill %d characters stored", len(part))
	}
	a.onDetail = nil
	code, out, errOut := rig.run(context.Background())
	if code != 0 || e2eSummary(out)["status"] != "ok" {
		t.Fatalf("resumed run: exit %d out=%q err=%q", code, out, errOut)
	}
	if got := stack.readAll(good); len(got) != 330 {
		t.Fatalf("after the resume %d of 330", len(got))
	}
	list, det := a.snapshotHits()
	replayedPages := 0
	for cur, n := range list {
		if n > 2 {
			t.Errorf("page %q fetched %d times", cur, n)
		}
		if n == 2 {
			replayedPages++
		}
	}
	if replayedPages > 1 {
		t.Errorf("%d pages were fetched twice, a crash may replay at most one: %v", replayedPages, list)
	}
	twice := 0
	for name, n := range det {
		if n > 2 {
			t.Errorf("%s fetched %d times", name, n)
		}
		if n == 2 {
			twice++
		}
	}
	if twice > a.pageSize+rig.concurrencyAllowance() {
		t.Errorf("%d characters were fetched twice, more than one replayed page", twice)
	}
	stack.assertNoKeys(rig.secrets(), out, errOut)
}

func (r *e2eRig) concurrencyAllowance() int { return 2 }

// A 429 storm backs off (circuit breaker) and a request budget is never exceeded.
func TestE2EArmoryStormAndBudget(t *testing.T) {
	stack := newE2EStack(t)
	t.Run("storm", func(t *testing.T) {
		a := newE2EArmory(e2eKey(t), 330)
		a.storm.Store(true)
		rig := newE2ERig(t, a, stack)
		rig.env["RADAR_ARMORY_MAX_REQUESTS"] = "40"
		code, out, errOut := rig.run(context.Background())
		if code != 1 {
			t.Fatalf("exit %d %q %q", code, out, errOut)
		}
		if n := a.total.Load(); n > 40 || n < 1 {
			t.Fatalf("%d requests during the storm (budget 40)", n)
		}
		if n := a.total.Load(); n > 12 {
			t.Fatalf("%d requests: no back-off (the breaker should stop a storm in a handful)", n)
		}
		stack.assertNoKeys(rig.secrets(), out, errOut)
	})
	t.Run("budget", func(t *testing.T) {
		a := newE2EArmory(e2eKey(t), 330)
		rig := newE2ERig(t, a, stack)
		rig.env["RADAR_ARMORY_MAX_REQUESTS"] = "25"
		code, out, errOut := rig.run(context.Background())
		s := e2eSummary(out)
		if code != 1 || s["status"] != "partial" {
			t.Fatalf("exit %d %q %q", code, out, errOut)
		}
		// The budget counts logical fetches (a character or a roster), not the 5 HTTP requests a character needs.
		if n := a.details.Load() + a.rosters.Load(); n > 25 {
			t.Fatalf("%d armory fetches, budget 25", n)
		}
		if e2eAtoi(s["stored"]) == 0 || e2eAtoi(s["stored"]) >= 330 {
			t.Fatalf("stored %s", s["stored"])
		}
	})
}

// A missing or empty key exits 2 and neither key shows up anywhere.
func TestE2EArmoryMissingKeyExits2(t *testing.T) {
	stack := newE2EStack(t)
	a := newE2EArmory(e2eKey(t), 20)
	rig := newE2ERig(t, a, stack)
	secrets := rig.secrets()
	for name, mutate := range map[string]func(){
		"missing":     func() { delete(rig.env, "WOW_ARMORY_API_KEY") },
		"empty":       func() { rig.env["WOW_ARMORY_API_KEY"] = "" },
		"wrong":       func() { rig.env["WOW_ARMORY_API_KEY"] = e2eKey(t) },
		"missing db":  func() { rig.env["WOW_ARMORY_API_KEY"] = a.secret; delete(rig.env, "RADAR_ARMORY_DB_KEY") },
		"wrong db":    func() { rig.env["RADAR_ARMORY_DB_KEY"] = e2eKey(t) },
		"secret flag": func() { rig.env["RADAR_ARMORY_DB_KEY"] = stack.ingestKey },
		"missing id":  func() { rig.env["WOW_ARMORY_API_KEY"] = a.secret; delete(rig.env, "WOW_ARMORY_CLIENT_ID") },
		"wrong id":    func() { rig.env["WOW_ARMORY_CLIENT_ID"] = e2eKey(t) },
	} {
		mutate()
		var args []string
		if name == "secret flag" {
			args = []string{"--api-key=" + a.secret}
		}
		var o, e bytes.Buffer
		code := run(context.Background(), args, env(rig.env), &o, &e, rig.hooks)
		if code != 2 {
			t.Fatalf("%s: exit %d out=%q err=%q", name, code, o.String(), e.String())
		}
		extra := []string{rig.env["WOW_ARMORY_API_KEY"], rig.env["WOW_ARMORY_CLIENT_ID"], rig.env["RADAR_ARMORY_DB_KEY"]}
		stack.assertNoKeys(append(secrets, extra...), o.String(), e.String())
	}
	if _, tables, _ := stack.inspect(); tables["armory_characters"] != 0 {
		t.Fatalf("a failed-auth run stored rows: %v", tables)
	}
}

// ---- hostile ----

func e2eHostileArmory(t *testing.T, trapURL string) *e2eArmory {
	a := newE2EArmory(e2eKey(t), 330)
	a.addRoster(6)
	// Hostile seeds. radar-db validates names, so most of these never leave radar-armory's DB client (they are
	// dropped as invalid seeds); the ones that pass are refused by the armory client. None may become a request or a row.
	hostile := []e2eRef{
		{e2eRealm, "../../etc/passwd"}, {e2eRealm, "a@evil.test"}, {e2eRealm, "%2f%2e%2e"}, {e2eRealm, "Bad\x00Name"},
		{e2eRealm, "Evil‮Name"}, {e2eRealm, strings.Repeat("A", 5000)}, {e2eRealm, "x'; DROP TABLE armory_characters;--"},
		{e2eRealm, "Robert'); DROP TABLE armory_runs;--"}, {e2eRealm, "<script>alert(1)</script>"}, {e2eRealm, "a/b"},
		{e2eRealm, ""}, {"../../", "Okname"}, {"Realm\x00", "Okname"}, {"http://evil.test/", "Okname"}, {strings.Repeat("R", 300), "Okname"},
		{"Test realm' OR '1'='1", "Okname"}, {"", "Okname"},
	}
	for i, h := range hostile {
		a.insert(5+i*17, h)
	}
	// Hostile guild seeds: names that cannot be slugs, and guilds whose roster is garbage, huge or a redirect.
	for _, g := range []string{"../../etc", "a/b", "x?y=1", "a‮b", strings.Repeat("g", 200), "Garbageguild", "Hugeguild", "Redirguild"} {
		a.guilds = append(a.guilds, domain.ArmorySeed{Kind: domain.ArmorySeedGuild, Realm: e2eRealm, Guild: g})
	}
	chunk := strings.Repeat("A", 1024)
	special := map[string]func(http.ResponseWriter, *http.Request){
		"Hugebody": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"level":5,"guild":"` + strings.Repeat("x", 2<<20) + `"}`))
		},
		"Endless": func(w http.ResponseWriter, r *http.Request) {
			for r.Context().Err() == nil {
				if _, err := w.Write([]byte(chunk)); err != nil {
					return
				}
			}
		},
		"Deepnest": e2eRawHandler(`{"level":10,"character_class":{"name":"Mage"}}`),
		"Deepjson": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"level":10,"guild":` + strings.Repeat("[", 200000) + strings.Repeat("]", 200000) + `}`))
		},
		"Bigblob":   e2eRawHandler(`{"level":11,"character_class":{"name":"Mage"}}`),
		"Arrayblob": e2eRawHandler(`{"level":12,"character_class":{"name":"Mage"}}`),
		"Liar": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100000")
			_, _ = w.Write([]byte(`{"level":`))
		},
		"Stall": func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() },
		"Redira": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, trapURL+"/loopback", http.StatusFound)
		},
		"Redirb": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://other-host.invalid/x", http.StatusMovedPermanently)
		},
		"Garbagea":  e2eRawHandler("\x00\x01\xff not json"),
		"Nullbody":  e2eRawHandler("null"),
		"Wildnums":  e2eRawHandler(`{"level":1e30,"last_login_timestamp":-3,"character_class":{"name":"Mage"}}`),
		"Sqlguild":  e2eRawHandler(`{"level":20,"character_class":{"name":"Mage"},"guild":{"name":"'; DROP TABLE armory_characters;--"}}`),
		"Bidiguild": e2eRawHandler(`{"level":20,"character_class":{"name":"Mage"},"guild":{"name":"Evil‮Guild\u0000\u0007 end"}}`),
		"Hugejson":  e2eRawHandler(`{"level":21,"character_class":{"name":"Mage"},"guild":{"name":"` + strings.Repeat("g", 900<<10) + `"}}`),
		"Hiddenz":   e2eStatusHandler(http.StatusForbidden),
		"Gonez":     e2eStatusHandler(http.StatusNotFound),
		"Servererr": e2eStatusHandler(http.StatusInternalServerError),
	}
	// Hostile sub-resources: the character itself is fine, only the blob is damaged (and must be dropped).
	a.subSpecial["deepnest"] = func(w http.ResponseWriter, _ *http.Request, sub string) {
		if sub == "specializations" {
			_, _ = w.Write([]byte(strings.Repeat(`{"a":`, 60) + "1" + strings.Repeat("}", 60)))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}
	a.subSpecial["bigblob"] = func(w http.ResponseWriter, _ *http.Request, sub string) {
		if sub == "statistics" {
			_, _ = w.Write([]byte(`{"health":77,"junk":"` + strings.Repeat("y", 40000) + `"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}
	a.subSpecial["arrayblob"] = func(w http.ResponseWriter, _ *http.Request, sub string) {
		switch sub {
		case "specializations":
			_, _ = w.Write([]byte(`[1,2,3]`))
		case "pvp-summary":
			_, _ = w.Write([]byte(`"text"`))
		case "equipment":
			_, _ = w.Write([]byte(`{"equipped_items":"nope"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
	a.subSpecial["wildnums"] = func(w http.ResponseWriter, _ *http.Request, sub string) {
		if sub == "statistics" {
			_, _ = w.Write([]byte(`{"health":-5,"power":1e99,"power_type":{"name":"Mana"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}
	i := 0
	for name, h := range special {
		a.special[strings.ToLower(name)] = h
		a.insert(30+i*13, e2eRef{e2eRealm, name})
		i++
	}
	a.guildOverride = func(guild string, w http.ResponseWriter, r *http.Request) bool {
		switch guild {
		case "garbageguild":
			_, _ = w.Write([]byte("\x00\x01 not json"))
		case "hugeguild":
			_, _ = w.Write([]byte(`{"members":[` + strings.Repeat(`{"character":{"name":"Hugemember","realm":{"slug":"testrealm"}}},`, 40000) + `{}]}`))
		case "redirguild":
			http.Redirect(w, r, trapURL+"/roster", http.StatusFound)
		default:
			return false
		}
		return true
	}
	return a
}

var e2eMustFail = []string{"Hugebody", "Endless", "Liar", "Stall", "Redira", "Redirb", "Garbagea", "Nullbody", "Servererr", "Deepjson"}

func TestE2EHostileArmoryAndSQLiteStaysConsistent(t *testing.T) {
	stack := newE2EStack(t)
	trapHits := atomic.Int64{}
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { trapHits.Add(1) }))
	defer trap.Close()
	a := e2eHostileArmory(t, trap.URL)
	rig := newE2ERig(t, a, stack)
	good := a.goodNames()
	for _, n := range []string{"Hugebody", "Endless", "Deepnest", "Deepjson", "Bigblob", "Arrayblob", "Liar", "Stall", "Redira", "Redirb", "Garbagea", "Nullbody",
		"Wildnums", "Sqlguild", "Bidiguild", "Hugejson", "Hiddenz", "Gonez", "Servererr"} {
		good = slicesRemove(good, n)
	}

	started := time.Now()
	code, out, errOut := rig.run(context.Background())
	if time.Since(started) > 90*time.Second {
		t.Fatalf("hostile run took %s", time.Since(started))
	}
	sum := e2eSummary(out)
	if code != 1 || sum["status"] != "partial" {
		t.Fatalf("exit %d out=%q err=%q", code, out, errOut)
	}
	if e2eAtoi(sum["failed"]) < len(e2eMustFail) {
		t.Fatalf("failed=%s, want at least %d: %q", sum["failed"], len(e2eMustFail), out+" ERR:"+errOut)
	}

	// Nothing hostile became a request, nothing left for another host, no redirect was followed.
	a.mu.Lock()
	uris := append([]string(nil), a.uris...)
	a.mu.Unlock()
	for _, u := range uris {
		for _, bad := range []string{"etc", "passwd", "evil", "%2f", "%2F", "%00", "DROP", "script", "%E2%80%AE", "OR%20", "Realm%00", "%3F", "x%3Fy"} {
			if strings.Contains(u, bad) {
				t.Fatalf("hostile text reached the armory in %q", u)
			}
		}
	}
	if hosts := rig.rt.seen(); len(hosts) != 1 || "http://"+hosts[0] != rig.armorySrv.URL {
		t.Fatalf("requests went to %v, want only %s", hosts, rig.armorySrv.URL)
	}
	if trapHits.Load() != 0 {
		t.Fatalf("a redirect was followed: %d hits on the trap", trapHits.Load())
	}

	// Every good character survived the poisoned neighbours (no whole batch was lost).
	got := stack.readAll(good)
	if len(got) != len(good) {
		t.Fatalf("%d of %d good characters stored (poison cost a batch?)", len(got), len(good))
	}
	for _, n := range e2eMustFail {
		if _, ok := stack.readAll([]string{n})[n]; ok {
			t.Errorf("poisoned character %s was stored", n)
		}
	}
	odd := stack.readAll([]string{"Hiddenz", "Gonez"})
	if odd["Hiddenz"].Status != "hidden" || odd["Gonez"].Status != "notfound" {
		t.Fatalf("hidden/notfound: %+v", odd)
	}
	// Damaged blobs were dropped, the characters kept.
	dam := stack.readAll([]string{"Deepnest", "Bigblob", "Arrayblob", "Wildnums"})
	if len(dam) != 4 || len(dam["Deepnest"].Talents) != 0 || len(dam["Bigblob"].Stats) != 0 || len(dam["Arrayblob"].Talents) != 0 || len(dam["Arrayblob"].PvP) != 0 || len(dam["Arrayblob"].Gear) != 0 {
		t.Fatalf("damaged blobs: %+v", dam)
	}
	if w := dam["Wildnums"]; w.Level != domain.PingLevelMax || w.Health != 0 || w.Power != domain.ArmoryValueMax {
		t.Fatalf("wild numbers not clamped: %+v", w)
	}
	stack.assertNoKeys(rig.secrets(), out, errOut)

	// The file is consistent and holds only clean rows.
	_, tables, names := stack.inspect()
	if tables["armory_characters"] < len(good) {
		t.Fatalf("tables %v", tables)
	}
	for _, rn := range names {
		for _, r := range rn[1] {
			if !(unicode.IsLetter(r) || r == '\'' || r == '-' || r == ' ') {
				t.Fatalf("unclean name stored: %q", rn[1])
			}
		}
		for _, r := range rn[0] {
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '-' || r == ' ') {
				t.Fatalf("unclean realm stored: %q", rn[0])
			}
		}
	}
	checkDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(stack.dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer checkDB.Close()
	rows, err := checkDB.Query("SELECT guild, level, health, power, status FROM armory_characters")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var guild, status string
		var level, health, power int64
		if err := rows.Scan(&guild, &level, &health, &power, &status); err != nil {
			t.Fatal(err)
		}
		if len([]rune(guild)) > 64 || level < 0 || level > domain.PingLevelMax || health < 0 || health > domain.ArmoryValueMax || power < 0 || power > domain.ArmoryValueMax {
			t.Fatalf("out of range row: guild len %d level %d health %d power %d", len(guild), level, health, power)
		}
		if strings.ContainsAny(guild, "‮\x00\x07") {
			t.Fatalf("unclean guild: %q", guild)
		}
	}
}

func slicesRemove(s []string, v string) []string {
	out := s[:0:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// A malicious radar-db (or a man in the middle on loopback): oversize, lying and redirecting answers.
func TestE2EMaliciousDBStub(t *testing.T) {
	cases := map[string]func(trap string) http.HandlerFunc{
		"huge reply": func(string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat(" ", 3<<20) + `{}`))
			}
		},
		"endless reply": func(string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				for r.Context().Err() == nil {
					if _, err := w.Write(bytes.Repeat([]byte("x"), 4096)); err != nil {
						return
					}
				}
			}
		},
		"not json": func(string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("\x00\x01 garbage")) }
		},
		"redirect to trap": func(trap string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, trap+"/steal", http.StatusTemporaryRedirect)
			}
		},
		"redirect elsewhere": func(string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "http://other-host.invalid/x", http.StatusPermanentRedirect)
			}
		},
		"nested json": func(string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat(`{"a":`, 5000) + "1" + strings.Repeat("}", 5000)))
			}
		},
		"negative counts": func(string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"duplicate":true,"stored":-5,"skipped":99999999999}`))
			}
		},
		"stall": func(string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }
		},
		"reset": func(string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if hj, ok := w.(http.Hijacker); ok {
					if c, _, err := hj.Hijack(); err == nil {
						_ = c.Close()
					}
				}
			}
		},
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if testing.Short() {
				t.Skip("slow")
			}
			var trapHits atomic.Int64
			trap := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { trapHits.Add(1) }))
			defer trap.Close()
			var seenKeyMu sync.Mutex
			var leaked bool
			dbKey := e2eKey(t)
			a := newE2EArmory(e2eKey(t), 30)
			inner := cases[name](trap.URL)
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
				seenKeyMu.Lock()
				if strings.Contains(string(body), a.secret) || strings.Contains(string(body), a.clientID) || r.Header.Get("Authorization") != "" || strings.Contains(r.URL.String(), a.secret) || r.Header.Get("X-Ingest-Key") != dbKey {
					leaked = true
				}
				seenKeyMu.Unlock()
				inner(w, r)
			}))
			defer stub.Close()
			armorySrv := httptest.NewServer(a)
			defer armorySrv.Close()
			e := map[string]string{
				"WOW_ARMORY_API_KEY": a.secret, "WOW_ARMORY_CLIENT_ID": a.clientID, "RADAR_ARMORY_DB_KEY": dbKey, "RADAR_ARMORY_BASE_URL": armorySrv.URL,
				"RADAR_ARMORY_TOKEN_URL": armorySrv.URL + "/oauth/token",
				"RADAR_ARMORY_DB_URL":    stub.URL, "RADAR_ARMORY_RATE": "1000", "RADAR_ARMORY_LOCATION": "UTC",
				"RADAR_ARMORY_MAX_RUNTIME": "2m", "RADAR_ARMORY_CONCURRENCY": "2",
			}
			hooks := testHooks{allowHTTP: true, transport: newE2EHostRT(), sleep: func(context.Context, time.Duration) error { return nil }}
			var o, er bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- run(context.Background(), nil, env(e), &o, &er, hooks) }()
			var code int
			select {
			case code = <-done:
			case <-time.After(120 * time.Second):
				t.Fatal("run did not end against a hostile radar-db")
			}
			// Cannot be ok: nothing was durably stored; may be 1 (partial) or 2 (fatal), never 0 or a crash.
			if code != 1 && code != 2 {
				t.Fatalf("exit %d out=%q err=%q", code, o.String(), er.String())
			}
			if trapHits.Load() != 0 {
				t.Fatalf("redirect followed: %d", trapHits.Load())
			}
			seenKeyMu.Lock()
			defer seenKeyMu.Unlock()
			if leaked {
				t.Fatal("the stub saw the armory key or a wrong DB key header")
			}
			for _, s := range []string{o.String(), er.String()} {
				if strings.Contains(s, a.secret) || strings.Contains(s, a.clientID) || strings.Contains(s, dbKey) {
					t.Fatalf("a key leaked into the output: %q", s)
				}
			}
		})
	}
}

// A malicious radar-db that serves valid runs and batches but hostile SEED pages: the crawl never loops, never
// follows a redirect, never believes oversized or malformed pages, and ends partial (1), never ok and never a crash.
func TestE2EMaliciousSeeds(t *testing.T) {
	cases := map[string]func(trap string, w http.ResponseWriter, r *http.Request){
		"huge": func(_ string, w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat(" ", 2<<20) + `{"items":[],"next":""}`))
		},
		"endless": func(_ string, w http.ResponseWriter, r *http.Request) {
			for r.Context().Err() == nil {
				if _, err := w.Write(bytes.Repeat([]byte("x"), 4096)); err != nil {
					return
				}
			}
		},
		"not json": func(_ string, w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("\x00\x01 garbage")) },
		"redirect": func(trap string, w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, trap+"/steal", http.StatusTemporaryRedirect)
		},
		"unknown fields": func(_ string, w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"items":[],"next":"","admin":true}`))
		},
		"cursor loop": func(_ string, w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(domain.ArmorySeedPage{Next: "same"})
		},
		"nested": func(_ string, w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat(`{"items":`, 5000) + "1" + strings.Repeat("}", 5000)))
		},
		"too many items": func(_ string, w http.ResponseWriter, r *http.Request) {
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			page := domain.ArmorySeedPage{}
			for i := 0; i <= limit+5; i++ {
				page.Items = append(page.Items, domain.ArmorySeed{Kind: domain.ArmorySeedCharacter, Realm: e2eRealm, Name: e2eName(i)})
			}
			_ = json.NewEncoder(w).Encode(page)
		},
		"hostile items": func(_ string, w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"items":[{"kind":"character","realm":"x","name":"../../etc/passwd"},{"kind":"guild","realm":"x","guild":"a/b?c=d"}],"next":""}`))
		},
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if testing.Short() {
				t.Skip("slow")
			}
			var trapHits atomic.Int64
			trap := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { trapHits.Add(1) }))
			defer trap.Close()
			a := newE2EArmory(e2eKey(t), 0)
			db := &fakeDB{}
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/internal/v1/armory/seeds" {
					cases[name](trap.URL, w, r)
					return
				}
				db.ServeHTTP(w, r)
			}))
			defer stub.Close()
			armorySrv := httptest.NewServer(a)
			defer armorySrv.Close()
			e := map[string]string{
				"WOW_ARMORY_API_KEY": a.secret, "WOW_ARMORY_CLIENT_ID": a.clientID, "RADAR_ARMORY_DB_KEY": fakeDBKey, "RADAR_ARMORY_BASE_URL": armorySrv.URL,
				"RADAR_ARMORY_TOKEN_URL": armorySrv.URL + "/oauth/token", "RADAR_ARMORY_DB_URL": stub.URL, "RADAR_ARMORY_RATE": "1000",
				"RADAR_ARMORY_LOCATION": "UTC", "RADAR_ARMORY_MAX_RUNTIME": "2m", "RADAR_ARMORY_CONCURRENCY": "2",
			}
			hooks := testHooks{allowHTTP: true, transport: newE2EHostRT(), sleep: func(context.Context, time.Duration) error { return nil }}
			var o, er bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- run(context.Background(), nil, env(e), &o, &er, hooks) }()
			var code int
			select {
			case code = <-done:
			case <-time.After(60 * time.Second):
				t.Fatal("run did not end against hostile seeds")
			}
			if name == "hostile items" {
				// invalid seeds are dropped one by one: a clean, empty run
				if code != 0 || a.total.Load() != 0 {
					t.Fatalf("exit %d out=%q err=%q requests=%d", code, o.String(), er.String(), a.total.Load())
				}
			} else if code != 1 {
				t.Fatalf("exit %d out=%q err=%q", code, o.String(), er.String())
			}
			if trapHits.Load() != 0 {
				t.Fatalf("redirect followed: %d", trapHits.Load())
			}
			if a.total.Load() != 0 {
				t.Fatalf("the armory was contacted (%d requests) on the strength of a hostile seed page", a.total.Load())
			}
			for _, s := range []string{o.String(), er.String()} {
				if strings.Contains(s, a.secret) || strings.Contains(s, a.clientID) || strings.Contains(s, fakeDBKey) {
					t.Fatalf("a key leaked into the output: %q", s)
				}
			}
		})
	}
}

// realRig talks to the REAL radar-db binary for everything, the seed route included (no gateway stub).
func newE2ERealRig(t *testing.T, stack *e2eStack, a *e2eArmory) *e2eRig {
	t.Helper()
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	rig := &e2eRig{armory: a, armorySrv: srv, stack: stack, rt: newE2EHostRT(),
		env: map[string]string{
			"WOW_ARMORY_API_KEY": a.secret, "WOW_ARMORY_CLIENT_ID": a.clientID, "RADAR_ARMORY_DB_KEY": stack.ingestKey,
			"RADAR_ARMORY_BASE_URL": srv.URL, "RADAR_ARMORY_TOKEN_URL": srv.URL + "/oauth/token", "RADAR_ARMORY_DB_URL": "http://" + stack.intl,
			"RADAR_ARMORY_RATE": "1000", "RADAR_ARMORY_LOCATION": "UTC", "RADAR_ARMORY_MAX_RUNTIME": "2m",
		}}
	rig.hooks = testHooks{allowHTTP: true, transport: rig.rt, sleep: func(context.Context, time.Duration) error { return nil }}
	return rig
}

// postContacts sends a ping with synthetic contacts "<name>-Testrealm" to the real radar-db.
func (s *e2eStack) postContacts(batch string, names ...string) {
	s.t.Helper()
	now := time.Now().UnixMilli()
	var cs []string
	for i, n := range names {
		cs = append(cs, fmt.Sprintf(`{"guid":"Player-1-%X","name":%q,"raw":%q,"reaction":"hostile","level":60,"x":10,"y":20,"mapId":1,"zone":"Zone","posTime":%d,"firstSeen":%d,"lastSeen":%d,"d":{"events":1},"fixes":[{"t":%d,"mapId":1,"x":10,"y":20,"kind":"exact"}]}`,
			0xA0+i, n, n+"-"+e2eRealm, now, now-1000, now, now))
	}
	body := fmt.Sprintf(`{"rs":"pvp","batchId":%q,"source":"e2e","contacts":[%s],"charts":[],"deaths":[]}`, batch, strings.Join(cs, ","))
	req, _ := http.NewRequest(http.MethodPost, "http://"+s.intl+"/internal/v1/ping", strings.NewReader(body))
	req.Header.Set("X-Ingest-Key", s.ingestKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.t.Fatalf("ping: HTTP %d %s", resp.StatusCode, data)
	}
}

// The real radar-db serves the seed route: over an empty database a full run ends ok with nothing enumerated.
func TestE2ERealSeedsRoute(t *testing.T) {
	stack := newE2EStack(t)
	rig := newE2ERealRig(t, stack, newE2EArmory(e2eKey(t), 0))
	code, out, errOut := rig.run(context.Background())
	if sum := e2eSummary(out); code != 0 || sum["status"] != "ok" || sum["enumerated"] != "0" {
		t.Fatalf("exit %d out=%q err=%q", code, out, errOut)
	}
}

// The first-night flow against the real radar-db: known contacts are the only seeds on night one (their
// characters are stored, the armory shows their guild); on a later run the guild is seeded, its roster is
// fetched and the members are stored.
func TestE2ERealFirstNightFlow(t *testing.T) {
	stack := newE2EStack(t)
	a := newE2EArmory(e2eKey(t), 0)
	a.addRoster(3)
	stack.postContacts("night1", "Synaa", "Synab")
	rig := newE2ERealRig(t, stack, a)

	code, out, errOut := rig.run(context.Background())
	sum := e2eSummary(out)
	if code != 0 || sum["status"] != "ok" || sum["enumerated"] != "2" || sum["stored"] != "2" || a.rosters.Load() != 0 {
		t.Fatalf("night 1: exit %d out=%q err=%q rosters=%d", code, out, errOut, a.rosters.Load())
	}
	if got := stack.readAll([]string{"Synaa", "Synab"}); len(got) != 2 || got["Synaa"].Guild != "Testers" {
		t.Fatalf("contacts not stored with their guild: %+v", got)
	}

	code, out, errOut = rig.run(context.Background())
	sum = e2eSummary(out)
	if code != 0 || sum["status"] != "ok" || a.rosters.Load() != 1 {
		t.Fatalf("night 2: exit %d out=%q err=%q rosters=%d", code, out, errOut, a.rosters.Load())
	}
	if got := stack.readAll(a.rosterNames); len(got) != len(a.rosterNames) {
		t.Fatalf("roster members stored: %d of %d (out=%q)", len(got), len(a.rosterNames), out)
	}
	for _, s := range rig.secrets() {
		if strings.Contains(out+errOut, s) {
			t.Fatal("a secret leaked into the output")
		}
	}
}
