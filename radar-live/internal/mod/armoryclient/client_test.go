package armoryclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// All keys, ids and tokens are generated at run time and obviously fake; all names are synthetic.
func fakeSecret() string { return fmt.Sprintf("test-secret-%016x", 4242) }
func fakeID() string     { return fmt.Sprintf("test-client-%016x", 7) }
func fakeToken(n int64) string {
	return fmt.Sprintf("test-token-%016x", 9000+n)
}

type harness struct {
	t         *testing.T
	srv       *httptest.Server
	c         *Client
	hits      atomic.Int64 // API requests (everything but the token endpoint)
	tokenHits atomic.Int64
	expiresIn atomic.Int64
	tokenFn   func(w http.ResponseWriter, r *http.Request, n int64) // optional override
	mu        sync.Mutex
	now       time.Time
	sleeps    []time.Duration
	errs      []error
}

func (h *harness) clock() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }

func (h *harness) advance(d time.Duration) { h.mu.Lock(); h.now = h.now.Add(d); h.mu.Unlock() }

// newHarness starts a fake Battle.net (token endpoint plus the given API handler) and a client with a fake
// clock whose sleeps only advance the clock.
func newHarness(t *testing.T, handler http.HandlerFunc, mutate func(*Config)) *harness {
	t.Helper()
	h := &harness{t: t, now: time.UnixMilli(domain.MinTimestampMs + 1_000_000)}
	h.expiresIn.Store(86399)
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			n := h.tokenHits.Add(1)
			if h.tokenFn != nil {
				h.tokenFn(w, r, n)
				return
			}
			id, sec, ok := r.BasicAuth()
			if r.Method != http.MethodPost || !ok || id != fakeID() || sec != fakeSecret() || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				writeJSON(w, 401, `{"error":"invalid_client"}`)
				return
			}
			if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "client_credentials" {
				writeJSON(w, 400, `{"error":"invalid_request"}`)
				return
			}
			writeJSON(w, 200, fmt.Sprintf(`{"access_token":%q,"token_type":"bearer","expires_in":%d,"sub":"x"}`, fakeToken(n), h.expiresIn.Load()))
			return
		}
		h.hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(h.srv.Close)
	cfg := Config{BaseURL: h.srv.URL, TokenURL: h.srv.URL + "/oauth/token", ClientID: NewSecret(fakeID()), ClientSecret: NewSecret(fakeSecret()),
		Timeout: 2 * time.Second, RatePerSecond: 1000, Burst: 1000}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := New(cfg, Options{
		Now: h.clock,
		Sleep: func(ctx context.Context, d time.Duration) error {
			h.mu.Lock()
			h.sleeps = append(h.sleeps, d)
			h.mu.Unlock()
			h.advance(d)
			return ctx.Err()
		},
		Jitter:    func(time.Duration) time.Duration { return 0 },
		AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.c = c
	return h
}

func (h *harness) secrets() []string {
	s := []string{fakeID(), fakeSecret()}
	for n := int64(1); n < 40; n++ {
		s = append(s, fakeToken(n))
	}
	return s
}

// track records an error so TestNoErrorLeaksSecrets can scan all of them.
func (h *harness) track(err error) error {
	if err != nil {
		h.errs = append(h.errs, err)
		out := fmt.Sprintf("%v %+v %#v", err, err, err)
		for _, s := range h.secrets() {
			if strings.Contains(out, s) {
				h.t.Fatalf("error leaks a secret: %v", err)
			}
		}
	}
	return err
}

func (h *harness) realSleeps() []time.Duration {
	var out []time.Duration
	for _, d := range h.sleeps {
		if d > 0 {
			out = append(out, d)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

// Synthetic fixtures in the (UNVERIFIED) Battle.net Classic Profile shape; see mapping.go.
const (
	fxSummary = `{"_links":{"self":{"href":"https://us.api.blizzard.com/profile/wow/character/fake-realm/alice"}},"id":1001,"name":"Alice",
"gender":{"type":"FEMALE","name":"Female"},"faction":{"type":"ALLIANCE","name":"Alliance"},"race":{"name":"Gnome","id":7},
"character_class":{"name":"Mage","id":8},"active_spec":{"name":"Frost","id":64},"realm":{"name":"Fake Realm","id":4242,"slug":"fake-realm"},
"guild":{"name":"Fake Guild","id":77,"realm":{"slug":"fake-realm"},"faction":{"type":"ALLIANCE","name":"Alliance"}},
"level":70,"last_login_timestamp":1700000000123,"average_item_level":120,"equipped_item_level":118}`
	fxEquipment = `{"_links":{"self":{"href":"x"}},"character":{"name":"Alice"},"equipped_items":[
{"item":{"id":101},"slot":{"type":"HEAD","name":"Head"},"quality":{"type":"EPIC","name":"Epic"},"name":"Synthetic Helm","level":{"value":115,"display_string":"Item Level 115"},"stats":[{"type":{"type":"INTELLECT"},"value":40}]},
{"item":{"id":102},"slot":{"type":"CHEST","name":"Chest"},"quality":{"type":"RARE","name":"Rare"},"name":"Synthetic Robe","level":{"value":100}}]}`
	fxSpecs = `{"_links":{"self":{"href":"x"}},"specialization_groups":[{"is_active":true,"specializations":[{"specialization":{"name":"Frost","id":64},"talents":[{"id":1,"rank":3}]}]}]}`
	fxStats = `{"_links":{"self":{"href":"x"}},"health":9000,"power":12000,"power_type":{"name":"Mana","id":0},"strength":{"base":10,"effective":12},"melee_crit":{"rating":5,"value":3.4}}`
	fxPvP   = `{"_links":{"self":{"href":"x"}},"honorable_kills":120,"brackets":[{"rating":1500}]}`
)

// bnet serves the fixtures for one character.
func bnet(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/equipment"):
			writeJSON(w, 200, fxEquipment)
		case strings.HasSuffix(p, "/specializations"):
			writeJSON(w, 200, fxSpecs)
		case strings.HasSuffix(p, "/statistics"):
			writeJSON(w, 200, fxStats)
		case strings.HasSuffix(p, "/pvp-summary"):
			writeJSON(w, 200, fxPvP)
		case strings.HasPrefix(p, "/profile/wow/character/"):
			writeJSON(w, 200, fxSummary)
		default:
			t.Errorf("unexpected path %q", p)
			writeJSON(w, 404, ``)
		}
	}
}

func TestSecretRedaction(t *testing.T) {
	s := NewSecret(fakeSecret())
	j, _ := json.Marshal(struct{ K Secret }{s})
	for _, out := range []string{
		fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s %q %x %d", s, s, s, s, s, s, s), string(j), s.String(), s.GoString(),
		fmt.Sprint(s.LogValue()), fmt.Sprintf("%v", &s), fmt.Sprint([]Secret{s}), fmt.Sprint(map[string]Secret{"a": s}),
	} {
		if strings.Contains(out, fakeSecret()) || !strings.Contains(out, "redacted") {
			t.Fatalf("secret not redacted: %q", out)
		}
	}
	if txt, _ := s.MarshalText(); string(txt) != "<redacted>" {
		t.Fatal("MarshalText leaks")
	}
	if s.Reveal() != fakeSecret() || !s.IsSet() || NewSecret("").IsSet() {
		t.Fatal("Reveal/IsSet wrong")
	}
}

func TestConfigStringAndFormatHideSecrets(t *testing.T) {
	cfg := Config{ClientID: NewSecret(fakeID()), ClientSecret: NewSecret(fakeSecret())}
	c, err := New(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{
		cfg.String(), fmt.Sprintf("%v|%+v|%#v|%s", cfg, cfg, cfg, cfg), fmt.Sprintf("%v|%+v", c.cfg, c.cfg), fmt.Sprintf("%+v", &cfg),
	} {
		if strings.Contains(out, fakeSecret()) || strings.Contains(out, fakeID()) {
			t.Fatalf("config printout leaks a secret: %s", out)
		}
	}
	if b, _ := json.Marshal(cfg); strings.Contains(string(b), fakeSecret()) || strings.Contains(string(b), fakeID()) {
		t.Fatal("config JSON leaks a secret")
	}
}

func TestConfigDefaults(t *testing.T) {
	c, err := New(Config{ClientID: NewSecret(fakeID()), ClientSecret: NewSecret(fakeSecret())}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.cfg
	if c.base != "https://us.api.blizzard.com" || cfg.Region != "us" || cfg.Namespace != "profile-classic-us" || cfg.Locale != "en_US" ||
		cfg.TokenURL != DefaultTokenURL || cfg.SummaryPath != DefaultSummaryPath || cfg.EquipmentPath != DefaultEquipmentPath ||
		cfg.SpecsPath != DefaultSpecsPath || cfg.StatsPath != DefaultStatsPath || cfg.PvPPath != DefaultPvPPath || cfg.RosterPath != DefaultRosterPath ||
		cfg.RosterNamespace != "profile-classic-us" || cfg.MaxRosterMembers != 1000 ||
		cfg.MaxResponseBytes != 1<<20 || cfg.MaxRetries != 3 || cfg.BackoffMax < cfg.BackoffMin || cfg.UserAgent == "" {
		t.Fatalf("defaults: %+v", cfg)
	}
	eu, err := New(Config{Region: "eu", ClientID: NewSecret(fakeID()), ClientSecret: NewSecret(fakeSecret())}, Options{})
	if err != nil || eu.base != "https://eu.api.blizzard.com" || eu.cfg.Namespace != "profile-classic-eu" {
		t.Fatalf("eu: %v %+v", err, eu)
	}
	custom, err := New(Config{Region: "eu", Namespace: "profile-classic1x-eu", RosterNamespace: "profile-x", ClientID: NewSecret(fakeID()), ClientSecret: NewSecret(fakeSecret())}, Options{})
	if err != nil || custom.cfg.Namespace != "profile-classic1x-eu" || custom.cfg.RosterNamespace != "profile-x" {
		t.Fatalf("custom: %v %+v", err, custom)
	}
	if DefaultNamespace("kr") != "profile-classic-kr" {
		t.Fatal("DefaultNamespace")
	}
}

func TestConfigValidation(t *testing.T) {
	good := Config{BaseURL: "https://armory.invalid", ClientID: NewSecret(fakeID()), ClientSecret: NewSecret(fakeSecret())}
	cases := map[string]func(*Config){
		"http":          func(c *Config) { c.BaseURL = "http://armory.invalid" },
		"no scheme":     func(c *Config) { c.BaseURL = "armory.invalid" },
		"ftp":           func(c *Config) { c.BaseURL = "ftp://armory.invalid" },
		"userinfo":      func(c *Config) { c.BaseURL = "https://u:p@armory.invalid" },
		"query":         func(c *Config) { c.BaseURL = "https://armory.invalid/?a=b" },
		"fragment":      func(c *Config) { c.BaseURL = "https://armory.invalid/#x" },
		"no host":       func(c *Config) { c.BaseURL = "https:///path" },
		"parse error":   func(c *Config) { c.BaseURL = "https://armory.invalid/%zz" },
		"no id":         func(c *Config) { c.ClientID = Secret{} },
		"no secret":     func(c *Config) { c.ClientSecret = Secret{} },
		"id newline":    func(c *Config) { c.ClientID = NewSecret("a\r\nX-Evil: 1") },
		"secret nl":     func(c *Config) { c.ClientSecret = NewSecret("a\nb") },
		"id colon":      func(c *Config) { c.ClientID = NewSecret("a:b") },
		"token http":    func(c *Config) { c.TokenURL = "http://oauth.invalid/token" },
		"token user":    func(c *Config) { c.TokenURL = "https://u:p@oauth.invalid/token" },
		"token query":   func(c *Config) { c.TokenURL = "https://oauth.invalid/token?x=1" },
		"token garbage": func(c *Config) { c.TokenURL = "::" },
		"region upper":  func(c *Config) { c.Region = "US" },
		"region slash":  func(c *Config) { c.Region = "u/s" },
		"region long":   func(c *Config) { c.Region = "abcdefgh" },
		"namespace sp":  func(c *Config) { c.Namespace = "profile classic" },
		"namespace amp": func(c *Config) { c.Namespace = "a&b=c" },
		"roster ns":     func(c *Config) { c.RosterNamespace = "a&b" },
		"locale":        func(c *Config) { c.Locale = "en-us" },
		"locale amp":    func(c *Config) { c.Locale = "en_US&x=1" },
		"summary slash": func(c *Config) { c.SummaryPath = "x/{realmSlug}/{characterName}" },
		"summary host":  func(c *Config) { c.SummaryPath = "//evil.invalid/{realmSlug}/{characterName}" },
		"summary query": func(c *Config) { c.SummaryPath = "/x/{realmSlug}/{characterName}?y=1" },
		"summary none":  func(c *Config) { c.SummaryPath = "/c" },
		"summary twice": func(c *Config) { c.SummaryPath = "/{realmSlug}/{characterName}/{characterName}" },
		"summary dots":  func(c *Config) { c.SummaryPath = "/a/../{realmSlug}/{characterName}" },
		"equip space":   func(c *Config) { c.EquipmentPath = "/c d/{realmSlug}/{characterName}" },
		"specs holder":  func(c *Config) { c.SpecsPath = "/{realmSlug}/{guildSlug}" },
		"stats none":    func(c *Config) { c.StatsPath = "/{characterName}" },
		"pvp none":      func(c *Config) { c.PvPPath = "/{realmSlug}" },
		"roster none":   func(c *Config) { c.RosterPath = "/roster" },
		"roster name":   func(c *Config) { c.RosterPath = "/{realmSlug}/{characterName}" },
		"members neg":   func(c *Config) { c.MaxRosterMembers = -1 },
		"members huge":  func(c *Config) { c.MaxRosterMembers = 1_000_000 },
	}
	for name, mutate := range cases {
		cfg := good
		mutate(&cfg)
		if _, err := New(cfg, Options{}); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: want ErrConfig, got %v", name, err)
		} else if strings.Contains(err.Error(), fakeSecret()) || strings.Contains(err.Error(), fakeID()) {
			t.Errorf("%s: error leaks a secret", name)
		}
	}
	cfg := good
	cfg.BaseURL = "https://armory.invalid/api/"
	c, err := New(cfg, Options{})
	if err != nil || c.base != "https://armory.invalid/api" {
		t.Fatalf("good config rejected or not normalised: %v", err)
	}
	// http is only accepted through the test option, for both the API and the token endpoint
	cfg.BaseURL = "http://127.0.0.1:1"
	cfg.TokenURL = "http://127.0.0.1:2/token"
	if _, err := New(cfg, Options{AllowHTTP: true}); err != nil {
		t.Fatalf("test option: %v", err)
	}
}

func TestRequestShapeAndTokenExchange(t *testing.T) {
	var paths []string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		q := r.URL.Query()
		if r.Method != http.MethodGet || q.Get("namespace") != "profile-classic-us" || q.Get("locale") != "en_US" || len(q) != 2 {
			t.Errorf("bad request %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer "+fakeToken(1) || r.Header.Get("User-Agent") != "radar-armory" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("bad headers %v", r.Header)
		}
		bnet(t)(w, r)
	}, nil)
	if _, err := h.c.Character(context.Background(), "Fake Realm", "Alice"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/profile/wow/character/fake-realm/alice", "/profile/wow/character/fake-realm/alice/equipment",
		"/profile/wow/character/fake-realm/alice/specializations", "/profile/wow/character/fake-realm/alice/statistics",
		"/profile/wow/character/fake-realm/alice/pvp-summary"}
	if fmt.Sprint(paths) != fmt.Sprint(want) {
		t.Fatalf("paths\n got %v\nwant %v", paths, want)
	}
	if h.tokenHits.Load() != 1 {
		t.Fatalf("token fetched %d times", h.tokenHits.Load())
	}
}

func TestCharacterSuccessMapping(t *testing.T) {
	h := newHarness(t, bnet(t), nil)
	c, err := h.c.Character(context.Background(), "Fake Realm", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "ok" || c.Level != 70 || c.Class != "Mage" || c.Race != "Gnome" || c.Faction != "Alliance" || c.Guild != "Fake Guild" ||
		c.Health != 9000 || c.PowerType != "Mana" || c.Power != 12000 || c.Realm != "Fake Realm" || c.Name != "Alice" {
		t.Fatalf("mapped: %+v", c)
	}
	if c.APIUpdatedAt == nil || *c.APIUpdatedAt != 1_700_000_000_123 || c.FetchedAt != h.clock().UnixMilli() {
		t.Fatalf("times: %v %d", c.APIUpdatedAt, c.FetchedAt)
	}
	var gear struct {
		Count int `json:"count"`
		Items []struct {
			Slot    string `json:"slot"`
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Quality string `json:"quality"`
			Level   int64  `json:"level"`
		} `json:"items"`
	}
	if err := json.Unmarshal(c.Gear, &gear); err != nil || gear.Count != 2 || len(gear.Items) != 2 ||
		gear.Items[0].Slot != "HEAD" || gear.Items[0].ID != 101 || gear.Items[0].Name != "Synthetic Helm" || gear.Items[0].Quality != "EPIC" || gear.Items[0].Level != 115 {
		t.Fatalf("gear: %s %v", c.Gear, err)
	}
	if strings.Contains(string(c.Gear), "INTELLECT") {
		t.Fatal("gear summary must not carry the raw item stats")
	}
	for name, blob := range map[string]json.RawMessage{"talents": c.Talents, "stats": c.Stats, "pvp": c.PvP} {
		if strings.Contains(string(blob), "_links") || strings.Contains(string(blob), `"character"`) {
			t.Errorf("%s keeps boilerplate: %s", name, blob)
		}
	}
	if !strings.Contains(string(c.Talents), "specialization_groups") || !strings.Contains(string(c.Stats), "melee_crit") || !strings.Contains(string(c.PvP), "honorable_kills") {
		t.Fatalf("blobs: %s %s %s", c.Talents, c.Stats, c.PvP)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c2, _ := h.c.Character(context.Background(), "Fake Realm", "Alice")
	if string(c2.Gear) != string(c.Gear) || string(c2.Talents) != string(c.Talents) || string(c2.Stats) != string(c.Stats) {
		t.Fatal("blobs are not deterministic")
	}
}

func TestCharacterMappingVariants(t *testing.T) {
	big := `{"x":"` + strings.Repeat("a", domain.ArmoryBlobMaxBytes) + `"}`
	cases := []struct {
		name, summary, stats string
		check                func(domain.ArmoryCharacter) bool
	}{
		{"locale name objects", `{"level":10,"character_class":{"name":{"en_US":"Warrior","de_DE":"Krieger"}},"race":{"en_US":"Orc"},"faction":{"type":"HORDE"},"guild":{"name":{"en_US":"Fake Guild"}}}`, ``,
			func(c domain.ArmoryCharacter) bool {
				return c.Class == "Warrior" && c.Race == "Orc" && c.Faction == "Horde" && c.Guild == "Fake Guild"
			}},
		{"plain strings", `{"level":10,"character_class":"Rogue","race":"Human","faction":"Alliance","guild":"G"}`, ``,
			func(c domain.ArmoryCharacter) bool {
				return c.Class == "Rogue" && c.Race == "Human" && c.Faction == "Alliance" && c.Guild == "G"
			}},
		{"minimal", `{}`, ``, func(c domain.ArmoryCharacter) bool {
			return c.Level == 0 && c.APIUpdatedAt == nil && c.Gear == nil && c.Class == "" && c.PowerType == ""
		}},
		{"hostile numbers clamped", `{"level":99999}`, `{"health":1e30,"power":-5}`, func(c domain.ArmoryCharacter) bool {
			return c.Level == domain.PingLevelMax && c.Health == domain.ArmoryValueMax && c.Power == 0
		}},
		{"wrong types ignored", `{"level":"high","character_class":5,"guild":7,"last_login_timestamp":"x"}`, `{"health":"x","power_type":5}`, func(c domain.ArmoryCharacter) bool {
			return c.Level == 0 && c.Class == "" && c.Guild == "" && c.Health == 0 && c.APIUpdatedAt == nil
		}},
		{"oversized stats dropped", `{"level":3}`, big, func(c domain.ArmoryCharacter) bool { return c.Stats == nil && c.Level == 3 }},
		{"login in the past ignored", `{"last_login_timestamp":-5}`, ``, func(c domain.ArmoryCharacter) bool { return c.APIUpdatedAt == nil }},
	}
	for _, tc := range cases {
		stats := tc.stats
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/statistics") && stats != "":
				writeJSON(w, 200, stats)
			case strings.HasSuffix(r.URL.Path, "/alice"):
				writeJSON(w, 200, tc.summary)
			default:
				writeJSON(w, 404, ``)
			}
		}, nil)
		c, err := h.c.Character(context.Background(), "Fake Realm", "Alice")
		if err != nil || !tc.check(c) {
			t.Errorf("%s: %+v %v", tc.name, c, err)
		}
	}
}

func TestSubresourceDegradation(t *testing.T) {
	for _, code := range []int{403, 404} {
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/alice") {
				writeJSON(w, 200, fxSummary)
				return
			}
			writeJSON(w, code, `{"code":1}`)
		}, nil)
		c, err := h.c.Character(context.Background(), "Fake Realm", "Alice")
		if err != nil || c.Status != "ok" || c.Class != "Mage" || c.Gear != nil || c.Talents != nil || c.Stats != nil || c.PvP != nil || c.Health != 0 || c.PowerType != "" {
			t.Errorf("%d: %+v %v", code, c, err)
		}
	}
	// garbage and non-object sub-resources are dropped, the character is kept
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/alice"):
			writeJSON(w, 200, fxSummary)
		case strings.HasSuffix(r.URL.Path, "/equipment"):
			writeJSON(w, 200, `not json`)
		case strings.HasSuffix(r.URL.Path, "/specializations"):
			writeJSON(w, 200, `[1,2]`)
		case strings.HasSuffix(r.URL.Path, "/statistics"):
			writeJSON(w, 200, `null`)
		default:
			writeJSON(w, 200, `"x"`)
		}
	}, nil)
	if c, err := h.c.Character(context.Background(), "Fake Realm", "Alice"); err != nil || c.Class != "Mage" || c.Gear != nil || c.Talents != nil || c.Stats != nil || c.PvP != nil {
		t.Fatalf("%+v %v", c, err)
	}
	// a server error on a sub-resource fails the character (retried first), it is not stored half-empty
	h = newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/equipment") {
			writeJSON(w, 500, ``)
			return
		}
		bnet(t)(w, r)
	}, func(c *Config) { c.MaxRetries = 1; c.BreakerThreshold = 100 })
	var se *StatusError
	if _, err := h.c.Character(context.Background(), "Fake Realm", "Alice"); !errors.As(h.track(err), &se) || se.Code != 500 {
		t.Fatalf("%v", err)
	}
	// 401 on a sub-resource after a refresh is an auth failure
	h = newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pvp-summary") {
			writeJSON(w, 401, ``)
			return
		}
		bnet(t)(w, r)
	}, nil)
	if _, err := h.c.Character(context.Background(), "Fake Realm", "Alice"); !errors.Is(h.track(err), ErrAuth) {
		t.Fatalf("%v", err)
	}
}

