package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// publicGet fetches a public-listener path and returns the response (body drained).
func publicGet(t *testing.T, s *server, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(s.base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestPageIsServedWithSecurityHeaders(t *testing.T) {
	s := startServer(t, "--store", "memstore", "--headers-api-origins", "https://api.example.test")
	for _, path := range []string{"/", "/style.css", "/js/main.js", "/healthz", "/api/v1/status", "/missing"} {
		resp, _ := publicGet(t, s, path)
		csp := resp.Header.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "script-src 'self'", "style-src 'self'",
			"connect-src 'self' https://api.example.test", "object-src 'none'", "base-uri 'none'",
			"form-action 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, want) {
				t.Fatalf("%s: CSP %q lacks %q", path, csp, want)
			}
		}
		for k, v := range map[string]string{
			"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY",
			"Cross-Origin-Opener-Policy": "same-origin", "Cross-Origin-Resource-Policy": "same-origin",
		} {
			if resp.Header.Get(k) != v {
				t.Fatalf("%s: %s = %q", path, k, resp.Header.Get(k))
			}
		}
		if !strings.Contains(resp.Header.Get("Permissions-Policy"), "camera=()") {
			t.Fatalf("%s: Permissions-Policy = %q", path, resp.Header.Get("Permissions-Policy"))
		}
		if resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Strict-Transport-Security") != "" {
			t.Fatalf("%s: unexpected Set-Cookie or HSTS: %v", path, resp.Header)
		}
	}
	resp, body := publicGet(t, s, "/")
	if resp.StatusCode != 200 || !strings.Contains(body, "Nearby Radar") {
		t.Fatalf("page = %d", resp.StatusCode)
	}
	if resp, _ := publicGet(t, s, "/api/v1/status"); resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("API Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
}

func TestHeadersModuleIsRemovable(t *testing.T) {
	s := startServerWithout(t, []string{"headers"}, "--store", "memstore")
	resp, body := publicGet(t, s, "/")
	if resp.StatusCode != 200 || !strings.Contains(body, "Nearby Radar") || resp.Header.Get("Content-Security-Policy") != "" {
		t.Fatalf("host without headers: %d csp=%q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
}

func TestSecurityTxtOnlyWhenContactConfigured(t *testing.T) {
	s := startServer(t, "--store", "memstore")
	if resp, _ := publicGet(t, s, "/.well-known/security.txt"); resp.StatusCode != 404 {
		t.Fatalf("security.txt without contact = %d", resp.StatusCode)
	}
	s = startServer(t, "--store", "memstore", "--security-contact", "mailto:security@example.test")
	resp, body := publicGet(t, s, "/.well-known/security.txt")
	if resp.StatusCode != 200 || !strings.Contains(body, "Contact: mailto:security@example.test") {
		t.Fatalf("security.txt = %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("security.txt lacks the security headers")
	}
}
