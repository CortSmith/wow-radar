package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"radarlive/internal/feed"
)

const testToken = "test-token-abcdef0123456789"

type fakePicker struct {
	path string
	err  error
	got  PickOptions
}

func (f *fakePicker) Pick(_ context.Context, opts PickOptions) (string, error) {
	f.got = opts
	return f.path, f.err
}

type uiRig struct {
	srv          *httptest.Server
	ui           *UIServer
	keys         *KeyStore
	picker       *fakePicker
	session      *sessionRig
	settingsFile string
	quit         chan struct{}
	logPath      string
}

func newUIRig(t *testing.T) *uiRig {
	t.Helper()
	dir := t.TempDir()
	rig := &uiRig{
		keys:         NewKeyStore(envWith("")),
		picker:       &fakePicker{},
		session:      newRig(t, nil),
		settingsFile: filepath.Join(dir, "cfg", "settings.json"),
		quit:         make(chan struct{}, 1),
		logPath:      filepath.Join(dir, "WoWCombatLog.txt"),
	}
	writeLog(t, rig.logPath, "")
	rig.ui = NewUIServer(UIOptions{
		Settings:     Settings{Server: "http://127.0.0.1:8788"},
		SettingsPath: rig.settingsFile,
		Keys:         rig.keys,
		Session:      rig.session.session,
		Picker:       rig.picker,
		CanBrowse:    true,
		Token:        testToken,
		Source:       "test-pc",
		Version:      "v-test",
		Quit:         func() { rig.quit <- struct{}{} },
	})
	rig.srv = httptest.NewServer(rig.ui.Handler())
	t.Cleanup(rig.srv.Close)
	return rig
}

func (r *uiRig) host() string { return strings.TrimPrefix(r.srv.URL, "http://") }

// do sends an authorised API request and returns status and body.
func (r *uiRig) do(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	return r.doWith(t, method, path, body, map[string]string{"X-Radar-Token": testToken})
}

func (r *uiRig) doWith(t *testing.T, method, path string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	switch v := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, r.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return v
}

func TestPageNeedsTheToken(t *testing.T) {
	r := newUIRig(t)
	if code, _ := r.doWith(t, "GET", "/", nil, nil); code != http.StatusForbidden {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := r.doWith(t, "GET", "/?t=wrong", nil, nil); code != http.StatusForbidden {
		t.Fatalf("wrong token: %d", code)
	}
	code, body := r.doWith(t, "GET", "/?t="+testToken, nil, nil)
	if code != http.StatusOK || !strings.Contains(string(body), `content="`+testToken+`"`) {
		t.Fatalf("page: %d %s", code, body)
	}
}

func TestStaticAssetsAreServedWithSafeHeaders(t *testing.T) {
	r := newUIRig(t)
	for path, wantType := range map[string]string{"/app.js": "javascript", "/logic.js": "javascript", "/style.css": "text/css"} {
		req, _ := http.NewRequest("GET", r.srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), wantType) {
			t.Fatalf("%s: %d %q", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		csp := resp.Header.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, want) {
				t.Fatalf("%s CSP %q lacks %q", path, csp, want)
			}
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Cache-Control") != "no-store" ||
			resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("%s headers: %v", path, resp.Header)
		}
	}
	if code, _ := r.doWith(t, "GET", "/nope.js", nil, nil); code != http.StatusNotFound {
		t.Fatalf("unknown asset: %d", code)
	}
}

func TestHostHeaderMustBeLoopbackWithOurPort(t *testing.T) {
	r := newUIRig(t)
	_, port, _ := net.SplitHostPort(r.host())
	tests := []struct {
		host string
		want int
	}{
		{"127.0.0.1:" + port, 200},
		{"localhost:" + port, 200},
		{"evil.example.test:" + port, http.StatusForbidden},
		{"127.0.0.1:1", http.StatusForbidden},
		{"127.0.0.1", http.StatusForbidden},
		{"[::1]:" + port, http.StatusForbidden},
	}
	for _, tc := range tests {
		code, _ := r.doWith(t, "GET", "/api/status", nil, map[string]string{"Host": tc.host, "X-Radar-Token": testToken})
		if code != tc.want {
			t.Errorf("Host %q: %d, want %d", tc.host, code, tc.want)
		}
	}
}

func TestAPINeedsTheToken(t *testing.T) {
	r := newUIRig(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/settings"}, {"PUT", "/api/settings"}, {"PUT", "/api/key"}, {"POST", "/api/start"},
		{"POST", "/api/stop"}, {"GET", "/api/status"}, {"POST", "/api/pick-file"}, {"POST", "/api/quit"},
	} {
		for name, headers := range map[string]map[string]string{
			"missing": nil, "wrong": {"X-Radar-Token": "wrong"}, "empty": {"X-Radar-Token": ""},
		} {
			code, body := r.doWith(t, tc.method, tc.path, `{}`, headers)
			if code != http.StatusUnauthorized {
				t.Errorf("%s %s with %s token: %d %s", tc.method, tc.path, name, code, body)
			}
		}
	}
}