func TestCharacterBadDocuments(t *testing.T) {
	for name, body := range map[string]string{"garbage": `{{{`, "null": `null`, "array": `[1]`, "string": `"x"`, "empty": ``, "truncated": `{"level":7`} {
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, body) }, nil)
		_, err := h.c.Character(context.Background(), "Fake Realm", "Alice")
		if !errors.Is(h.track(err), ErrBadResponse) {
			t.Errorf("%s: want ErrBadResponse, got %v", name, err)
		}
	}
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{"character_class":"Ma\ngi"}`) }, nil)
	if c, err := h.c.Character(context.Background(), "Fake Realm", "Alice"); err != nil && !errors.Is(err, ErrBadResponse) {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestMapCharacterValidationFailure(t *testing.T) {
	if _, err := mapCharacter("R", "bad/name", parts{summary: []byte(`{}`)}, domain.MinTimestampMs); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("bad name: %v", err)
	}
	if _, err := mapCharacter("R", "Alice", parts{summary: []byte(`{}`)}, 5); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("bad time: %v", err)
	}
}

func TestCharacterStatuses(t *testing.T) {
	for code, want := range map[int]string{403: "hidden", 404: "notfound"} {
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, code, `{"error":"x"}`) }, nil)
		c, err := h.c.Character(context.Background(), "Fake Realm", "Alice")
		if err != nil || c.Status != want || c.Name != "Alice" || c.Realm != "Fake Realm" || c.Validate() != nil {
			t.Errorf("%d: %+v %v", code, c, err)
		}
		if h.hits.Load() != 1 {
			t.Errorf("%d must not be retried or followed by sub-requests (%d hits)", code, h.hits.Load())
		}
	}
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 418, ``) }, nil)
	var se *StatusError
	if _, err := h.c.Character(context.Background(), "Fake Realm", "Alice"); !errors.As(err, &se) || se.Code != 418 || se.Error() == "" {
		t.Fatalf("418: %v", err)
	}
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{"Area 52": "area-52", "Mal'Ganis": "malganis", "Mal’Ganis": "malganis", "Fake Realm": "fake-realm", "area-52": "area-52", "Testrealm": "testrealm", "Realm2": "realm2"} {
		if got, ok := RealmSlug(in); !ok || got != want {
			t.Errorf("RealmSlug(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "../x", "a/b", "a%2fb", "a?b", "Ünï", "-lead", "trail-", "a b\n", strings.Repeat("a", 80), "a@b", "x y/z", "a\x00b", " "} {
		if got, ok := RealmSlug(in); ok {
			t.Errorf("RealmSlug(%q) accepted as %q", in, got)
		}
	}
	for in, want := range map[string]string{"Fake Guild": "fake-guild", "Guild's Own": "guilds-own", "Élite Guard": "élite-guard"} {
		if got, ok := GuildSlug(in); !ok || got != want {
			t.Errorf("GuildSlug(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "a/b", "a?b", "a#b", "..", "a\x00b", "a‮b", strings.Repeat("g", 100), "a%2fb", "a;b", `a\b`, "-x"} {
		if got, ok := GuildSlug(in); ok {
			t.Errorf("GuildSlug(%q) accepted as %q", in, got)
		}
	}
}

func TestHostileNamesNeverReachTheNetwork(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) }, nil)
	bad := []string{"../x", "..", "a/b", `a\b`, "a@b", "%2f", "%2F..%2f", "a%00b", "a?b=c", "a#b", "a b\n", " lead", "trail ", "", "x\x00y",
		strings.Repeat("a", 60), strings.Repeat("é", 100), "a:b", "a;b", "<script>", "'; DROP TABLE x;--", "https://evil.invalid"}
	for _, n := range bad {
		if _, err := h.c.Character(context.Background(), "Fake Realm", n); !errors.Is(err, ErrBadName) {
			t.Errorf("name %q: want ErrBadName, got %v", n, err)
		}
		if _, err := h.c.Character(context.Background(), n, "Alice"); !errors.Is(err, ErrBadName) {
			t.Errorf("realm %q: want ErrBadName, got %v", n, err)
		}
		if _, err := h.c.GuildRoster(context.Background(), n, "Fake Guild"); !errors.Is(err, ErrBadName) {
			t.Errorf("roster realm %q: want ErrBadName, got %v", n, err)
		}
		if n != "" && !strings.ContainsAny(n, "é") && len(n) < 60 {
			if _, err := h.c.GuildRoster(context.Background(), "Fake Realm", n); !errors.Is(err, ErrBadName) {
				t.Errorf("guild %q: want ErrBadName, got %v", n, err)
			}
		}
	}
	// a realm that is a valid domain name but has no slug (non-ASCII)
	if _, err := h.c.Character(context.Background(), "Élan Realm", "Alice"); !errors.Is(err, ErrBadName) {
		t.Errorf("non-ASCII realm: %v", err)
	}
	if h.hits.Load() != 0 || h.tokenHits.Load() != 0 {
		t.Fatalf("hostile names reached the network (%d requests, %d token requests)", h.hits.Load(), h.tokenHits.Load())
	}
}

func TestNamesAreLowercasedAndEscaped(t *testing.T) {
	var seen []string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.EscapedPath()
		if !strings.HasSuffix(p, "/equipment") && !strings.HasSuffix(p, "/specializations") && !strings.HasSuffix(p, "/statistics") && !strings.HasSuffix(p, "/pvp-summary") {
			seen = append(seen, p)
		}
		writeJSON(w, 200, `{}`)
	}, nil)
	for _, n := range []string{"Élan", "O'Brien", "Two Words", "Jean-Luc"} {
		if _, err := h.c.Character(context.Background(), "Realm 2", n); err != nil {
			t.Fatalf("%q: %v", n, err)
		}
	}
	want := []string{"/profile/wow/character/realm-2/%C3%A9lan", "/profile/wow/character/realm-2/o%27brien",
		"/profile/wow/character/realm-2/two%20words", "/profile/wow/character/realm-2/jean-luc"}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("escaped paths:\n got %v\nwant %v", seen, want)
	}
}

func TestPathPrefixAndCustomTemplates(t *testing.T) {
	var paths []string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		writeJSON(w, 200, `{}`)
	}, func(c *Config) {
		c.BaseURL += "/api/"
		c.SummaryPath = "/c/{characterName}/in/{realmSlug}"
		c.EquipmentPath = "/e/{realmSlug}/{characterName}"
		c.SpecsPath = "/s/{realmSlug}/{characterName}"
		c.StatsPath = "/t/{realmSlug}/{characterName}"
		c.PvPPath = "/p/{realmSlug}/{characterName}"
		c.RosterPath = "/g/{guildSlug}@{realmSlug}"
	})
	if _, err := h.c.Character(context.Background(), "R", "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.GuildRoster(context.Background(), "R", "G"); err != nil {
		t.Fatal(err)
	}
	want := "/api/c/alice/in/r /api/e/r/alice /api/s/r/alice /api/t/r/alice /api/p/r/alice /api/g/g@r"
	if strings.Join(paths, " ") != want {
		t.Fatalf("paths %v", paths)
	}
}

func TestNamespaceAndLocaleSent(t *testing.T) {
	var got []string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Query().Get("namespace")+"|"+r.URL.Query().Get("locale"))
		writeJSON(w, 200, `{}`)
	}, func(c *Config) { c.Namespace = "profile-x-eu"; c.RosterNamespace = "profile-y-eu"; c.Locale = "de_DE" })
	_, _ = h.c.Character(context.Background(), "R", "Alice")
	_, _ = h.c.GuildRoster(context.Background(), "R", "G")
	if got[0] != "profile-x-eu|de_DE" || got[len(got)-1] != "profile-y-eu|de_DE" {
		t.Fatalf("%v", got)
	}
}

// ---- guild roster ----

const fxRoster = `{"_links":{},"guild":{"name":"Fake Guild"},"members":[
{"character":{"name":"Alice","id":1,"level":70,"realm":{"slug":"fake-realm","id":4242}},"rank":0},
{"character":{"name":"Bob","id":2,"realm":{"slug":"other-realm"}},"rank":1},
{"character":{"name":"../evil","realm":{"slug":"fake-realm"}},"rank":1},
{"character":{"name":"Carol","realm":{"slug":"bad/realm"}},"rank":1},
{"character":{"name":"Dave"},"rank":2},
{"rank":3},
{"character":"x"}]}`

func TestGuildRoster(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/data/wow/guild/fake-realm/fake-guild/roster" || r.URL.Query().Get("namespace") != "profile-classic-us" {
			t.Errorf("request %s", r.URL)
		}
		writeJSON(w, 200, fxRoster)
	}, nil)
	refs, err := h.c.GuildRoster(context.Background(), "Fake Realm", "Fake Guild")
	if err != nil {
		t.Fatal(err)
	}
	want := []CharRef{{"fake-realm", "Alice"}, {"other-realm", "Bob"}, {"fake-realm", "Dave"}}
	if fmt.Sprint(refs) != fmt.Sprint(want) {
		t.Fatalf("members %v, want %v", refs, want)
	}
}

func TestGuildRosterStatusesAndLimits(t *testing.T) {
	for _, code := range []int{404, 403} {
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, code, ``) }, nil)
		refs, err := h.c.GuildRoster(context.Background(), "R", "G")
		if err != nil || len(refs) != 0 || h.hits.Load() != 1 {
			t.Errorf("%d: %v %v hits=%d", code, refs, err, h.hits.Load())
		}
	}
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 401, ``) }, nil)
	if _, err := h.c.GuildRoster(context.Background(), "R", "G"); !errors.Is(h.track(err), ErrAuth) {
		t.Fatalf("401: %v", err)
	}
	h = newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 418, ``) }, nil)
	var se *StatusError
	if _, err := h.c.GuildRoster(context.Background(), "R", "G"); !errors.As(err, &se) {
		t.Fatalf("418: %v", err)
	}
	for _, body := range []string{`{{`, `null`, `[1]`, ``} {
		h = newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, body) }, nil)
		if _, err := h.c.GuildRoster(context.Background(), "R", "G"); !errors.Is(h.track(err), ErrBadResponse) {
			t.Errorf("body %q: %v", body, err)
		}
	}
	var b strings.Builder
	b.WriteString(`{"members":[`)
	for i := 0; i < 50; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"character":{"name":"Member%c","realm":{"slug":"r"}}}`, 'a'+rune(i%26))
	}
	b.WriteString(`]}`)
	h = newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, b.String()) }, func(c *Config) { c.MaxRosterMembers = 7 })
	if refs, err := h.c.GuildRoster(context.Background(), "R", "G"); err != nil || len(refs) != 7 {
		t.Fatalf("cap: %d %v", len(refs), err)
	}
	h = newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, b.String()) }, func(c *Config) { c.MaxResponseBytes = 100 })
	if _, err := h.c.GuildRoster(context.Background(), "R", "G"); !errors.Is(h.track(err), ErrTooLarge) {
		t.Fatalf("size: %v", err)
	}
}

