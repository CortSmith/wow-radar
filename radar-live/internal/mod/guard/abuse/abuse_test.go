package abuse

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
)

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
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func testConfig() Config {
	return Config{MaxFailures: 5, Window: 10 * time.Minute, BaseBan: time.Minute, MaxBan: 24 * time.Hour, MaxEntries: 100}
}

func failN(t *Table, a netip.Addr, n int) bool {
	banned := false
	for i := 0; i < n; i++ {
		banned = t.Fail(a)
	}
	return banned
}

func TestLockoutAfterFailures(t *testing.T) {
	c := newClock()
	tb := NewTable(testConfig(), c.Now)
	a := ip("203.0.113.7")
	if failN(tb, a, 4) {
		t.Fatal("4 failures must not ban")
	}
	if _, b := tb.Banned(a); b {
		t.Fatal("banned too early")
	}
	if !tb.Fail(a) {
		t.Fatal("5th failure bans")
	}
	until, b := tb.Banned(a)
	if !b || !until.Equal(c.Now().Add(time.Minute)) {
		t.Fatalf("until=%v banned=%v", until, b)
	}
	if _, b := tb.Banned(ip("203.0.113.8")); b {
		t.Fatal("other address must not be banned")
	}
}

func TestLockoutEscalatesAndCaps(t *testing.T) {
	c := newClock()
	tb := NewTable(testConfig(), c.Now)
	a := ip("203.0.113.7")
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	for i, d := range want {
		failN(tb, a, 5)
		until, b := tb.Banned(a)
		if !b || until.Sub(c.Now()) != d {
			t.Fatalf("ban %d: want %v got %v (banned=%v)", i+1, d, until.Sub(c.Now()), b)
		}
		c.Advance(d) // retry right after the ban ends: escalation continues
	}
	for i := 0; i < 20; i++ {
		failN(tb, a, 5)
		until, _ := tb.Banned(a)
		c.Advance(until.Sub(c.Now()))
	}
	failN(tb, a, 5)
	until, _ := tb.Banned(a)
	if got := until.Sub(c.Now()); got != 24*time.Hour {
		t.Fatalf("ban must cap at 24h, got %v", got)
	}
}

func TestBanExpiryAndStrikeMemory(t *testing.T) {
	c := newClock()
	tb := NewTable(testConfig(), c.Now)
	a := ip("203.0.113.7")
	failN(tb, a, 5)
	c.Advance(time.Minute)
	if _, b := tb.Banned(a); b {
		t.Fatal("ban must expire")
	}
	// A quiet period of one more ban length forgets the strike: the next ban is the base length again.
	c.Advance(2 * time.Minute)
	failN(tb, a, 5)
	until, _ := tb.Banned(a)
	if until.Sub(c.Now()) != time.Minute {
		t.Fatalf("strikes should have been forgotten, got %v", until.Sub(c.Now()))
	}
}

func TestFailuresOutsideWindowDoNotAccumulate(t *testing.T) {
	c := newClock()
	tb := NewTable(testConfig(), c.Now)
	a := ip("203.0.113.7")
	failN(tb, a, 4)
	c.Advance(11 * time.Minute)
	if tb.Fail(a) {
		t.Fatal("old failures must age out of the window")
	}
}

func TestUnban(t *testing.T) {
	c := newClock()
	tb := NewTable(testConfig(), c.Now)
	a := ip("203.0.113.7")
	failN(tb, a, 5)
	if !tb.Unban(a) {
		t.Fatal("unban reports it was banned")
	}
	if _, b := tb.Banned(a); b {
		t.Fatal("still banned")
	}
	if tb.Unban(a) {
		t.Fatal("second unban finds nothing")
	}
	failN(tb, a, 5)
	if until, _ := tb.Banned(a); until.Sub(c.Now()) != time.Minute {
		t.Fatal("unban forgets strikes")
	}
}

