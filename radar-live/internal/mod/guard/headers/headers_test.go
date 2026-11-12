package headers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/mod/guard/headers"
)

// fakeAPI is a stand-in feature module with an API route and a failing route.
type fakeAPI struct{}

func (fakeAPI) Name() string { return "fakeapi" }

func (fakeAPI) Init(h *core.Host) error {
	if err := h.Routes.HandleFunc(http.MethodGet, "/api/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	}); err != nil {
		return err
	}
	if err := h.Routes.HandleFunc(http.MethodGet, "/style.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = io.WriteString(w, "body{}")
	}); err != nil {
		return err
	}
	if err := h.Routes.HandleFunc(http.MethodGet, "/", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "page") }); err != nil {
		return err
	}
	return h.Routes.HandleFunc(http.MethodGet, "/boom", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusTeapot)
	})
}

// start runs a real host with the given modules plus the fake API and returns its base URL.
func start(t *testing.T, args []string, mods ...core.Module) string {
	t.Helper()
	host := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := host.Register(append(mods, fakeAPI{})...); err != nil {
		t.Fatal(err)
	}
	args = append([]string{"--listen", "127.0.0.1:0", "--shutdown-timeout", "2s"}, args...)
	if err := host.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Run(ctx) }()
	select {
	case <-host.Ready():
	case err := <-done:
		cancel()
		t.Fatalf("host stopped early: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("host never became ready")
	}
	t.Cleanup(func() { cancel(); <-done })
	return "http://" + host.Addr()
}

func fetch(t *testing.T, method, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func TestCSPOnEveryResponse(t *testing.T) {
	base := start(t, nil, headers.New())
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/", 200}, {"GET", "/style.css", 200}, {"GET", "/api/v1/ping", 200},
		{"GET", "/boom", 418},
		{"POST", "/", 405}, {"DELETE", "/api/v1/ping", 405},
	} {
		resp := fetch(t, tc.method, base+tc.path)
		if resp.StatusCode != tc.status {
			t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.status)
		}
		if got := resp.Header.Get("Content-Security-Policy"); got != wantCSP {
			t.Fatalf("%s %s CSP = %q", tc.method, tc.path, got)
		}
	}
}

func TestCSPListsConfiguredAPIOrigins(t *testing.T) {
	base := start(t, []string{"--headers-api-origins", "https://api.example.test, https://b.example.test:8443"}, headers.New())
	got := fetch(t, "GET", base+"/boom").Header.Get("Content-Security-Policy")
	if !strings.Contains(got, "connect-src 'self' https://api.example.test https://b.example.test:8443; object-src 'none'") {
		t.Fatalf("CSP = %q", got)
	}
}

func TestSecurityHeadersAll(t *testing.T) {
	base := start(t, nil, headers.New())
	want := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"X-Frame-Options":              "DENY",
	}
	for _, path := range []string{"/", "/api/v1/ping", "/nope", "/boom"} {
		resp := fetch(t, "GET", base+path)
		for k, v := range want {
			if got := resp.Header.Get(k); got != v {
				t.Fatalf("%s %s = %q, want %q", path, k, got, v)
			}
		}
		pp := resp.Header.Get("Permissions-Policy")
		for _, f := range []string{"camera=()", "microphone=()", "geolocation=()", "payment=()", "usb=()"} {
			if !strings.Contains(pp, f) {
				t.Fatalf("%s Permissions-Policy %q lacks %s", path, pp, f)
			}
		}
		if resp.Header.Get("Set-Cookie") != "" {
			t.Fatalf("%s sets a cookie", path)
		}
	}
}

func TestHSTSFlag(t *testing.T) {
	off := start(t, nil, headers.New())
	if v := fetch(t, "GET", off+"/boom").Header.Get("Strict-Transport-Security"); v != "" {
		t.Fatalf("HSTS set by default: %q", v)
	}
	on := start(t, []string{"--headers-hsts"}, headers.New())
	v := fetch(t, "GET", on+"/boom").Header.Get("Strict-Transport-Security")
	if !strings.Contains(v, "max-age=") || strings.Contains(v, "preload") {
		t.Fatalf("HSTS = %q", v)
	}
}

func TestNoStoreOnAPIOnly(t *testing.T) {
	base := start(t, nil, headers.New())
	if got := fetch(t, "GET", base+"/api/v1/ping").Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("api Cache-Control = %q", got)
	}
	if got := fetch(t, "DELETE", base+"/api/v1/ping").Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("api 405 Cache-Control = %q", got)
	}
	if got := fetch(t, "GET", base+"/style.css").Header.Get("Cache-Control"); !strings.Contains(got, "max-age") {
		t.Fatalf("asset Cache-Control = %q (headers must not override caching)", got)
	}
}

func TestBadAPIOriginRefusesToStart(t *testing.T) {
	for _, bad := range []string{"*", "https://a.test/path", "https://a.test; script-src *", "javascript:alert(1)", "https://a.test'", "a.test"} {
		host := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		if err := host.Register(headers.New()); err != nil {
			t.Fatal(err)
		}
		args := []string{"--listen", "127.0.0.1:0", "--headers-api-origins", bad}
		if err := host.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := host.Run(ctx)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "headers") {
			t.Fatalf("origin %q accepted (err %v)", bad, err)
		}
	}
}

func TestHostWorksWithoutHeadersModule(t *testing.T) {
	base := start(t, nil)
	resp := fetch(t, "GET", base+"/")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Security-Policy") != "" {
		t.Fatalf("status %d csp %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
}

func TestName(t *testing.T) {
	if headers.New().Name() != "headers" {
		t.Fatal("name")
	}
}