// ---- token source ----

func TestTokenIsCachedAndRefreshedProactively(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) }, nil)
	h.expiresIn.Store(100)
	ctx := context.Background()
	for range 3 {
		if _, err := h.c.Character(ctx, "R", "Alice"); err != nil {
			t.Fatal(err)
		}
	}
	if h.tokenHits.Load() != 1 {
		t.Fatalf("token fetched %d times for 3 characters", h.tokenHits.Load())
	}
	h.advance(89 * time.Second) // before 90% of expires_in
	if _, err := h.c.Character(ctx, "R", "Alice"); err != nil || h.tokenHits.Load() != 1 {
		t.Fatalf("refreshed too early: %v hits=%d", err, h.tokenHits.Load())
	}
	h.advance(2 * time.Second) // 91s: past 90%
	if _, err := h.c.Character(ctx, "R", "Alice"); err != nil || h.tokenHits.Load() != 2 {
		t.Fatalf("not refreshed at 90%%: %v hits=%d", err, h.tokenHits.Load())
	}
}

func TestTokenUsedInBearerHeaderAfterRefresh(t *testing.T) {
	var auths []string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		if len(auths) == 1 {
			writeJSON(w, 401, `{"error":"expired"}`)
			return
		}
		writeJSON(w, 200, `{}`)
	}, nil)
	if _, err := h.c.Character(context.Background(), "R", "Alice"); err != nil {
		t.Fatal(err)
	}
	if h.tokenHits.Load() != 2 || auths[0] != "Bearer "+fakeToken(1) || auths[1] != "Bearer "+fakeToken(2) {
		t.Fatalf("401 must refresh once and retry: tokens=%d auths=%v", h.tokenHits.Load(), auths)
	}
}