func TestAdminBan(t *testing.T) {
	c := newClock()
	tb := NewTable(testConfig(), c.Now)
	if err := tb.Ban(ip("203.0.113.9"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, b := tb.Banned(ip("203.0.113.9")); !b {
		t.Fatal("not banned")
	}
	if err := tb.Ban(ip("127.0.0.1"), time.Hour); !errors.Is(err, ErrExempt) {
		t.Fatalf("loopback exempt: %v", err)
	}
	if tb.Ban(netip.Addr{}, time.Hour) == nil {
		t.Fatal("invalid address must fail")
	}
}

func TestLoopbackExemptByDefault(t *testing.T) {
	tb := NewTable(testConfig(), newClock().Now)
	if failN(tb, ip("127.0.0.1"), 50) || failN(tb, ip("::1"), 50) {
		t.Fatal("loopback must never be banned by default")
	}
	cfg := testConfig()
	cfg.BanLoopback = true
	tb = NewTable(cfg, newClock().Now)
	if !failN(tb, ip("127.0.0.1"), 5) {
		t.Fatal("BanLoopback allows it")
	}
}

func TestIPv6SharesPrefixBan(t *testing.T) {
	tb := NewTable(testConfig(), newClock().Now)
	for i := 0; i < 5; i++ {
		tb.Fail(ip(fmt.Sprintf("2001:db8:1:2::%x", i+1)))
	}
	if _, b := tb.Banned(ip("2001:db8:1:2:ffff::9")); !b {
		t.Fatal("rotating addresses inside a /64 must share the ban")
	}
	if _, b := tb.Banned(ip("2001:db8:1:3::1")); b {
		t.Fatal("other /64 unaffected")
	}
}

func TestMemoryBounded(t *testing.T) {
	cfg := testConfig()
	cfg.MaxEntries = 50
	tb := NewTable(cfg, newClock().Now)
	for i := 0; i < 5000; i++ {
		tb.Fail(netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 1}))
		if tb.Len() > 50 {
			t.Fatalf("table grew to %d", tb.Len())
		}
	}
	// Active bans are never evicted to make room for newcomers.
	tb = NewTable(cfg, newClock().Now)
	banned := ip("198.51.100.1")
	failN(tb, banned, 5)
	for i := 0; i < 5000; i++ {
		tb.Fail(netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 2}))
	}
	if _, b := tb.Banned(banned); !b {
		t.Fatal("active ban evicted by a flood of new addresses")
	}
	// Full of bans: newcomers are simply untracked, the table does not grow.
	tb = NewTable(Config{MaxFailures: 1, Window: time.Hour, BaseBan: time.Hour, MaxBan: time.Hour, MaxEntries: 3}, newClock().Now)
	for i := 0; i < 10; i++ {
		tb.Fail(netip.AddrFrom4([4]byte{10, 0, 0, byte(i + 1)}))
	}
	if tb.Len() != 3 || len(tb.Bans()) != 3 {
		t.Fatalf("len %d bans %d", tb.Len(), len(tb.Bans()))
	}
}

func TestConcurrentUse(t *testing.T) {
	tb := NewTable(testConfig(), nil)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				a := netip.AddrFrom4([4]byte{203, 0, byte(g), byte(i)})
				tb.Fail(a)
				tb.Banned(a)
				if i%50 == 0 {
					tb.Bans()
					tb.Prune()
					tb.TakeDirty()
					tb.Unban(a)
				}
			}
		}(g)
	}
	// One address hammered from many goroutines gets banned exactly by the 5th failure.
	hot := ip("198.51.100.99")
	var bans int
	var mu sync.Mutex
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tb.Fail(hot) {
				mu.Lock()
				bans++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if _, b := tb.Banned(hot); b != (bans == 1) {
		t.Fatalf("banned=%v bans=%d", b, bans)
	}
}

func TestConfigValidate(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Config){
		"failures": func(c *Config) { c.MaxFailures = 0 }, "window": func(c *Config) { c.Window = 0 },
		"base": func(c *Config) { c.BaseBan = 0 }, "max<base": func(c *Config) { c.MaxBan = time.Second },
		"entries": func(c *Config) { c.MaxEntries = 0 },
	} {
		c := DefaultConfig()
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s should be invalid", name)
		}
	}
}