func TestCrossOriginRequestsAreRefused(t *testing.T) {
	r := newUIRig(t)
	code, _ := r.doWith(t, "GET", "/api/status", nil, map[string]string{"X-Radar-Token": testToken, "Origin": "http://evil.example.test"})
	if code != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", code)
	}
	code, _ = r.doWith(t, "GET", "/api/status", nil, map[string]string{"X-Radar-Token": testToken, "Origin": "http://" + r.host()})
	if code != http.StatusOK {
		t.Fatalf("own origin: %d", code)
	}
}

func TestGetSettingsShowsKeySourceNeverTheKey(t *testing.T) {
	r := newUIRig(t)
	r.keys.Set(fakeKey)
	code, body := r.do(t, "GET", "/api/settings", nil)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if strings.Contains(string(body), fakeKey) {
		t.Fatalf("settings leak the key: %s", body)
	}
	v := decode[map[string]any](t, body)
	if v["server"] != "http://127.0.0.1:8788" || v["keySource"] != KeyTyped || v["canBrowse"] != true || v["version"] != "v-test" || v["canQuit"] != true {
		t.Fatalf("view = %v", v)
	}
}

func TestPutSettingsValidatesSavesAndNeverStoresTheKey(t *testing.T) {
	r := newUIRig(t)
	want := Settings{Server: "http://10.1.2.3:8788/", Ruleset: "PVP", LogPath: `"` + r.logPath + `"`, FollowNewest: true}
	code, body := r.do(t, "PUT", "/api/settings", want)
	if code != 200 {
		t.Fatalf("code %d %s", code, body)
	}
	saved, err := LoadSettings(r.settingsFile, Settings{})
	if err != nil || saved.Server != "http://10.1.2.3:8788" || saved.Ruleset != "pvp" || saved.LogPath != r.logPath || !saved.FollowNewest {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
	code, _ = r.do(t, "PUT", "/api/settings", Settings{Server: "ftp://x"})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid settings accepted: %d", code)
	}
}