func TestSecond401IsAuthError(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 401, ``) }, nil)
	if _, err := h.c.Character(context.Background(), "R", "Alice"); !errors.Is(h.track(err), ErrAuth) {
		t.Fatalf("%v", err)
	}
	if h.hits.Load() != 2 || h.tokenHits.Load() != 2 {
		t.Fatalf("one refresh and one retry only: hits=%d tokens=%d", h.hits.Load(), h.tokenHits.Load())
	}
}

func TestTokenEndpointFailures(t *testing.T) {
	type tokFn = func(w http.ResponseWriter, r *http.Request, n int64)
	reply := func(code int, body string) tokFn {
		return func(w http.ResponseWriter, _ *http.Request, _ int64) { writeJSON(w, code, body) }
	}
	cases := []struct {
		name string
		fn   tokFn
		want error
	}{
		{"401", reply(401, `{"error":"invalid_client"}`), ErrAuth},
		{"400", reply(400, `{"error":"invalid_request"}`), ErrAuth},
		{"garbage", reply(200, `<html>`), ErrBadResponse},
		{"no token", reply(200, `{"expires_in":10}`), ErrBadResponse},
		{"token with space", reply(200, `{"access_token":"a b","expires_in":10}`), ErrBadResponse},
		{"token with newline", reply(200, `{"access_token":"a\r\nX: y","expires_in":10}`), ErrBadResponse},
		{"huge token", reply(200, `{"access_token":"`+strings.Repeat("a", 5000)+`","expires_in":10}`), ErrBadResponse},
		{"mac type", reply(200, `{"access_token":"abc","token_type":"mac","expires_in":10}`), ErrBadResponse},
		{"418", reply(418, ``), nil},
	}
	for _, tc := range cases {
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) }, nil)
		h.tokenFn = tc.fn
		_, err := h.c.Character(context.Background(), "R", "Alice")
		h.track(err)
		var se *StatusError
		if tc.want == nil {
			if !errors.As(err, &se) || se.Code != 418 {
				t.Errorf("%s: %v", tc.name, err)
			}
		} else if !errors.Is(err, tc.want) {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, err)
		}
		if h.hits.Load() != 0 {
			t.Errorf("%s: an API request was made without a token", tc.name)
		}
	}
}