// ---- persistence -------------------------------------------------------------------------------

func TestBanExpiryAndPersistence(t *testing.T) {
	c := newClock()
	path := filepath.Join(t.TempDir(), "sub", "bans.json")
	tb := NewTable(testConfig(), c.Now)
	failN(tb, ip("203.0.113.7"), 5)
	failN(tb, ip("2001:db8:1:2::5"), 5)
	if err := SaveBans(path, tb.Bans()); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("mode: %v %v", st, err)
		}
	}
	loaded, err := LoadBans(path, c.Now())
	if err != nil || len(loaded) != 2 {
		t.Fatalf("round trip: %v %v", loaded, err)
	}
	t2 := NewTable(testConfig(), c.Now)
	t2.Replace(loaded)
	if _, b := t2.Banned(ip("203.0.113.7")); !b {
		t.Fatal("ban lost in the round trip")
	}
	if got := t2.Bans(); len(got) != 2 || got[0].Strikes != 1 {
		t.Fatalf("bans %+v", got)
	}
	// Expired entries are pruned at load.
	c.Advance(2 * time.Minute)
	loaded, err = LoadBans(path, c.Now())
	if err != nil || len(loaded) != 0 {
		t.Fatalf("expired bans must be dropped: %v %v", loaded, err)
	}
	// No temporary files are left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("leftovers: %v", entries)
	}
}

func TestMissingBanFileIsEmpty(t *testing.T) {
	bans, err := LoadBans(filepath.Join(t.TempDir(), "nope.json"), time.Now())
	if err != nil || bans != nil {
		t.Fatalf("%v %v", bans, err)
	}
}

func TestCorruptBanFile(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"garbage": "not json at all", "truncated": `{"version":1,"bans":[{"ip":"1.2.3.4"`,
		"version": `{"version":99,"bans":[]}`, "binary": "\x00\x01\x02", "empty": "",
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		bans, err := LoadBans(p, time.Now())
		if !errors.Is(err, ErrCorrupt) || len(bans) != 0 {
			t.Errorf("%s: %v %v", name, bans, err)
		}
	}
	// Bad entries inside a good file are skipped, good ones kept.
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	p := filepath.Join(dir, "mixed")
	doc := `{"version":1,"bans":[{"ip":"nope","until":"2031-01-01T00:00:00Z"},{"ip":"203.0.113.1","until":"2031-01-01T00:00:00Z","strikes":2},
	{"ip":"203.0.113.2","until":"2020-01-01T00:00:00Z"},{"ip":"203.0.113.3","until":"2031-01-01T00:00:00Z","strikes":-4}]}`
	_ = os.WriteFile(p, []byte(doc), 0o600)
	bans, err := LoadBans(p, now)
	if err != nil || len(bans) != 1 || bans[0].IP != ip("203.0.113.1") || bans[0].Strikes != 2 {
		t.Fatalf("%v %v", bans, err)
	}
}

