package readapi_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestCORS(t *testing.T) {
	r := newRig(t, "--cors-origins", "https://a.example.test, https://b.example.test")
	for _, origin := range []string{"https://a.example.test", "https://b.example.test"} {
		rec := r.get("GET", "/api/v1/status", "Origin", origin)
		if rec.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Fatalf("allow-origin for %s = %v", origin, rec.Header())
		}
		if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("credentials must never be allowed")
		}
		pre := r.get("OPTIONS", "/api/v1/sweep", "Origin", origin, "Access-Control-Request-Method", "GET")
		if pre.Code != 204 || !strings.Contains(pre.Header().Get("Access-Control-Allow-Headers"), "X-View-Key") ||
			pre.Header().Get("Access-Control-Allow-Origin") != origin || pre.Header().Get("Access-Control-Allow-Methods") == "" {
			t.Fatalf("preflight = %d %v", pre.Code, pre.Header())
		}
	}
	plain := newRig(t)
	if plain.get("GET", "/api/v1/status", "Origin", "https://a.example.test").Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS header set without configuration")
	}
}

func TestCORSRejectsUnknownOriginsWithoutEcho(t *testing.T) {
	r := newRig(t, "--cors-origins", "https://a.example.test")
	for _, bad := range []string{"https://evil.test", "https://a.example.test.evil.test", "http://a.example.test",
		"https://a.example.test:8443", "https://A.example.test", "null", "*", "https://a.example.test/"} {
		get := r.get("GET", "/api/v1/status", "Origin", bad)
		if get.Header().Get("Access-Control-Allow-Origin") != "" || strings.Contains(fmt.Sprint(get.Header()), "evil") {
			t.Fatalf("origin %q echoed: %v", bad, get.Header())
		}
		pre := r.get("OPTIONS", "/api/v1/sweep", "Origin", bad, "Access-Control-Request-Method", "GET")
		if pre.Code != 403 || pre.Header().Get("Access-Control-Allow-Origin") != "" ||
			pre.Header().Get("Access-Control-Allow-Methods") != "" || strings.Contains(pre.Body.String()+fmt.Sprint(pre.Header()), "evil") {
			t.Fatalf("preflight for %q = %d %v", bad, pre.Code, pre.Header())
		}
	}
}

func TestCORSVaryOriginAlways(t *testing.T) {
	r := newRig(t, "--cors-origins", "https://a.example.test")
	for _, origin := range []string{"", "https://evil.test", "https://a.example.test"} {
		rec := r.get("GET", "/api/v1/status", "Origin", origin)
		if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
			t.Fatalf("no Vary: Origin for %q: %v", origin, rec.Header())
		}
	}
}

func TestCORSWildcardNeverHonoured(t *testing.T) {
	r := newRig(t, "--cors-origins", "*")
	rec := r.get("GET", "/api/v1/status", "Origin", "https://evil.test")
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("wildcard honoured: %v", rec.Header())
	}
}