func TestTokenExpiryDefaults(t *testing.T) {
	for _, body := range []string{`{"access_token":"abc"}`, `{"access_token":"abc","expires_in":0}`, `{"access_token":"abc","expires_in":-5}`, `{"access_token":"abc","expires_in":"x"}`} {
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) }, nil)
		h.tokenFn = func(w http.ResponseWriter, _ *http.Request, _ int64) { writeJSON(w, 200, body) }
		if _, err := h.c.Character(context.Background(), "R", "Alice"); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		h.advance(10 * time.Minute) // a missing lifetime must not mean "never expires"
		if _, err := h.c.Character(context.Background(), "R", "Alice"); err != nil || h.tokenHits.Load() < 2 {
			t.Errorf("%s: no refresh after the default lifetime (tokens=%d)", body, h.tokenHits.Load())
		}
	}
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) }, nil)
	h.expiresIn.Store(1 << 40)
	_, _ = h.c.Character(context.Background(), "R", "Alice")
	h.advance(8 * 24 * time.Hour)
	_, _ = h.c.Character(context.Background(), "R", "Alice")
	if h.tokenHits.Load() != 2 {
		t.Fatalf("lifetime not capped: %d", h.tokenHits.Load())
	}
}

func TestTokenSingleFlight(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) }, nil)
	h.tokenFn = func(w http.ResponseWriter, r *http.Request, n int64) {
		time.Sleep(50 * time.Millisecond)
		writeJSON(w, 200, fmt.Sprintf(`{"access_token":%q,"expires_in":86399}`, fakeToken(n)))
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.c.Character(context.Background(), "R", "Alice"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if h.tokenHits.Load() != 1 {
		t.Fatalf("%d token requests for 20 concurrent calls, want 1", h.tokenHits.Load())
	}
}

func TestTokenIsNotInvalidatedByStaleCaller(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) }, nil)
	ctx := context.Background()
	old, _ := h.c.ts.Token(ctx)
	h.c.ts.Invalidate(old)
	fresh, _ := h.c.ts.Token(ctx)
	h.c.ts.Invalidate(old) // a slow caller still holding the old token must not drop the fresh one
	again, _ := h.c.ts.Token(ctx)
	if fresh.Reveal() != again.Reveal() || h.tokenHits.Load() != 2 {
		t.Fatalf("stale invalidation dropped the fresh token (%d token requests)", h.tokenHits.Load())
	}
	if fmt.Sprint(fresh) != "<redacted>" {
		t.Fatal("token must be a redacting Secret")
	}
}