func TestSaveDoesNotLeakKeysOrExtraData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bans.json")
	if err := SaveBans(path, []Ban{{IP: ip("203.0.113.7"), Until: time.Now().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte("203.0.113.7")) || bytes.Contains(bytes.ToLower(data), []byte("key")) {
		t.Fatalf("unexpected content: %s", data)
	}
}

func TestReplaceLiftsRemovedBans(t *testing.T) {
	c := newClock()
	tb := NewTable(testConfig(), c.Now)
	failN(tb, ip("203.0.113.7"), 5)
	failN(tb, ip("203.0.113.8"), 5)
	tb.Replace([]Ban{{IP: ip("203.0.113.8"), Until: c.Now().Add(time.Hour), Strikes: 1}})
	if _, b := tb.Banned(ip("203.0.113.7")); b {
		t.Fatal("ban removed from the file must be lifted")
	}
	if _, b := tb.Banned(ip("203.0.113.8")); !b {
		t.Fatal("ban in the file must stay")
	}
}

// ---- admin -------------------------------------------------------------------------------------

func TestAdminListBanUnban(t *testing.T) {
	c := newClock()
	path := filepath.Join(t.TempDir(), "bans.json")
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := Admin(args, path, c.Now, &out)
		return out.String(), err
	}
	if out, err := run("list"); err != nil || !strings.Contains(out, "no active bans") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := run("ban", "203.0.113.7", "2h"); err != nil || !strings.Contains(out, "banned 203.0.113.7") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := run("ban", "::ffff:203.0.113.9", "30m"); err != nil || !strings.Contains(out, "203.0.113.9") {
		t.Fatalf("4in6 is normalised: %q %v", out, err)
	}
	out, err := run("list")
	if err != nil || !strings.Contains(out, "203.0.113.7") || !strings.Contains(out, "2030-01-01T02:00:00Z") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := run("unban", "203.0.113.7"); err != nil || !strings.Contains(out, "unbanned") {
		t.Fatalf("%q %v", out, err)
	}
	if out, _ := run("unban", "203.0.113.7"); !strings.Contains(out, "was not banned") {
		t.Fatalf("%q", out)
	}
	bans, _ := LoadBans(path, c.Now())
	if len(bans) != 1 || bans[0].IP != ip("203.0.113.9") {
		t.Fatalf("file now %v", bans)
	}
	c.Advance(time.Hour)
	if out, _ := run("list"); strings.Contains(out, "203.0.113.9") && !strings.Contains(out, "no active") {
		t.Fatalf("expired ban listed: %q", out)
	}
}

func TestAdminRejectsBadInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bans.json")
	for _, args := range [][]string{
		nil, {"nope"}, {"unban"}, {"unban", "not-an-ip"}, {"ban", "203.0.113.7"}, {"ban", "203.0.113.7", "abc"},
		{"ban", "203.0.113.7", "-1h"}, {"ban", "203.0.113.7", "99999h"}, {"list", "extra"}, {"unban", "1.1.1.1", "2.2.2.2"},
	} {
		if err := Admin(args, path, nil, &bytes.Buffer{}); err == nil {
			t.Errorf("%v should fail", args)
		}
	}
	if err := Admin([]string{"list"}, "", nil, &bytes.Buffer{}); err == nil {
		t.Error("no ban file configured must fail")
	}
}

func TestAdminNeverOverwritesCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bans.json")
	_ = os.WriteFile(path, []byte("garbage"), 0o600)
	if err := Admin([]string{"ban", "203.0.113.7", "1h"}, path, nil, &bytes.Buffer{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("%v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "garbage" {
		t.Fatal("corrupt file was overwritten")
	}
}

func TestResolveBanFile(t *testing.T) {
	if got := ResolveBanFile("/x/b.json", "/d/db"); got != "/x/b.json" {
		t.Fatal(got)
	}
	if got := ResolveBanFile("", filepath.Join("d", "radar.db")); got != filepath.Join("d", BanFileName) {
		t.Fatal(got)
	}
	if ResolveBanFile("", "") != "" {
		t.Fatal("memory only")
	}
}

// ---- HTTP guard --------------------------------------------------------------------------------

type rig struct {
	t     *testing.T
	clock *fakeClock
	table *Table
	h     http.Handler
}

// newRig guards a backend that accepts the key "good-key-0000000000" and answers 401 otherwise.
func newRig(t *testing.T, trusted, allow string, class string) *rig {
	t.Helper()
	c := newClock()
	tb := NewTable(testConfig(), c.Now)
	var tr, al []netip.Prefix
	if trusted != "" {
		tr = []netip.Prefix{netip.MustParsePrefix(trusted)}
	}
	if allow != "" {
		al = []netip.Prefix{netip.MustParsePrefix(allow)}
	}
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-View-Key") == "good-key-0000000000" || r.Header.Get("X-Ingest-Key") == "good-key-0000000000" {
			w.WriteHeader(http.StatusOK)
			return
		}
		core.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid key")
	})
	return &rig{t: t, clock: c, table: tb, h: NewGuard(tb, tr, al).Wrap(class, backend)}
}

