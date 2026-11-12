package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.port != 0 || cfg.noWindow || cfg.settingsPath != "" || cfg.showVersion {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseConfigFlags(t *testing.T) {
	cfg, err := parseConfig([]string{"--port", "8123", "--no-window", "--settings", "s.json", "--version"})
	if err != nil || cfg.port != 8123 || !cfg.noWindow || cfg.settingsPath != "s.json" || !cfg.showVersion {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}

func TestParseConfigRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{{"--port", "70000"}, {"--port", "-1"}, {"extra"}, {"--key", "x"}, {"--unknown"}} {
		if _, err := parseConfig(args); err == nil {
			t.Errorf("args %v accepted", args)
		}
	}
}

// syncBuffer is a bytes.Buffer safe for the writer goroutine and the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type harness struct {
	out      *syncBuffer
	done     chan error
	cancel   context.CancelFunc
	settings string
}

func envMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func startRun(t *testing.T, env map[string]string, extra []string, d deps) *harness {
	t.Helper()
	h := &harness{out: &syncBuffer{}, done: make(chan error, 1), settings: filepath.Join(t.TempDir(), "settings.json")}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	d.lookup, d.stdout, d.stderr = envMap(env), h.out, io.Discard
	if d.hostname == nil {
		d.hostname = func() (string, error) { return "test-pc", nil }
	}
	args := append([]string{"--no-window", "--settings", h.settings}, extra...)
	go func() { h.done <- run(ctx, args, d) }()
	return h
}

var urlLine = regexp.MustCompile(`http://127\.0\.0\.1:\d+/\?t=[0-9a-f]+`)