func TestTokenRequestsAreNotRedirected(t *testing.T) {
	var target atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("client credentials forwarded to the redirect target")
		}
		writeJSON(w, 200, `{"access_token":"abc","expires_in":100}`)
	}))
	defer other.Close()
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) }, nil)
	h.tokenFn = func(w http.ResponseWriter, r *http.Request, _ int64) { http.Redirect(w, r, other.URL+"/steal", 307) }
	if _, err := h.c.Character(context.Background(), "R", "Alice"); !errors.Is(h.track(err), ErrRedirect) {
		t.Fatalf("%v", err)
	}
	if target.Load() != 0 {
		t.Fatal("token redirect was followed")
	}
}

func TestTokenEndpointRetriesServerErrors(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) }, nil)
	h.tokenFn = func(w http.ResponseWriter, r *http.Request, n int64) {
		if n <= 2 {
			writeJSON(w, 503, ``)
			return
		}
		writeJSON(w, 200, fmt.Sprintf(`{"access_token":%q,"expires_in":86399}`, fakeToken(n)))
	}
	if _, err := h.c.Character(context.Background(), "R", "Alice"); err != nil || h.tokenHits.Load() != 3 {
		t.Fatalf("%v tokens=%d", err, h.tokenHits.Load())
	}
}

// ---- transport (shape-agnostic) ----

func TestRetryAfterHonoured(t *testing.T) {
	var n atomic.Int64
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			writeJSON(w, 429, ``)
			return
		}
		writeJSON(w, 200, `{"level":5}`)
	}, nil)
	if _, err := h.c.Character(context.Background(), "R", "Alice"); err != nil {
		t.Fatal(err)
	}
	if got := h.realSleeps(); len(got) != 1 || got[0] != 7*time.Second {
		t.Fatalf("sleeps %v", got)
	}
}

func TestRetryAfterDateAndCap(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if d := parseRetryAfter(now.Add(90*time.Second).UTC().Format(http.TimeFormat), now); d != 90*time.Second {
		t.Fatalf("date: %v", d)
	}
	for _, v := range []string{"", "soon", "-5", now.Add(-time.Hour).UTC().Format(http.TimeFormat)} {
		if d := parseRetryAfter(v, now); d != 0 {
			t.Fatalf("%q -> %v", v, d)
		}
	}
	var n atomic.Int64
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "86400")
			writeJSON(w, 429, ``)
			return
		}
		writeJSON(w, 200, `{}`)
	}, func(c *Config) { c.BackoffMax = 3 * time.Second })
	if _, err := h.c.Character(context.Background(), "R", "Alice"); err != nil {
		t.Fatal(err)
	}
	if got := h.realSleeps(); len(got) != 1 || got[0] != 3*time.Second {
		t.Fatalf("Retry-After not capped: %v", got)
	}
}

func TestServerErrorsThenSuccessExponentialBackoff(t *testing.T) {
	var n atomic.Int64
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 3 {
			writeJSON(w, 503, ``)
			return
		}
		writeJSON(w, 200, `{}`)
	}, func(c *Config) { c.BackoffMin = time.Second; c.BackoffMax = time.Hour; c.BreakerThreshold = 10 })
	c, err := h.c.Character(context.Background(), "R", "Alice")
	if err != nil || c.Status != "ok" {
		t.Fatalf("%+v %v", c, err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if got := h.realSleeps(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("backoff %v, want %v", got, want)
	}
}

func TestJitterIsAdded(t *testing.T) {
	var n atomic.Int64
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			writeJSON(w, 500, ``)
			return
		}
		writeJSON(w, 200, `{}`)
	}, func(c *Config) { c.BackoffMin = 2 * time.Second })
	h.c.jitter = func(limit time.Duration) time.Duration { return limit }
	if _, err := h.c.Character(context.Background(), "R", "Alice"); err != nil {
		t.Fatal(err)
	}
	if got := h.realSleeps(); len(got) != 1 || got[0] != 3*time.Second {
		t.Fatalf("jittered sleep %v", got)
	}
	if d := randomJitter(0); d != 0 {
		t.Fatal(d)
	}
	for range 50 {
		if d := randomJitter(time.Second); d < 0 || d > time.Second {
			t.Fatal(d)
		}
	}
}