func (g *rig) do(remote string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/v1/sweep", nil)
	r.RemoteAddr = remote
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Add(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	g.h.ServeHTTP(w, r)
	return w
}

func TestAuthFailBudget(t *testing.T) {
	g := newRig(t, "", "", core.ClassPublic)
	for i := 0; i < 5; i++ {
		if w := g.do("203.0.113.7:1", "X-View-Key", "wrong-key-00000000"); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	w := g.do("203.0.113.7:1", "X-View-Key", "good-key-0000000000")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" {
		t.Fatalf("banned client, even with a good key: %d %v", w.Code, w.Header())
	}
	if w := g.do("203.0.113.8:1", "X-View-Key", "good-key-0000000000"); w.Code != 200 {
		t.Fatalf("other client: %d", w.Code)
	}
	g.clock.Advance(61 * time.Second)
	if w := g.do("203.0.113.7:1", "X-View-Key", "good-key-0000000000"); w.Code != 200 {
		t.Fatalf("after expiry: %d", w.Code)
	}
}

func TestRequestsWithoutKeyDoNotCountAsGuessing(t *testing.T) {
	g := newRig(t, "", "", core.ClassPublic)
	for i := 0; i < 50; i++ {
		if w := g.do("203.0.113.7:1"); w.Code != 401 {
			t.Fatalf("%d", w.Code)
		}
	}
	if w := g.do("203.0.113.7:1", "X-View-Key", "good-key-0000000000"); w.Code != 200 {
		t.Fatalf("keyless requests must not ban: %d", w.Code)
	}
}

func TestUniformAuthFailure(t *testing.T) {
	g := newRig(t, "", "", core.ClassPublic)
	var bodies, heads []string
	for i, h := range [][]string{
		{"X-View-Key", "good-key-0000000000"},   // a valid key
		{"X-Ingest-Key", "good-key-0000000000"}, // valid for another role
		{"X-View-Key", "wrong-key-00000000"},
		{"Authorization", "Bearer wrong-key-00000000"},
		nil,
	} {
		// Each probe comes from its own banned address so the refusal is what we compare.
		a := fmt.Sprintf("198.51.100.%d", i+1)
		g.table.Ban(ip(a), time.Hour)
		w := g.do(a+":1", h...)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("probe %d: %d", i, w.Code)
		}
		bodies = append(bodies, w.Body.String())
		heads = append(heads, w.Header().Get("Content-Type")+w.Header().Get("Retry-After"))
	}
	for i := range bodies {
		if bodies[i] != bodies[0] || heads[i] != heads[0] {
			t.Fatalf("banned responses differ: %q/%q vs %q/%q", bodies[i], heads[i], bodies[0], heads[0])
		}
	}
	for _, leak := range []string{"key", "role", "view", "ingest"} {
		if strings.Contains(strings.ToLower(bodies[0]), leak) {
			t.Errorf("refusal body %q mentions %q", bodies[0], leak)
		}
	}
}

