package static_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	webpBody  = "RIFFxxxxWEBPfake"
	indexBody = `{"zones":{"dunmorogh":{"name":"Dun Morogh","file":"DunMorogh.webp","w":1002,"h":668,"revealed":true}}}`
)

// mapsFixture lays out parent/secret.txt next to parent/maps/{index.json,DunMorogh.webp,sub/a.png,notes.txt}.
func mapsFixture(t *testing.T) (parent, maps string) {
	t.Helper()
	parent = t.TempDir()
	maps = filepath.Join(parent, "maps")
	if err := os.MkdirAll(filepath.Join(maps, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(parent, "secret.txt"):        secret,
		filepath.Join(parent, "maps-evil.txt"):     secret, // sibling sharing the prefix
		filepath.Join(maps, "index.json"):          indexBody,
		filepath.Join(maps, "DunMorogh.webp"):      webpBody,
		filepath.Join(maps, "sub", "a.png"):        "png",
		filepath.Join(maps, "notes.txt"):           "not a map",
		filepath.Join(maps, "page.html"):           "<script>1</script>",
		filepath.Join(maps, ".hidden.webp"):        "dot",
		filepath.Join(maps, "UPPER.WEBP"):          "upper",
		filepath.Join(maps, "noextension"):         "x",
		filepath.Join(maps, "sub", "index.json"):   "{}",
		filepath.Join(maps, "sub", "deep.webp"):    "deep",
		filepath.Join(maps, "index.json.webp.bak"): "bak",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return parent, maps
}

func TestMapsOffByDefault(t *testing.T) {
	h := newHandler(t)
	for _, p := range []string{"/maps/index.json", "/maps/DunMorogh.webp", "/maps/"} {
		if rec := get(h, p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404 with the feature off, got %d", p, rec.Code)
		}
	}
}

func TestMapsServedWithTypesAndCaching(t *testing.T) {
	_, maps := mapsFixture(t)
	h := newHandler(t, "--maps-dir", maps)
	cases := []struct {
		path, typ, body string
		longCache       bool
	}{
		{"/maps/DunMorogh.webp", "image/webp", webpBody, true},
		{"/maps/sub/a.png", "image/png", "png", true},
		{"/maps/index.json", "application/json", indexBody, false},
	}
	for _, c := range cases {
		rec := get(h, c.path)
		if rec.Code != 200 || rec.Body.String() != c.body {
			t.Fatalf("%s: %d %q", c.path, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, c.typ) {
			t.Errorf("%s: content type %q", c.path, ct)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff", c.path)
		}
		cc := rec.Header().Get("Cache-Control")
		if c.longCache && !strings.Contains(cc, "max-age=2592000") {
			t.Errorf("%s: image cache-control %q", c.path, cc)
		}
		if !c.longCache && (!strings.Contains(cc, "max-age=60") || strings.Contains(cc, "2592000")) {
			t.Errorf("%s: index cache-control %q", c.path, cc)
		}
		if rec.Header().Get("ETag") == "" {
			t.Errorf("%s: no ETag", c.path)
		}
	}
}

func TestMapsConditionalRequest(t *testing.T) {
	_, maps := mapsFixture(t)
	h := newHandler(t, "--maps-dir", maps)
	etag := get(h, "/maps/DunMorogh.webp").Header().Get("ETag")
	req := httptest.NewRequest(http.MethodGet, "/maps/DunMorogh.webp", nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("want 304, got %d", rec.Code)
	}
}

func TestMapsUnknownAndOtherTypes404(t *testing.T) {
	_, maps := mapsFixture(t)
	h := newHandler(t, "--maps-dir", maps)
	for _, p := range []string{
		"/maps/missing.webp", "/maps/notes.txt", "/maps/page.html", "/maps/noextension", "/maps/", "/maps",
		"/maps/sub/", "/maps/sub", "/maps/DunMorogh.webp/", "/maps/UPPER.WEBP", "/maps/index.json.webp.bak",
		"/maps/.hidden.webp",
	} {
		rec := get(h, p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", p, rec.Code)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: 404 lacks nosniff", p)
		}
	}
}

func TestMapsOnlyGet(t *testing.T) {
	_, maps := mapsFixture(t)
	h := newHandler(t, "--maps-dir", maps)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/maps/index.json", nil))
		if rec.Code == 200 {
			t.Errorf("%s served a map", m)
		}
	}
}

func TestMapsTraversalAttempts(t *testing.T) {
	_, maps := mapsFixture(t)
	h := newHandler(t, "--maps-dir", maps)
	paths := []string{
		"/maps/../secret.txt", "/maps/..%2fsecret.txt", "/maps/%2e%2e/secret.txt", "/maps/%2e%2e%2fsecret.txt",
		"/maps/%2E%2E/secret.txt", "/maps/sub/../../secret.txt", "/maps/sub/%2e%2e/%2e%2e/secret.txt",
		"/maps/..%5csecret.txt", "/maps/%5c..%5csecret.txt", "/maps/sub/..%5c..%5csecret.txt", "/maps/%00",
		"/maps/index.json%00.png", "/maps/..%00/secret.txt", "/maps//secret.txt", "/maps/./../secret.txt",
		"/maps/....//secret.txt", "/maps/..;/secret.txt", "/maps/%252e%252e/secret.txt", "/maps/..%c0%af/secret.txt",
		"/maps/../maps-evil.txt", "/maps/C:/Windows/win.ini", "/maps/C:%5cWindows%5cwin.ini",
		"/maps/%2fetc%2fpasswd", "/maps/etc/passwd", "/maps/..%2f..%2fsecret.txt.webp", "/maps/../secret.txt.png",
	}
	for _, p := range paths {
		rec := get(h, p)
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("%s leaked the secret (status %d)", p, rec.Code)
		}
		if rec.Code == 200 {
			t.Errorf("%s: unexpected 200 %q", p, rec.Body.String())
		}
	}
}

func TestMapsSymlinkEscape(t *testing.T) {
	parent, maps := mapsFixture(t)
	if err := os.WriteFile(filepath.Join(parent, "secret.webp"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "secret.webp"), filepath.Join(maps, "link.webp")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linkDir(t, parent, filepath.Join(maps, "up"))
	h := newHandler(t, "--maps-dir", maps)
	for _, p := range []string{"/maps/link.webp", "/maps/up/secret.webp"} {
		rec := get(h, p)
		if rec.Code == 200 || strings.Contains(rec.Body.String(), secret) {
			t.Errorf("%s escaped via link: %d", p, rec.Code)
		}
	}
	if rec := get(h, "/maps/DunMorogh.webp"); rec.Code != 200 {
		t.Errorf("normal map: %d", rec.Code)
	}
}

func TestMapsDirMissingServesNothingAndPageStillWorks(t *testing.T) {
	h := newHandler(t, "--maps-dir", filepath.Join(t.TempDir(), "does-not-exist"))
	if rec := get(h, "/maps/index.json"); rec.Code != http.StatusNotFound {
		t.Errorf("missing maps dir: %d", rec.Code)
	}
	if rec := get(h, "/"); rec.Code != 200 {
		t.Errorf("page broke: %d", rec.Code)
	}
}

func TestMapsDoNotShadowThePage(t *testing.T) {
	_, maps := mapsFixture(t)
	h := newHandler(t, "--maps-dir", maps)
	if rec := get(h, "/js/core.js"); rec.Code != 200 {
		t.Errorf("page asset: %d", rec.Code)
	}
	if rec := get(h, "/api/v1/nothing"); rec.Code != 404 {
		t.Errorf("api: %d", rec.Code)
	}
}