func TestRetriesExhausted(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 502, ``) }, func(c *Config) {
		c.MaxRetries = 2
		c.BreakerThreshold = 100
	})
	_, err := h.c.Character(context.Background(), "R", "Alice")
	var se *StatusError
	if !errors.As(h.track(err), &se) || se.Code != 502 || h.hits.Load() != 3 {
		t.Fatalf("%v hits=%d", err, h.hits.Load())
	}
	h = newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 500, ``) }, func(c *Config) { c.MaxRetries = -1 })
	if _, err := h.c.Character(context.Background(), "R", "Alice"); err == nil || h.hits.Load() != 1 {
		t.Fatalf("hits=%d %v", h.hits.Load(), err)
	}
}

func TestCircuitBreakerLifecycle(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			writeJSON(w, 500, ``)
			return
		}
		writeJSON(w, 200, `{}`)
	}, func(c *Config) {
		c.MaxRetries = -1
		c.BreakerThreshold = 2
		c.BreakerCooldown = time.Minute
	})
	ctx := context.Background()
	for range 2 {
		_, _ = h.c.Character(ctx, "R", "Alice")
	}
	before := h.hits.Load()
	if _, err := h.c.Character(ctx, "R", "Alice"); !errors.Is(err, ErrCircuitOpen) || h.hits.Load() != before {
		t.Fatalf("open breaker must fail fast: %v", err)
	}
	if _, err := h.c.GuildRoster(ctx, "R", "G"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("roster: %v", err)
	}
	h.advance(61 * time.Second)
	if _, err := h.c.Character(ctx, "R", "Alice"); errors.Is(err, ErrCircuitOpen) || h.hits.Load() != before+1 {
		t.Fatalf("probe not sent: %v", err)
	}
	if _, err := h.c.Character(ctx, "R", "Alice"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("must reopen after failed probe: %v", err)
	}
	failing.Store(false)
	h.advance(61 * time.Second)
	if _, err := h.c.Character(ctx, "R", "Alice"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if _, err := h.c.Character(ctx, "R", "Alice"); err != nil {
		t.Fatalf("closed breaker: %v", err)
	}
}

func TestBreakerOnlyOneProbeAndMidRetryOpen(t *testing.T) {
	b := newBreaker(1, time.Minute)
	now := time.Unix(1000, 0)
	b.failure(now)
	if b.allow(now) || !b.isOpen(now) {
		t.Fatal("should be open")
	}
	later := now.Add(2 * time.Minute)
	if !b.allow(later) || b.allow(later) || !b.isOpen(later) {
		t.Fatal("exactly one probe while half-open")
	}
	b.abort()
	if !b.allow(later) {
		t.Fatal("abort must free the probe")
	}
	b.success()
	if b.isOpen(later) || !b.allow(later) {
		t.Fatal("success must close")
	}
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 500, ``) }, func(c *Config) {
		c.MaxRetries = 5
		c.BreakerThreshold = 2
	})
	_, err := h.c.Character(context.Background(), "R", "Alice")
	var se *StatusError
	if !errors.Is(err, ErrCircuitOpen) || !errors.As(err, &se) || h.hits.Load() != 2 {
		t.Fatalf("%v hits=%d", err, h.hits.Load())
	}
}

func TestSuccessResetsConsecutiveFailures(t *testing.T) {
	var n atomic.Int64
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%2 == 1 {
			writeJSON(w, 500, ``)
			return
		}
		writeJSON(w, 200, `{}`)
	}, func(c *Config) { c.BreakerThreshold = 2 })
	for range 3 {
		if _, err := h.c.GuildRoster(context.Background(), "R", "G"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLimiterThrottles(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) }, func(c *Config) {
		c.RatePerSecond = 2
		c.Burst = 2
	})
	h.c.limiter = newLimiter(2, 2, h.clock())
	for range 4 {
		if _, err := h.c.GuildRoster(context.Background(), "R", "G"); err != nil {
			t.Fatal(err)
		}
	}
	got := h.realSleeps()
	if len(got) < 2 || got[0] != 500*time.Millisecond {
		t.Fatalf("limiter sleeps %v", got)
	}
	l := newLimiter(10, 1, time.Unix(0, 0))
	if l.reserve(time.Unix(0, 0)) != 0 || l.reserve(time.Unix(0, 0)) != 100*time.Millisecond {
		t.Fatal("bucket math")
	}
	if d := l.reserve(time.Unix(10, 0)); d != 0 {
		t.Fatalf("refill: %v", d)
	}
}

func TestRedirectIsRefused(t *testing.T) {
	var target atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("token forwarded to the redirect target")
		}
		writeJSON(w, 200, `{}`)
	}))
	defer other.Close()
	for _, code := range []int{301, 302, 307, 308} {
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL+"/x", code) }, nil)
		if _, err := h.c.Character(context.Background(), "R", "Alice"); !errors.Is(h.track(err), ErrRedirect) || h.hits.Load() != 1 {
			t.Fatalf("%d: %v hits=%d", code, err, h.hits.Load())
		}
		if _, err := h.c.GuildRoster(context.Background(), "R", "G"); !errors.Is(err, ErrRedirect) {
			t.Fatalf("roster %d: %v", code, err)
		}
	}
	if target.Load() != 0 {
		t.Fatal("redirect target was contacted")
	}
}

func TestHugeBodyRefused(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"content-length": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(5000))
			w.WriteHeader(200)
			_, _ = w.Write([]byte(strings.Repeat("a", 5000)))
		},
		"chunked": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			for range 10 {
				_, _ = w.Write([]byte(strings.Repeat("a", 500)))
				w.(http.Flusher).Flush()
			}
		},
	} {
		h := newHarness(t, handler, func(c *Config) { c.MaxResponseBytes = 1024 })
		_, err := h.c.Character(context.Background(), "R", "Alice")
		if !errors.Is(h.track(err), ErrTooLarge) || h.hits.Load() != 1 {
			t.Errorf("%s: %v hits=%d", name, err, h.hits.Load())
		}
	}
	// exactly at the cap is accepted (the token answer is small, the summary is padded to the cap)
	exact := `{"level":5,"pad":"` + strings.Repeat("x", 400-len(`{"level":5,"pad":""}`)) + `"}`
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, exact) }, func(c *Config) { c.MaxResponseBytes = 400 })
	if _, err := h.c.Character(context.Background(), "R", "Alice"); err != nil {
		t.Fatal(err)
	}
}

func TestSlowServerTimesOut(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}, func(c *Config) { c.Timeout = 50 * time.Millisecond; c.MaxRetries = 1 })
	defer close(release)
	start := time.Now()
	_, err := h.c.Character(context.Background(), "R", "Alice")
	if h.track(err) == nil || time.Since(start) > 5*time.Second || h.hits.Load() != 2 {
		t.Fatalf("timeout not enforced/retried: %v hits=%d", err, h.hits.Load())
	}
}