func (h *harness) pageURL(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if u := urlLine.FindString(h.out.String()); u != "" {
			return u
		}
		select {
		case err := <-h.done:
			t.Fatalf("run ended early: %v (output %q)", err, h.out.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	t.Fatalf("no UI address printed: %q", h.out.String())
	return ""
}

func (h *harness) stop(t *testing.T) {
	t.Helper()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not stop")
	}
}

func get(t *testing.T, url, token string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("X-Radar-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestNoWindowServesTheUIAndStopsOnCancel(t *testing.T) {
	h := startRun(t, nil, nil, deps{})
	url := h.pageURL(t)
	code, body := get(t, url, "")
	if code != 200 || !strings.Contains(string(body), "Ingest key") {
		t.Fatalf("page: %d", code)
	}
	h.stop(t)
}

func TestSettingsComeFromTheFileAndTheEnvironment(t *testing.T) {
	h := startRun(t, map[string]string{"RADAR_CLIENT_SERVER": "http://10.9.8.7:8788", "RADAR_CLIENT_INGEST_KEY": "test-key-0000000000000000"}, nil, deps{})
	url := h.pageURL(t)
	token := url[strings.Index(url, "t=")+2:]
	base := url[:strings.Index(url, "/?")]
	code, body := get(t, base+"/api/settings", token)
	if code != 200 {
		t.Fatalf("settings: %d", code)
	}
	var view map[string]any
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view["server"] != "http://10.9.8.7:8788" || view["keySource"] != "env" {
		t.Fatalf("view = %v", view)
	}
	if strings.Contains(string(body), "test-key-0000000000000000") || strings.Contains(h.out.String(), "test-key-0000000000000000") {
		t.Fatal("the key leaked into the API or the console")
	}
	h.stop(t)
}

func TestCorruptSettingsFileIsReportedNotFatal(t *testing.T) {
	h := &harness{out: &syncBuffer{}, done: make(chan error, 1), settings: filepath.Join(t.TempDir(), "settings.json")}
	if err := os.WriteFile(h.settings, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	d := deps{lookup: envMap(nil), stdout: h.out, stderr: io.Discard, hostname: func() (string, error) { return "pc", nil }}
	go func() { h.done <- run(ctx, []string{"--no-window", "--settings", h.settings}, d) }()
	url := h.pageURL(t)
	token := url[strings.Index(url, "t=")+2:]
	_, body := get(t, url[:strings.Index(url, "/?")]+"/api/settings", token)
	if !strings.Contains(string(body), "not valid JSON") {
		t.Fatalf("no warning in %s", body)
	}
	h.stop(t)
}

func TestWindowClosingEndsTheProgram(t *testing.T) {
	var shownURL string
	d := deps{window: func(_ context.Context, url, _ string) error { shownURL = url; return nil }}
	h := &harness{out: &syncBuffer{}, done: make(chan error, 1)}
	d.lookup, d.stdout, d.stderr = envMap(nil), h.out, io.Discard
	d.hostname = func() (string, error) { return "pc", nil }
	go func() {
		h.done <- run(context.Background(), []string{"--settings", filepath.Join(t.TempDir(), "s.json")}, d)
	}()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("program kept running after the window closed")
	}
	if !urlLine.MatchString(shownURL) {
		t.Fatalf("window opened %q", shownURL)
	}
}

func TestBrowserFallbackWhenNoWindowIsAvailable(t *testing.T) {
	opened := make(chan string, 1)
	d := deps{
		window:  func(context.Context, string, string) error { return errors.New("no WebView2") },
		browser: func(url string) error { opened <- url; return nil },
	}
	h := startRunWithoutNoWindow(t, d)
	select {
	case url := <-opened:
		if !urlLine.MatchString(url) {
			t.Fatalf("browser opened %q", url)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("browser not opened")
	}
	h.stop(t)
}

func startRunWithoutNoWindow(t *testing.T, d deps) *harness {
	t.Helper()
	h := &harness{out: &syncBuffer{}, done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	d.lookup, d.stdout, d.stderr = envMap(nil), h.out, io.Discard
	d.hostname = func() (string, error) { return "pc", nil }
	go func() { h.done <- run(ctx, []string{"--settings", filepath.Join(t.TempDir(), "s.json")}, d) }()
	return h
}

func TestQuitButtonEndsTheProgram(t *testing.T) {
	h := startRun(t, nil, nil, deps{})
	url := h.pageURL(t)
	token := url[strings.Index(url, "t=")+2:]
	req, _ := http.NewRequest("POST", url[:strings.Index(url, "/?")]+"/api/quit", nil)
	req.Header.Set("X-Radar-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("quit did not end the program")
	}
}

func TestVersionFlagPrintsAndExits(t *testing.T) {
	out := &syncBuffer{}
	err := run(context.Background(), []string{"--version"}, deps{lookup: envMap(nil), stdout: out, stderr: io.Discard})
	if err != nil || !strings.Contains(out.String(), "radar-client") {
		t.Fatalf("out %q err %v", out.String(), err)
	}
}

func TestBusyPortIsAnError(t *testing.T) {
	h := startRun(t, nil, nil, deps{})
	url := h.pageURL(t)
	port := regexp.MustCompile(`:(\d+)/`).FindStringSubmatch(url)[1]
	d := deps{lookup: envMap(nil), stdout: io.Discard, stderr: io.Discard, hostname: func() (string, error) { return "pc", nil }}
	err := run(context.Background(), []string{"--no-window", "--port", port, "--settings", filepath.Join(t.TempDir(), "s.json")}, d)
	if err == nil {
		t.Fatal("second instance on the same port started")
	}
	h.stop(t)
}

func TestSecretFlagsAreRefusedWithAPointerToTheEnvVar(t *testing.T) {
	for _, args := range [][]string{{"--key", "test-key-0000000000000000"}, {"--ingest-key=x"}} {
		_, err := parseConfig(args)
		if err == nil || !strings.Contains(err.Error(), "RADAR_CLIENT_INGEST_KEY") || strings.Contains(err.Error(), "0000") {
			t.Errorf("args %v: err = %v", args, err)
		}
	}
}
