package core

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestRoutesClassesAreIsolated(t *testing.T) {
	h := NewHost()
	if err := h.EnsureListener(ClassInternal, "internal-listen", "127.0.0.1:0", "internal listen address"); err != nil {
		t.Fatal(err)
	}
	if err := h.Routes.HandleFuncOn(ClassInternal, "GET", "/internal/x", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.Routes.HandleFunc("GET", "/pub", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("pub")) }); err != nil {
		t.Fatal(err)
	}
	loadRandomPorts(t, h)
	stop := runHost(t, h)
	defer stop()
	pub, internal := "http://"+h.Addr(), "http://"+h.ClassAddr(ClassInternal)
	if h.ClassAddr(ClassInternal) == "" || h.ClassAddr(ClassInternal) == h.Addr() {
		t.Fatalf("internal addr %q public %q", h.ClassAddr(ClassInternal), h.Addr())
	}
	if code, body := get(t, internal+"/internal/x"); code != 200 || body != "secret" {
		t.Fatalf("internal route = %d %q", code, body)
	}
	if code, _ := get(t, pub+"/internal/x"); code != 404 {
		t.Fatalf("internal route on public listener = %d, want 404", code)
	}
	if code, _ := get(t, internal+"/pub"); code != 404 {
		t.Fatalf("public route on internal listener = %d, want 404", code)
	}
	for _, base := range []string{pub, internal} {
		if code, body := get(t, base+"/healthz"); code != 200 || strings.TrimSpace(body) != "ok" {
			t.Fatalf("healthz on %s = %d %q", base, code, body)
		}
	}
}

// loadRandomPorts loads the config with the public listener on a random port.
func loadRandomPorts(t *testing.T, h *Host) {
	t.Helper()
	if err := h.Config.Load([]string{"--listen", "127.0.0.1:0"}, envOf(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureListenerIdempotentAndValidated(t *testing.T) {
	h := NewHost()
	if err := h.EnsureListener(ClassInternal, "internal-listen", "127.0.0.1:0", "u"); err != nil {
		t.Fatal(err)
	}
	if err := h.EnsureListener(ClassInternal, "internal-listen", "127.0.0.1:0", "u"); err != nil {
		t.Fatalf("second call should be a no-op: %v", err)
	}
	if err := h.EnsureListener(ClassInternal, "other-key", "", "u"); err == nil {
		t.Fatal("same class with another key must fail")
	}
	if err := h.EnsureListener("", "k", "", "u"); err == nil {
		t.Fatal("empty class must fail")
	}
	err := h.Routes.HandleFuncOn("nope", "GET", "/x", func(http.ResponseWriter, *http.Request) {})
	if err == nil {
		t.Fatal("unknown class must fail")
	}
}

func TestInternalListenerDisabledWhenAddressEmpty(t *testing.T) {
	h := NewHost()
	if err := h.EnsureListener(ClassInternal, "internal-listen", "", "u"); err != nil {
		t.Fatal(err)
	}
	if err := h.Routes.HandleFuncOn(ClassInternal, "GET", "/internal/x", func(http.ResponseWriter, *http.Request) {}); err != nil {
		t.Fatal(err)
	}
	loadRandomPorts(t, h)
	stop := runHost(t, h)
	defer stop()
	if h.ClassAddr(ClassInternal) != "" {
		t.Fatal("no internal listener expected")
	}
	if code, _ := get(t, "http://"+h.Addr()+"/internal/x"); code != 404 {
		t.Fatalf("internal route leaked to public: %d", code)
	}
}

func TestServerSocketHooksApply(t *testing.T) {
	h := newTestHost(t)
	var tuned, wrapped []string
	h.Server.Tune(func(class string, srv *http.Server) {
		tuned = append(tuned, class)
		srv.MaxHeaderBytes = 4321
	})
	h.Server.WrapListener(func(class string, ln net.Listener) net.Listener {
		wrapped = append(wrapped, class)
		return ln
	})
	h.Server.WrapHandler(func(_ string, next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Wrapped", "yes")
			next.ServeHTTP(w, r)
		})
	})
	stop := runHost(t, h)
	defer stop()
	resp, err := http.Get("http://" + h.Addr() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Wrapped") != "yes" {
		t.Fatal("handler wrapper not applied")
	}
	if len(tuned) != 1 || tuned[0] != ClassPublic || len(wrapped) != 1 {
		t.Fatalf("tuned=%v wrapped=%v", tuned, wrapped)
	}
}