func TestContextCancelStopsAndSleepCtx(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 500, ``) }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.c.Character(ctx, "R", "Alice"); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := sleepCtx(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	h2 := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 500, ``) }, nil)
	h2.c.sleep = func(ctx context.Context, d time.Duration) error {
		if d > 0 {
			cancel2()
			return ctx.Err()
		}
		return nil
	}
	if _, err := h2.c.Character(ctx2, "R", "Alice"); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	h3 := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) }, nil)
	h3.c.sleep = func(context.Context, time.Duration) error { return errors.New("sleep failed") }
	if _, err := h3.c.Character(context.Background(), "R", "Alice"); err == nil || h3.hits.Load() != 0 {
		t.Fatalf("%v", err)
	}
}

func TestConnectionDroppedIsRetried(t *testing.T) {
	var n atomic.Int64
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		writeJSON(w, 200, `{}`)
	}, nil)
	if _, err := h.c.Character(context.Background(), "R", "Alice"); err != nil || h.hits.Load() < 2 {
		t.Fatalf("%v hits=%d", err, h.hits.Load())
	}
	h = newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	}, func(c *Config) { c.MaxRetries = 1 })
	if _, err := h.c.Character(context.Background(), "R", "Alice"); h.track(err) == nil {
		t.Fatal("expected error")
	}
}

func TestScrubRemovesSecretsKeepsCause(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) }, nil)
	if _, err := h.c.Character(context.Background(), "R", "Alice"); err != nil { // learns a token
		t.Fatal(err)
	}
	cause := context.DeadlineExceeded
	err := fmt.Errorf("boom %s %s %s: %w", fakeSecret(), fakeID(), fakeToken(1), cause)
	got := h.c.scrub(err)
	for _, s := range []string{fakeSecret(), fakeID(), fakeToken(1)} {
		if strings.Contains(got.Error(), s) {
			t.Fatalf("not scrubbed: %v", got)
		}
	}
	if !strings.Contains(got.Error(), "<redacted>") {
		t.Fatalf("no marker: %v", got)
	}
	var s *scrubbed
	if !errors.As(got, &s) || s.Unwrap() == nil {
		t.Fatal("cause lost")
	}
	if h.c.scrub(nil) != nil || h.c.scrub(cause) != cause {
		t.Fatal("clean errors must pass through")
	}
}

func TestTLSIsVerifiedByDefault(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			writeJSON(w, 200, `{"access_token":"abc","expires_in":100}`)
			return
		}
		writeJSON(w, 200, `{}`)
	}))
	defer srv.Close()
	cfg := Config{BaseURL: srv.URL, TokenURL: srv.URL + "/oauth/token", ClientID: NewSecret(fakeID()), ClientSecret: NewSecret(fakeSecret()), MaxRetries: -1}
	c, err := New(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Character(context.Background(), "R", "Alice")
	if err == nil || strings.Contains(err.Error(), fakeSecret()) || strings.Contains(err.Error(), fakeID()) {
		t.Fatalf("self-signed certificate must be rejected without leaking: %v", err)
	}
	c, err = New(cfg, Options{Transport: srv.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Character(context.Background(), "R", "Alice"); err != nil {
		t.Fatal(err)
	}
}

func TestNoErrorLeaksSecrets(t *testing.T) {
	// an echo server that reflects the presented credentials in every body and header, including error pages
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", r.Header.Get("Authorization"))
		writeJSON(w, 500, r.Header.Get("Authorization"))
	}, func(c *Config) { c.MaxRetries = 1 })
	_, err := h.c.Character(context.Background(), "R", "Alice")
	h.track(err)
	_, err = h.c.GuildRoster(context.Background(), "R", "G")
	h.track(err)
	// and a token endpoint that echoes the Basic header
	h2 := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, func(c *Config) { c.MaxRetries = 1 })
	h2.tokenFn = func(w http.ResponseWriter, r *http.Request, _ int64) {
		writeJSON(w, 500, r.Header.Get("Authorization"))
	}
	_, err = h2.c.Character(context.Background(), "R", "Alice")
	h2.track(err)
	if len(h.errs) != 2 || len(h2.errs) != 1 {
		t.Fatalf("tracked %d/%d", len(h.errs), len(h2.errs))
	}
	var _ Source = h.c
}

// TestTokenRequestMatchesReferenceCurl pins the token exchange to the request verified against the real
// Battle.net token endpoint (docs/armory.md, "Reference OAuth request"):
//
//	curl -u CLIENT_ID:CLIENT_SECRET -d grant_type=client_credentials https://oauth.battle.net/token
//
// i.e. POST, HTTP Basic client_id:client_secret, form body exactly grant_type=client_credentials, form
// content type, no query string, and the credentials nowhere but the Authorization header.
func TestTokenRequestMatchesReferenceCurl(t *testing.T) {
	h := newHarness(t, bnet(t), nil)
	var seen int
	h.tokenFn = func(w http.ResponseWriter, r *http.Request, n int64) {
		seen++
		body, _ := io.ReadAll(r.Body)
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(fakeID()+":"+fakeSecret()))
		switch {
		case r.Method != http.MethodPost:
			t.Errorf("method %s, want POST", r.Method)
		case r.URL.RawQuery != "":
			t.Errorf("token request has a query string")
		case r.Header.Get("Authorization") != wantAuth:
			t.Errorf("Authorization is not HTTP Basic client_id:client_secret")
		case r.Header.Get("Content-Type") != "application/x-www-form-urlencoded":
			t.Errorf("Content-Type %q", r.Header.Get("Content-Type"))
		case string(body) != "grant_type=client_credentials":
			t.Errorf("body %q, want exactly grant_type=client_credentials", body)
		}
		for _, s := range []string{fakeID(), fakeSecret()} {
			if strings.Contains(string(body), s) || strings.Contains(r.URL.String(), s) {
				t.Errorf("a credential leaked outside the Authorization header")
			}
		}
		writeJSON(w, 200, fmt.Sprintf(`{"access_token":%q,"token_type":"bearer","expires_in":86399}`, fakeToken(n)))
	}
	if _, err := h.c.Character(context.Background(), "Fake Realm", "Alice"); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("token endpoint hit %d times, want 1", seen)
	}
}

// TestAuthErrorsNameTheRefusingStep keeps the two auth refusals apart in the run's log line: the token
// endpoint refusing the client credentials vs. the profile API refusing a freshly issued token.
func TestAuthErrorsNameTheRefusingStep(t *testing.T) {
	h := newHarness(t, bnet(t), nil)
	h.tokenFn = func(w http.ResponseWriter, _ *http.Request, _ int64) {
		writeJSON(w, 401, `{"error":"unauthorized","error_description":"Bad credentials"}`)
	}
	_, err := h.c.Character(context.Background(), "Fake Realm", "Alice")
	if !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "token endpoint answered 401") {
		t.Fatalf("token refusal: %v", err)
	}
	h2 := newHarness(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 401, `{}`) }, nil)
	_, err = h2.c.Character(context.Background(), "Fake Realm", "Alice")
	if !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "profile API answered 401 twice") {
		t.Fatalf("API refusal: %v", err)
	}
	for _, s := range append(h.secrets(), h2.secrets()...) {
		if strings.Contains(fmt.Sprint(err), s) {
			t.Fatal("auth error leaks a credential")
		}
	}
}