func TestBannedIPsAreRefusedBeforeAuthWork(t *testing.T) {
	c := newClock()
	tb := NewTable(testConfig(), c.Now)
	called := false
	h := NewGuard(tb, nil, nil).Wrap(core.ClassPublic, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	tb.Ban(ip("203.0.113.7"), time.Hour)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.7:5"
	r.Header.Set("X-View-Key", "anything")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if called || w.Code != http.StatusTooManyRequests {
		t.Fatalf("called=%v code=%d", called, w.Code)
	}
}

func TestSpoofedForwardedForFromUntrustedPeerCannotDodgeOrFrame(t *testing.T) {
	g := newRig(t, "", "", core.ClassPublic) // no trusted proxies
	// Dodge: rotating X-Forwarded-For does not reset the counter of the real peer.
	for i := 0; i < 5; i++ {
		g.do("203.0.113.7:1", "X-Forwarded-For", fmt.Sprintf("9.9.9.%d", i), "X-View-Key", "wrong-key-00000000")
	}
	if w := g.do("203.0.113.7:1", "X-Forwarded-For", "9.9.9.200", "X-View-Key", "good-key-0000000000"); w.Code != 429 {
		t.Fatalf("spoofed XFF dodged the ban: %d", w.Code)
	}
	// Frame: claiming a victim's address in headers cannot get the victim banned.
	for i := 0; i < 20; i++ {
		g.do("198.51.100.66:1", "X-Forwarded-For", "192.0.2.50", "CF-Connecting-IP", "192.0.2.50", "X-View-Key", "wrong-key-00000000")
	}
	if _, b := g.table.Banned(ip("192.0.2.50")); b {
		t.Fatal("victim framed through a forged header")
	}
	if w := g.do("192.0.2.50:1", "X-View-Key", "good-key-0000000000"); w.Code != 200 {
		t.Fatalf("victim locked out: %d", w.Code)
	}
}

func TestTrustedProxyBansTheRealClient(t *testing.T) {
	g := newRig(t, "10.0.0.0/8", "", core.ClassPublic)
	for i := 0; i < 5; i++ {
		g.do("10.0.0.1:1", "CF-Connecting-IP", "203.0.113.7", "X-View-Key", "wrong-key-00000000")
	}
	if w := g.do("10.0.0.1:2", "CF-Connecting-IP", "203.0.113.7", "X-View-Key", "good-key-0000000000"); w.Code != 429 {
		t.Fatalf("real client behind the proxy must be banned: %d", w.Code)
	}
	if w := g.do("10.0.0.1:3", "CF-Connecting-IP", "203.0.113.8", "X-View-Key", "good-key-0000000000"); w.Code != 200 {
		t.Fatalf("other client behind the same proxy: %d", w.Code)
	}
	if _, b := g.table.Banned(ip("10.0.0.1")); b {
		t.Fatal("the proxy itself must not be banned")
	}
}

func TestInternalRoutesLoopbackOnly(t *testing.T) {
	g := newRig(t, "", "", core.ClassInternal)
	if w := g.do("127.0.0.1:1", "X-Ingest-Key", "good-key-0000000000"); w.Code != 200 {
		t.Fatalf("loopback: %d", w.Code)
	}
	if w := g.do("[::1]:1", "X-Ingest-Key", "good-key-0000000000"); w.Code != 200 {
		t.Fatalf("loopback v6: %d", w.Code)
	}
	if w := g.do("203.0.113.7:1", "X-Ingest-Key", "good-key-0000000000"); w.Code != 403 {
		t.Fatalf("remote peer with a valid key: %d", w.Code)
	}
	// A forwarded header never makes a remote peer local.
	if w := g.do("203.0.113.7:1", "X-Forwarded-For", "127.0.0.1", "X-Ingest-Key", "good-key-0000000000"); w.Code != 403 {
		t.Fatalf("spoofed loopback: %d", w.Code)
	}
	allowed := newRig(t, "", "192.168.1.0/24", core.ClassInternal)
	if w := allowed.do("192.168.1.9:1", "X-Ingest-Key", "good-key-0000000000"); w.Code != 200 {
		t.Fatalf("explicitly allowed peer: %d", w.Code)
	}
	// The public class is not restricted.
	pub := newRig(t, "", "", core.ClassPublic)
	if w := pub.do("203.0.113.7:1", "X-View-Key", "good-key-0000000000"); w.Code != 200 {
		t.Fatalf("public: %d", w.Code)
	}
}

func TestUnparsableRemoteAddrPassesThroughUntracked(t *testing.T) {
	g := newRig(t, "", "", core.ClassPublic)
	for i := 0; i < 10; i++ {
		g.do("garbage", "X-View-Key", "wrong-key-00000000")
	}
	if g.table.Len() != 0 {
		t.Fatal("unparsable peers must not create entries")
	}
}

func TestRecorderSupportsStreaming(t *testing.T) {
	flushed := false
	h := NewGuard(NewTable(testConfig(), nil), nil, nil).Wrap(core.ClassPublic, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("x"))
		_ = http.NewResponseController(w).Flush()
		flushed = true
	}))
	r := httptest.NewRequest("POST", "/v1/stream", nil)
	r.RemoteAddr = "203.0.113.7:1"
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !flushed {
		t.Fatal("flush did not reach the real writer")
	}
}
