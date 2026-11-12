package readapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
	"radarlive/internal/mod/readapi"
)

// armoryRig hosts readapi on a real memstore seeded with synthetic characters.
type armoryRig struct {
	host *core.Host
	h    http.Handler
}

func newArmoryRig(t *testing.T) *armoryRig {
	t.Helper()
	st := &fakeArmory{fakeStore: &fakeStore{}, chars: map[string]domain.ArmoryCharacter{}}
	for _, n := range []string{"Ann", "Bob", "Cy"} {
		st.chars[akey("Testrealm", n)] = domain.ArmoryCharacter{Realm: "Testrealm", Name: n, Class: "Mage", Race: "Human",
			Faction: "Alliance", Level: 60, Health: 4000, PowerType: "mana", Power: 3000,
			Gear: json.RawMessage(`{"head":{"id":1}}`), FetchedAt: t0.UnixMilli(), Status: domain.ArmoryStatusOK}
	}
	host := core.NewHost()
	if err := host.Stores.Add("mem", st); err != nil {
		t.Fatal(err)
	}
	if err := host.Register(readapi.NewModule(readapi.WithClock(func() time.Time { return t0 }))); err != nil {
		t.Fatal(err)
	}
	if err := host.Config.Load(nil, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	return &armoryRig{host: host, h: host.Routes.Handler()}
}

func (r *armoryRig) get(target string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

func TestArmoryCharacterFound(t *testing.T) {
	r := newArmoryRig(t)
	rec := r.get("/api/v1/armory/character?realm=TESTREALM&name=ann")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var c domain.ArmoryCharacter
	decode(t, rec, &c)
	if c.Name != "Ann" || c.Class != "Mage" || c.Level != 60 || string(c.Gear) != `{"head":{"id":1}}` {
		t.Fatalf("character = %+v", c)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestArmoryCharacterNotFoundAndBadInput(t *testing.T) {
	r := newArmoryRig(t)
	if rec := r.get("/api/v1/armory/character?realm=Testrealm&name=Nobody"); rec.Code != 404 || errCode(t, rec) != "not_found" {
		t.Fatalf("unknown: %d %s", rec.Code, rec.Body)
	}
	bad := map[string]string{
		"no params":     "/api/v1/armory/character",
		"no name":       "/api/v1/armory/character?realm=Testrealm",
		"no realm":      "/api/v1/armory/character?name=Ann",
		"empty":         "/api/v1/armory/character?realm=&name=",
		"extra param":   "/api/v1/armory/character?realm=Testrealm&name=Ann&x=1",
		"duplicate key": "/api/v1/armory/character?realm=Testrealm&name=Ann&name=Bob",
		"long name":     "/api/v1/armory/character?realm=Testrealm&name=" + strings.Repeat("a", 500),
		"long realm":    "/api/v1/armory/character?name=Ann&realm=" + strings.Repeat("a", 500),
		"control":       "/api/v1/armory/character?realm=Testrealm&name=" + url.QueryEscape("An\x00n"),
		"bidi":          "/api/v1/armory/character?realm=Testrealm&name=" + url.QueryEscape("An‮n"),
		"bad utf8":      "/api/v1/armory/character?realm=Testrealm&name=%ff%fe",
	}
	for name, target := range bad {
		t.Run(name, func(t *testing.T) {
			rec := r.get(target)
			if rec.Code != 400 || errCode(t, rec) != "invalid_request" {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestArmoryCharacterHostileStringsNeverReflected(t *testing.T) {
	r := newArmoryRig(t)
	for _, name := range []string{"Ann'; DROP TABLE armory_characters;--", "<script>alert(1)</script>", "../../etc/passwd", "Ann%00", "' OR '1'='1"} {
		rec := r.get("/api/v1/armory/character?realm=Testrealm&name=" + url.QueryEscape(name))
		if rec.Code != 400 && rec.Code != 404 {
			t.Fatalf("%q: status %d", name, rec.Code)
		}
		for _, leak := range []string{"DROP", "script", "passwd", "OR '"} {
			if strings.Contains(rec.Body.String(), leak) {
				t.Fatalf("%q reflected in %s", name, rec.Body)
			}
		}
	}
	if rec := r.get("/api/v1/armory/character?name=Ann&realm=" + url.QueryEscape("x' OR 1=1--")); rec.Code != 404 && rec.Code != 400 {
		t.Fatalf("realm: %d", rec.Code)
	}
	if rec := r.get("/api/v1/armory/character?realm=Testrealm&name=Ann"); rec.Code != 200 {
		t.Fatalf("store damaged: %d", rec.Code)
	}
}

func TestArmoryCharactersList(t *testing.T) {
	r := newArmoryRig(t)
	rec := r.get("/api/v1/armory/characters?realm=Testrealm&names=Cy,Nobody,Ann")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Characters []domain.ArmoryCharacter `json:"characters"`
	}
	decode(t, rec, &out)
	if len(out.Characters) != 2 || out.Characters[0].Name != "Cy" || out.Characters[1].Name != "Ann" {
		t.Fatalf("characters = %+v", out.Characters)
	}
	rec = r.get("/api/v1/armory/characters?realm=Testrealm&names=Nobody")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"characters":[]`) {
		t.Fatalf("all unknown: %d %s", rec.Code, rec.Body)
	}
}

func TestArmoryCharactersLimitsAndBadInput(t *testing.T) {
	r := newArmoryRig(t)
	names := func(n int) string {
		s := make([]string, n)
		for i := range s {
			s[i] = fmt.Sprintf("N%c%c", 'a'+i%26, 'a'+i/26)
		}
		return strings.Join(s, ",")
	}
	if rec := r.get("/api/v1/armory/characters?realm=Testrealm&names=" + names(100)); rec.Code != 200 {
		t.Fatalf("100 names: %d %s", rec.Code, rec.Body)
	}
	bad := map[string]string{
		"101 names":   "/api/v1/armory/characters?realm=Testrealm&names=" + names(101),
		"no names":    "/api/v1/armory/characters?realm=Testrealm",
		"no realm":    "/api/v1/armory/characters?names=Ann",
		"empty entry": "/api/v1/armory/characters?realm=Testrealm&names=Ann,,Bob",
		"only commas": "/api/v1/armory/characters?realm=Testrealm&names=,",
		"huge":        "/api/v1/armory/characters?realm=Testrealm&names=" + strings.Repeat("a,", 20000),
		"long name":   "/api/v1/armory/characters?realm=Testrealm&names=Ann," + strings.Repeat("a", 200),
		"extra param": "/api/v1/armory/characters?realm=Testrealm&names=Ann&limit=5",
		"bidi":        "/api/v1/armory/characters?realm=Testrealm&names=" + url.QueryEscape("Ann,B‮ob"),
		"control":     "/api/v1/armory/characters?realm=Testrealm&names=" + url.QueryEscape("Ann,B\x01ob"),
		"sql":         "/api/v1/armory/characters?realm=Testrealm&names=" + url.QueryEscape("Ann,x'; DROP TABLE y;--"),
		"long realm":  "/api/v1/armory/characters?names=Ann&realm=" + strings.Repeat("r", 300),
	}
	for name, target := range bad {
		t.Run(name, func(t *testing.T) {
			rec := r.get(target)
			if rec.Code != 400 {
				t.Fatalf("status %d: %.200s", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "DROP") {
				t.Fatal("input reflected")
			}
		})
	}
}

func TestArmoryReadAuthAndMethod(t *testing.T) {
	r := newArmoryRig(t)
	r.host.Auth.Add(core.RoleView, core.AuthenticatorFunc(func(req *http.Request) bool {
		return req.Header.Get("X-View-Key") == "k"
	}))
	for _, p := range []string{"character?realm=Testrealm&name=Ann", "characters?realm=Testrealm&names=Ann"} {
		if rec := r.get("/api/v1/armory/" + p); rec.Code != 401 {
			t.Fatalf("%s no key: %d", p, rec.Code)
		}
		if rec := r.get("/api/v1/armory/"+p, "X-View-Key", "bad"); rec.Code != 401 {
			t.Fatalf("%s bad key: %d", p, rec.Code)
		}
		if rec := r.get("/api/v1/armory/"+p, "X-View-Key", "k"); rec.Code != 200 {
			t.Fatalf("%s with key: %d %s", p, rec.Code, rec.Body)
		}
		req := httptest.NewRequest("POST", "/api/v1/armory/"+p, nil)
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, req)
		if rec.Code != 405 {
			t.Fatalf("%s POST: %d", p, rec.Code)
		}
	}
}

func TestArmoryUnavailableWithoutInterface(t *testing.T) {
	r := newRig(t) // fakeStore has no armory methods
	for _, p := range []string{"character?realm=Testrealm&name=Ann", "characters?realm=Testrealm&names=Ann"} {
		rec := r.get("GET", "/api/v1/armory/"+p)
		if rec.Code != 404 || errCode(t, rec) != "not_found" {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

func TestArmoryPreflight(t *testing.T) {
	r := newArmoryRig(t)
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, httptest.NewRequest("OPTIONS", "/api/v1/armory/character", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight: %d", rec.Code)
	}
}