func TestBadInputIsRejectedClearly(t *testing.T) {
	r := newUIRig(t)
	tests := []struct {
		name, method, path, body string
	}{
		{"not json", "PUT", "/api/settings", `{nope`},
		{"unknown field", "PUT", "/api/settings", `{"server":"http://h:1","password":"x"}`},
		{"trailing data", "PUT", "/api/settings", `{"server":"http://h:1"} {}`},
		{"oversized", "PUT", "/api/settings", `{"server":"` + strings.Repeat("a", maxBodyBytes) + `"}`},
		{"bad key body", "PUT", "/api/key", `[]`},
		{"bad start body", "POST", "/api/start", `"x"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, body := r.do(t, tc.method, tc.path, tc.body)
			if code != http.StatusBadRequest && code != http.StatusRequestEntityTooLarge {
				t.Fatalf("code %d %s", code, body)
			}
			if e := decode[map[string]string](t, body); e["error"] == "" || e["message"] == "" {
				t.Fatalf("error body = %s", body)
			}
		})
	}
}

func TestPutKeyKeepsItInMemoryOnly(t *testing.T) {
	r := newUIRig(t)
	code, body := r.do(t, "PUT", "/api/key", map[string]string{"key": fakeKey})
	if code != 200 || strings.Contains(string(body), fakeKey) {
		t.Fatalf("code %d body %s", code, body)
	}
	if r.keys.Key() != fakeKey || decode[map[string]string](t, body)["keySource"] != KeyTyped {
		t.Fatalf("key not stored: %s", body)
	}
	if _, err := os.Stat(r.settingsFile); err == nil {
		t.Fatal("setting the key wrote a settings file")
	}
}

func TestStartStopFlow(t *testing.T) {
	r := newUIRig(t)
	req := map[string]any{"server": "http://127.0.0.1:1", "ruleset": "pvp", "logPath": r.logPath, "key": fakeKey}
	code, body := r.do(t, "POST", "/api/start", req)
	if code != http.StatusAccepted {
		t.Fatalf("start: %d %s", code, body)
	}
	if strings.Contains(string(body), fakeKey) {
		t.Fatalf("start reply leaks the key: %s", body)
	}
	if r.session.uploader == nil || r.session.uploader.cfg.Key != fakeKey || r.session.uploader.cfg.Source != "test-pc" {
		t.Fatalf("uploader config %+v", r.session.uploader)
	}
	eventually(t, "running status", func() bool {
		_, b := r.do(t, "GET", "/api/status", nil)
		return decode[Status](t, b).Running
	})
	if code, _ := r.do(t, "POST", "/api/start", req); code != http.StatusConflict {
		t.Fatalf("second start: %d", code)
	}
	code, body = r.do(t, "POST", "/api/stop", nil)
	if code != 200 || decode[Status](t, body).State != StateIdle {
		t.Fatalf("stop: %d %s", code, body)
	}
	saved, err := os.ReadFile(r.settingsFile)
	if err != nil || strings.Contains(string(saved), fakeKey) || !strings.Contains(string(saved), "127.0.0.1:1") {
		t.Fatalf("settings file %q, %v", saved, err)
	}
}

func TestStartUsesTheEnvironmentKeyWhenNoneIsTyped(t *testing.T) {
	r := newUIRig(t)
	r.keys = NewKeyStore(envWith(fakeKey))
	r.ui.opts.Keys = r.keys
	code, body := r.do(t, "POST", "/api/start", map[string]any{"server": "http://127.0.0.1:1", "logPath": r.logPath})
	if code != http.StatusAccepted {
		t.Fatalf("start: %d %s", code, body)
	}
	if r.session.uploader.cfg.Key != fakeKey {
		t.Fatal("environment key not used")
	}
}

func TestStartRejectsInvalidSettings(t *testing.T) {
	r := newUIRig(t)
	code, body := r.do(t, "POST", "/api/start", map[string]any{"server": "http://127.0.0.1:1"})
	if code != http.StatusBadRequest || !strings.Contains(string(body), "log") {
		t.Fatalf("no log: %d %s", code, body)
	}
	code, _ = r.do(t, "POST", "/api/start", map[string]any{"server": "nope", "logPath": r.logPath})
	if code != http.StatusBadRequest {
		t.Fatalf("bad server: %d", code)
	}
}

func TestStartStillRunsWhenSettingsCannotBeSaved(t *testing.T) {
	r := newUIRig(t)
	blocker := filepath.Join(t.TempDir(), "file")
	writeLog(t, blocker, "x")
	r.ui.opts.SettingsPath = filepath.Join(blocker, "sub", "settings.json") // a file where a folder is needed
	code, body := r.do(t, "POST", "/api/start", map[string]any{"server": "http://127.0.0.1:1", "logPath": r.logPath})
	if code != http.StatusAccepted || decode[map[string]any](t, body)["warning"] == "" {
		t.Fatalf("start: %d %s", code, body)
	}
}

func TestStatusShape(t *testing.T) {
	r := newUIRig(t)
	_, body := r.do(t, "GET", "/api/status", nil)
	got := decode[map[string]any](t, body)
	for _, field := range []string{"state", "running", "error", "note", "file", "linesSent", "bytesSent", "linesPerSec",
		"lastUploadMs", "lastReply", "retryInSec", "queued", "dropped", "events"} {
		if _, ok := got[field]; !ok {
			t.Errorf("status lacks %q: %s", field, body)
		}
	}
	if got["state"] != StateIdle {
		t.Fatalf("state = %v", got["state"])
	}
}

func TestStatusReflectsSessionEvents(t *testing.T) {
	r := newUIRig(t)
	r.do(t, "POST", "/api/start", map[string]any{"server": "http://127.0.0.1:1", "logPath": r.logPath})
	eventually(t, "log open", func() bool { return r.session.session.Status().File != "" })
	r.session.uploader.emit(feed.Event{Kind: feed.EventSent, Lines: 9, Bytes: 90})
	_, body := r.do(t, "GET", "/api/status", nil)
	if st := decode[Status](t, body); st.State != StateUploading || st.LinesSent != 9 {
		t.Fatalf("status = %+v", st)
	}
}

func TestPickFile(t *testing.T) {
	r := newUIRig(t)
	r.picker.path = `C:\logs\WoWCombatLog.txt`
	code, body := r.do(t, "POST", "/api/pick-file", map[string]string{"current": r.logPath})
	if code != 200 || decode[map[string]any](t, body)["path"] != r.picker.path || r.picker.got.InitialPath != r.logPath {
		t.Fatalf("pick: %d %s (options %+v)", code, body, r.picker.got)
	}
	r.picker.err = ErrPickCancelled
	code, body = r.do(t, "POST", "/api/pick-file", `{}`)
	if code != 200 || decode[map[string]any](t, body)["cancelled"] != true {
		t.Fatalf("cancel: %d %s", code, body)
	}
	r.picker.err = ErrPickerUnsupported
	if code, _ = r.do(t, "POST", "/api/pick-file", `{}`); code != http.StatusNotImplemented {
		t.Fatalf("unsupported: %d", code)
	}
	r.picker.err = errors.New("boom")
	if code, _ = r.do(t, "POST", "/api/pick-file", `{}`); code != http.StatusInternalServerError {
		t.Fatalf("failure: %d", code)
	}
}

func TestQuit(t *testing.T) {
	r := newUIRig(t)
	if code, _ := r.do(t, "POST", "/api/quit", nil); code != http.StatusNoContent {
		t.Fatalf("quit: %d", code)
	}
	select {
	case <-r.quit:
	case <-time.After(5 * time.Second):
		t.Fatal("quit callback not called")
	}
	r.ui.opts.Quit = nil
	if code, _ := r.do(t, "POST", "/api/quit", nil); code != http.StatusNotFound {
		t.Fatalf("quit without callback: %d", code)
	}
}

func TestWrongMethodIsRefused(t *testing.T) {
	r := newUIRig(t)
	if code, _ := r.do(t, "DELETE", "/api/status", nil); code != http.StatusMethodNotAllowed {
		t.Fatalf("code %d", code)
	}
}

func TestServeBindsToLoopbackAndStopsWithContext(t *testing.T) {
	r := newUIRig(t)
	ln, err := ListenLocal(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ln.Addr().String(), "127.0.0.1:") {
		t.Fatalf("listening on %s", ln.Addr())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.ui.Serve(ctx, ln) }()
	url := PageURL(ln, testToken)
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, "/?t="+testToken) {
		t.Fatalf("url = %s", url)
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("page: %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

func TestListenLocalReportsBusyPort(t *testing.T) {
	ln, err := ListenLocal(0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p := 0
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	if _, err := ListenLocal(p); err == nil {
		t.Fatal("second listener on the same port succeeded")
	}
}

func TestNewTokenIsRandomAndLong(t *testing.T) {
	a, b := NewToken(), NewToken()
	if len(a) < 32 || a == b {
		t.Fatalf("tokens %q %q", a, b)
	}
}

func TestCanBrowse(t *testing.T) {
	if CanBrowse(UnsupportedPicker{}) || !CanBrowse(&fakePicker{}) {
		t.Fatal("CanBrowse misreports")
	}
}

// external matches anything that would load or send something to another origin.
var external = regexp.MustCompile(`(?i)((src|href|action)\s*=\s*["']?(https?:)?//|url\(\s*["']?(https?:)?//|fetch\(\s*["'` + "`" + `]https?:|from\s+["']https?:|import\(\s*["']https?:|WebSocket|XMLHttpRequest|sendBeacon|EventSource)`)

func TestEmbeddedUIHasNoExternalRequestsOrUnsafeSinks(t *testing.T) {
	for _, name := range []string{"index.html", "app.js", "logic.js", "style.css"} {
		data, err := webFiles.ReadFile("web/" + name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "localStorage", "sessionStorage", "document.cookie"} {
			if strings.Contains(text, bad) {
				t.Errorf("web/%s contains %q", name, bad)
			}
		}
		if external.MatchString(text) {
			t.Errorf("web/%s makes or references an external request: %q", name, external.FindString(text))
		}
		if name == "index.html" {
			if strings.Contains(text, " style=") || strings.Contains(strings.ReplaceAll(text, `<script type="module" src="app.js"></script>`, ""), "<script") {
				t.Errorf("index.html has inline script or style")
			}
			if !strings.Contains(text, `type="password"`) || !strings.Contains(text, `autocomplete="off"`) {
				t.Errorf("index.html: key field must be a non-autofilled password input")
			}
		}
	}
}

// TestKeyNeverAppearsInAnyReply: with a key typed and a run started, no endpoint, error body or the
// saved settings file contains it (docs/security/secrets-policy.md).
func TestKeyNeverAppearsInAnyReply(t *testing.T) {
	r := newUIRig(t)
	req := map[string]any{"server": "http://127.0.0.1:1", "ruleset": "pvp", "logPath": r.logPath, "key": fakeKey}
	r.do(t, "PUT", "/api/key", map[string]string{"key": fakeKey})
	r.do(t, "POST", "/api/start", req)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/status"}, {"GET", "/api/settings"}, {"GET", "/api/nope"}, {"DELETE", "/api/status"},
	} {
		if _, body := r.do(t, c.method, c.path, nil); strings.Contains(string(body), fakeKey) {
			t.Errorf("%s %s leaks the key: %s", c.method, c.path, body)
		}
	}
	// A bad request that carries the key in its body must not echo it back.
	bad := map[string]any{"server": "ftp://x", "ruleset": "nope", "logPath": r.logPath, "key": fakeKey}
	if _, body := r.do(t, "POST", "/api/start", bad); strings.Contains(string(body), fakeKey) {
		t.Errorf("error reply echoes the key: %s", body)
	}
	if saved, err := os.ReadFile(r.settingsFile); err == nil && strings.Contains(string(saved), fakeKey) {
		t.Errorf("settings file holds the key: %s", saved)
	}
}
