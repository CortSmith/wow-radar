package static_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radarlive/internal/core"
	"radarlive/internal/mod/static"
)

// newHandler builds a host with the static module and an API route, then returns its handler.
func newHandler(t *testing.T, args ...string) http.Handler {
	t.Helper()
	host := core.NewHost()
	if err := host.Routes.HandleFunc("GET", "/api/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"api":true}`)
	}); err != nil {
		t.Fatal(err)
	}
	if err := host.Register(static.NewModule()); err != nil {
		t.Fatal(err)
	}
	if err := host.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	return host.Routes.Handler()
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServesPageAtRoot(t *testing.T) {
	rec := get(newHandler(t), "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `data-mount="radar"`) {
		t.Fatalf("code=%d body=%.80s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("html cache-control %q", cc)
	}
}

func TestAssetContentTypesAndCaching(t *testing.T) {
	h := newHandler(t)
	cases := []struct{ path, typ string }{
		{"/style.css", "text/css"},
		{"/js/core.js", "text/javascript"},
		{"/js/config.js", "text/javascript"},
		{"/js/features/radar-canvas.js", "text/javascript"},
	}
	for _, c := range cases {
		rec := get(h, c.path)
		if rec.Code != 200 {
			t.Fatalf("%s: code %d", c.path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, c.typ) {
			t.Errorf("%s: content type %q", c.path, ct)
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "max-age") {
			t.Errorf("%s: cache-control %q", c.path, rec.Header().Get("Cache-Control"))
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", c.path)
		}
	}
}

func TestETagGives304(t *testing.T) {
	h := newHandler(t)
	etag := get(h, "/style.css").Header().Get("ETag")
	if etag == "" {
		t.Fatal("no etag")
	}
	req := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestAPIIsNotShadowed(t *testing.T) {
	h := newHandler(t)
	if rec := get(h, "/api/v1/ping"); rec.Body.String() != `{"api":true}` {
		t.Fatalf("api shadowed: %q", rec.Body.String())
	}
	rec := get(h, "/api/v1/missing")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("unknown api path: %d %q", rec.Code, rec.Body.String())
	}
}

func TestUnknownFileAndTraversal(t *testing.T) {
	h := newHandler(t)
	for _, p := range []string{"/nope.js", "/js/../../go.mod", "/js/", "/%2e%2e/go.mod"} {
		if rec := get(h, p); rec.Code != 404 && rec.Code != 400 && (rec.Code < 300 || rec.Code > 399) {
			t.Errorf("%s: code %d", p, rec.Code)
		}
	}
}

func TestPostNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	newHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestSiteDirOverrideServesFromDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<p>disk</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHandler(t, "--site-dir", dir)
	if rec := get(h, "/"); !strings.Contains(rec.Body.String(), "disk") {
		t.Fatalf("body %q", rec.Body.String())
	}
	if rec := get(h, "/style.css"); rec.Code != 404 {
		t.Fatalf("embedded file leaked: %d", rec.Code)
	}
}

func TestName(t *testing.T) {
	if static.NewModule().Name() != "static" {
		t.Fatal("name")
	}
}

func TestDuplicateRegistrationFails(t *testing.T) {
	host := core.NewHost()
	if err := host.Register(static.NewModule(), static.NewModule()); err == nil {
		t.Fatal("expected error")
	}
}

func TestSecurityTxtOmittedWhenUnset(t *testing.T) {
	h := newHandler(t)
	if rec := get(h, "/.well-known/security.txt"); rec.Code != 404 {
		t.Fatalf("security.txt without contact = %d", rec.Code)
	}
}

func TestSecurityTxtServedFromContact(t *testing.T) {
	for in, want := range map[string]string{
		"mailto:sec@example.test":     "Contact: mailto:sec@example.test\n",
		"sec@example.test":            "Contact: mailto:sec@example.test\n",
		"https://example.test/report": "Contact: https://example.test/report\n",
	} {
		rec := get(newHandler(t, "--security-contact", in), "/.well-known/security.txt")
		body := rec.Body.String()
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") ||
			!strings.Contains(body, want) || !strings.Contains(body, "Expires: ") {
			t.Fatalf("%q: %d %q %q", in, rec.Code, rec.Header().Get("Content-Type"), body)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("no nosniff")
		}
	}
}

func TestSecurityTxtRejectsInjection(t *testing.T) {
	rec := get(newHandler(t, "--security-contact", "a@example.test\nContact: evil"), "/.well-known/security.txt")
	if rec.Code != 404 {
		t.Fatalf("injected contact served: %d %q", rec.Code, rec.Body.String())
	}
}
