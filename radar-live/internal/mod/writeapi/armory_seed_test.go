package writeapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
	"radarlive/internal/mod/writeapi"
)

const seedsPath = "/internal/v1/armory/seeds"

// seedArmory is a fakeArmory that also serves seeds and records the last call.
type seedArmory struct {
	*fakeArmory
	cursor string
	limit  int
	calls  int
	page   domain.ArmorySeedPage
}

func (f *seedArmory) ArmorySeeds(_ context.Context, cursor string, limit int) (domain.ArmorySeedPage, error) {
	f.calls++
	f.cursor, f.limit = cursor, limit
	if err := domain.ValidateArmorySeedLimit(limit); err != nil {
		return domain.ArmorySeedPage{}, err
	}
	if _, _, _, err := domain.ParseArmorySeedCursor(cursor); err != nil {
		return domain.ArmorySeedPage{}, err
	}
	return f.page, nil
}

func newSeedRig(t *testing.T) (*armoryRig, *seedArmory) {
	t.Helper()
	fs := &seedArmory{fakeArmory: newFakeArmory(), page: domain.ArmorySeedPage{
		Items: []domain.ArmorySeed{{Kind: "character", Realm: "Testrealm", Name: "Ann"}, {Kind: "guild", Realm: "Testrealm", Guild: "Test Guild"}},
		Next:  "g|testrealm|Test Guild"}}
	r := &armoryRig{host: core.NewHost()}
	if err := r.host.Stores.Add("mem", fs); err != nil {
		t.Fatal(err)
	}
	if err := r.host.Register(writeapi.NewModule(writeapi.WithClock(func() time.Time { return fakeNow }))); err != nil {
		t.Fatal(err)
	}
	if err := r.host.Config.Load(nil, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	return r, fs
}

func TestArmorySeedsServed(t *testing.T) {
	r, fs := newSeedRig(t)
	rec := r.do("GET", seedsPath, "")
	if rec.Code != 200 || fs.limit != 200 || fs.cursor != "" {
		t.Fatalf("default: %d limit %d cursor %q: %s", rec.Code, fs.limit, fs.cursor, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers %v", rec.Header())
	}
	var page domain.ArmorySeedPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 2 || page.Next != "g|testrealm|Test Guild" {
		t.Fatalf("body %s err %v", rec.Body, err)
	}
	want := `{"items":[{"kind":"character","realm":"Testrealm","name":"Ann"},{"kind":"guild","realm":"Testrealm","guild":"Test Guild"}],"next":"g|testrealm|Test Guild"}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("wire shape %s", rec.Body)
	}
	q := url.Values{"cursor": {"c|ann|testrealm"}, "limit": {"500"}}
	if rec := r.do("GET", seedsPath+"?"+q.Encode(), ""); rec.Code != 200 || fs.limit != 500 || fs.cursor != "c|ann|testrealm" {
		t.Fatalf("explicit: %d %d %q", rec.Code, fs.limit, fs.cursor)
	}
	if rec := r.do("GET", seedsPath+"?limit=1&cursor=", ""); rec.Code != 200 || fs.limit != 1 {
		t.Fatalf("empty cursor: %d", rec.Code)
	}
}

func TestArmorySeedsEmptyItemsIsArray(t *testing.T) {
	r, fs := newSeedRig(t)
	fs.page = domain.ArmorySeedPage{}
	rec := r.do("GET", seedsPath, "")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"items":[],"next":""}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestArmorySeedsHostileParams(t *testing.T) {
	r, fs := newSeedRig(t)
	long := strings.Repeat("a", domain.ArmoryCursorMax+1)
	for name, qs := range map[string]string{
		"limit zero":      "limit=0",
		"limit negative":  "limit=-1",
		"limit too big":   "limit=501",
		"limit text":      "limit=abc",
		"limit empty":     "limit=",
		"limit huge":      "limit=99999999999999999999",
		"limit float":     "limit=1.5",
		"limit dup":       "limit=1&limit=2",
		"cursor dup":      "cursor=&cursor=",
		"unknown key":     "foo=1",
		"cursor sql":      "cursor=" + url.QueryEscape("c|'; DROP TABLE contacts;--|x"),
		"cursor script":   "cursor=" + url.QueryEscape("<script>alert(1)</script>"),
		"cursor too long": "cursor=" + long,
		"cursor control":  "cursor=" + url.QueryEscape("c|a\x00|b"),
		"cursor bidi":     "cursor=" + url.QueryEscape("c|a‮|b"),
		"cursor garbage":  "cursor=zzz",
		"bad escape":      "cursor=%zz",
	} {
		t.Run(name, func(t *testing.T) {
			before := fs.calls
			rec := r.do("GET", seedsPath+"?"+qs, "")
			if rec.Code != 400 || errCode(t, rec) != "invalid_request" {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			for _, bad := range []string{"DROP", "script", "zzz"} {
				if strings.Contains(rec.Body.String(), bad) {
					t.Fatalf("error reflects input: %s", rec.Body)
				}
			}
			if fs.calls != before {
				t.Fatal("hostile input reached the store")
			}
		})
	}
}

func TestArmorySeedsAuthMethodAndClass(t *testing.T) {
	r, _ := newSeedRig(t)
	r.host.Auth.Add(core.RoleIngest, core.AuthenticatorFunc(func(req *http.Request) bool {
		return req.Header.Get("X-Ingest-Key") == "k"
	}))
	if rec := r.do("GET", seedsPath, ""); rec.Code != 401 {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := r.do("GET", seedsPath, "", "X-Ingest-Key", "wrong"); rec.Code != 401 {
		t.Fatalf("wrong key: %d", rec.Code)
	}
	if rec := r.do("GET", seedsPath, "", "X-Ingest-Key", "k"); rec.Code != 200 {
		t.Fatalf("key: %d", rec.Code)
	}
	if rec := r.do("POST", seedsPath, "{}", "X-Ingest-Key", "k"); rec.Code != 405 {
		t.Fatalf("POST: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", seedsPath, nil)
	rec := httptest.NewRecorder()
	r.host.Routes.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("public class: %d", rec.Code)
	}
}

func TestArmorySeedsUnavailableWithoutInterface(t *testing.T) {
	for name, r := range map[string]*armoryRig{"plain store": newRig2(t), "armory but no seeds": newArmoryRig(t)} {
		rec := r.do("GET", seedsPath, "")
		if rec.Code != 404 || errCode(t, rec) != "not_found" {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func newRig2(t *testing.T) *armoryRig {
	t.Helper()
	r := newRig(t)
	return &armoryRig{host: r.host}
}
