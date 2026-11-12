package static_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const secret = "TOP-SECRET-CONTENT"

// siteWithSecret lays out parent/secret.txt next to parent/site/index.html and returns both.
func siteWithSecret(t *testing.T) (parent, site string) {
	t.Helper()
	parent = t.TempDir()
	site = filepath.Join(parent, "site")
	if err := os.MkdirAll(filepath.Join(site, "js"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(parent, "secret.txt"):    secret,
		filepath.Join(site, "index.html"):      "<p>home</p>",
		filepath.Join(site, "js", "app.js"):    "var a=1",
		filepath.Join(parent, "site-evil.txt"): secret, // sibling sharing the prefix
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return parent, site
}

func TestSiteDirTraversalAttempts(t *testing.T) {
	_, site := siteWithSecret(t)
	h := newHandler(t, "--site-dir", site)
	paths := []string{
		"/../secret.txt", "/..%2fsecret.txt", "/%2e%2e/secret.txt", "/%2e%2e%2fsecret.txt", "/%2E%2E/secret.txt",
		"/js/../../secret.txt", "/js/%2e%2e/%2e%2e/secret.txt", "/..%5csecret.txt", "/%5c..%5csecret.txt",
		"/js/..%5c..%5csecret.txt", "/%00", "/index.html%00.png", "/..%00/secret.txt", "/%2e%2e%00/secret.txt",
		"//secret.txt", "/./../secret.txt", "/....//secret.txt", "/..;/secret.txt", "/%252e%252e/secret.txt",
		"/..%c0%af/secret.txt", "/../site-evil.txt", "/C:/Windows/win.ini", "/C:%5cWindows%5cwin.ini",
		"/%2fetc%2fpasswd", "/etc/passwd",
	}
	for _, p := range paths {
		rec := get(h, p)
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("%s leaked the secret (status %d)", p, rec.Code)
		}
		if rec.Code == 200 && !strings.Contains(rec.Body.String(), "home") {
			t.Errorf("%s: unexpected 200 %q", p, rec.Body.String())
		}
	}
}

func TestSiteDirRawPathAttacks(t *testing.T) {
	_, site := siteWithSecret(t)
	h := newHandler(t, "--site-dir", site)
	for _, p := range []string{`/..\secret.txt`, "/a\x00b", `\secret.txt`, `/js\..\..\secret.txt`, "/js/app.js\x00", `/C:\secret.txt`, ""} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Path = p
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), secret) || rec.Code >= 500 {
			t.Errorf("%q: status %d leaked or crashed", p, rec.Code)
		}
		if p != "" && rec.Code == 200 {
			t.Errorf("%q: served with 200", p)
		}
	}
}

func TestSiteDirAbsolutePathIsNotAFileName(t *testing.T) {
	parent, site := siteWithSecret(t)
	h := newHandler(t, "--site-dir", site)
	abs := filepath.ToSlash(filepath.Join(parent, "secret.txt"))
	if rec := get(h, "/"+abs); strings.Contains(rec.Body.String(), secret) {
		t.Fatal("absolute path served")
	}
}

func TestSiteDirSymlinkEscape(t *testing.T) {
	parent, site := siteWithSecret(t)
	if err := os.Symlink(filepath.Join(parent, "secret.txt"), filepath.Join(site, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(parent, filepath.Join(site, "up")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// a symlink that stays inside the site is fine
	if err := os.Symlink(filepath.Join(site, "js", "app.js"), filepath.Join(site, "inside.js")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	h := newHandler(t, "--site-dir", site)
	for _, p := range []string{"/link.txt", "/up/secret.txt", "/up/site/index.html/../../secret.txt"} {
		rec := get(h, p)
		if strings.Contains(rec.Body.String(), secret) || rec.Code == 200 {
			t.Errorf("%s escaped via symlink: %d %q", p, rec.Code, rec.Body.String())
		}
	}
	if rec := get(h, "/inside.js"); rec.Code != 200 || rec.Body.String() != "var a=1" {
		t.Errorf("inside symlink: %d %q", rec.Code, rec.Body.String())
	}
	if rec := get(h, "/js/app.js"); rec.Code != 200 {
		t.Errorf("regular file: %d", rec.Code)
	}
}

func TestSiteDirRootSymlinkIsFollowed(t *testing.T) {
	_, site := siteWithSecret(t)
	link := filepath.Join(t.TempDir(), "sitelink")
	if err := os.Symlink(site, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	h := newHandler(t, "--site-dir", link)
	if rec := get(h, "/"); !strings.Contains(rec.Body.String(), "home") {
		t.Fatalf("symlinked site root not served: %d", rec.Code)
	}
}

func TestHeadersOnErrors(t *testing.T) {
	h := newHandler(t)
	rec := get(h, "/api/v1/nothing")
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("api 404 lacks nosniff")
	}
	rec = get(h, "/missing.html")
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("404 lacks nosniff")
	}
}

// linkDir makes link point at target: a symlink, or on Windows without the privilege a junction.
func linkDir(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	}
	if runtime.GOOS != "windows" {
		t.Skip("symlinks unavailable")
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("junction unavailable: %v %s", err, out)
	}
}

func TestSiteDirDirectoryLinkEscape(t *testing.T) {
	parent, site := siteWithSecret(t)
	linkDir(t, parent, filepath.Join(site, "up"))
	h := newHandler(t, "--site-dir", site)
	for _, p := range []string{"/up/secret.txt", "/up/site-evil.txt"} {
		rec := get(h, p)
		if rec.Code == 200 || strings.Contains(rec.Body.String(), secret) {
			t.Errorf("%s escaped through a directory link: %d", p, rec.Code)
		}
	}
	if rec := get(h, "/js/app.js"); rec.Code != 200 {
		t.Errorf("normal file: %d", rec.Code)
	}
}
