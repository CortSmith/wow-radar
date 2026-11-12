package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(nil, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ingestURL != "http://127.0.0.1:8788" || cfg.logs != defaultLogs || cfg.pattern != "WoWCombatLog*.txt" {
		t.Fatalf("defaults: %+v", cfg)
	}
	if cfg.maxBytesPerSec != 262144 || cfg.queueLines != 200000 || cfg.fromStart || cfg.key != "" {
		t.Fatalf("defaults: %+v", cfg)
	}
	if cfg.source == "" || strings.ContainsAny(cfg.source, " ()") {
		t.Fatalf("source = %q", cfg.source)
	}
}

func TestParseConfigFlagsAndEnv(t *testing.T) {
	cfg, err := parseConfig([]string{"--logs", "/tmp/l", "--from-start", "--source", "My PC", "--max-bytes-per-sec=0", "--rs", "pvp"},
		env(map[string]string{"RADAR_FEED_KEY": "envkey-envkey-envkey", "RADAR_FEED_INGEST_URL": "http://10.0.0.5:8788", "RADAR_FEED_LOGS": "/ignored"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logs != "/tmp/l" || !cfg.fromStart || cfg.source != "My-PC" || cfg.maxBytesPerSec != 0 || cfg.rs != "pvp" {
		t.Fatalf("%+v", cfg)
	}
	if cfg.key != "envkey-envkey-envkey" || cfg.ingestURL != "http://10.0.0.5:8788" {
		t.Fatalf("env not applied: %+v", cfg)
	}
}

func TestParseConfigKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("  file-key-file-key-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig([]string{"--key-file", path}, env(map[string]string{"RADAR_FEED_KEY": "env"}))
	if err != nil || cfg.key != "file-key-file-key-1" {
		t.Fatalf("key %q err %v", cfg.key, err)
	}
	if _, err := parseConfig([]string{"--key-file", path + ".missing"}, env(nil)); err == nil {
		t.Fatal("missing key file accepted")
	}
}

func TestParseConfigRejects(t *testing.T) {
	for _, args := range [][]string{
		{"--nope"},
		{"--ingest-url", "ftp://x"},
		{"--queue-lines", "0"},
		{"--max-bytes-per-sec", "-5"},
		{"--poll", "0s"},
		{"--rs", "bogus"},
		{"extra"},
	} {
		if _, err := parseConfig(args, env(nil)); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

// sink is a fake ingest endpoint.
type sink struct {
	mu    sync.Mutex
	lines []string
	key   string
}

func (s *sink) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.key = r.Header.Get("X-Ingest-Key")
	s.mu.Unlock()
	sc := bufio.NewScanner(r.Body)
	for sc.Scan() {
		s.mu.Lock()
		s.lines = append(s.lines, sc.Text())
		s.mu.Unlock()
	}
}

func (s *sink) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

func TestRunStreamsNewLogLines(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "WoWCombatLog-110526_200000.txt")
	if err := os.WriteFile(logPath, []byte("old line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &sink{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--logs", dir, "--ingest-url", srv.URL, "--poll", "5ms"}, env(map[string]string{"RADAR_FEED_KEY": "feeder-key-feeder-key"}), &out)
	}()
	time.Sleep(100 * time.Millisecond)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("new line one\nnew line two\n")
	f.Close()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(s.snapshot()) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	got := s.snapshot()
	if len(got) != 2 || got[0] != "new line one" || got[1] != "new line two" {
		t.Fatalf("got %v (the old line must not be sent)", got)
	}
	if s.key != "feeder-key-feeder-key" {
		t.Fatalf("key = %q", s.key)
	}
	if strings.Contains(out.String(), "feeder-key") {
		t.Fatalf("the key was logged: %s", out.String())
	}
}

func TestRunReportsBadFlags(t *testing.T) {
	err := run(context.Background(), []string{"--ingest-url", "nope"}, env(nil), io.Discard)
	if err == nil {
		t.Fatal("want an error")
	}
}

const fakeFeedKey = "test-key-0000000000000000"

func TestKeyFlagIsRefused(t *testing.T) {
	for _, args := range [][]string{{"--key", fakeFeedKey}, {"-key=" + fakeFeedKey}} {
		_, err := parseConfig(args, env(nil))
		if err == nil || !strings.Contains(err.Error(), "RADAR_FEED_KEY") || strings.Contains(err.Error(), fakeFeedKey) {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
}

func TestKeyFileComesFromEnvVar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(fakeFeedKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig(nil, env(map[string]string{"RADAR_FEED_KEY_FILE": path}))
	if err != nil || cfg.key != fakeFeedKey {
		t.Fatalf("key %q err %v", cfg.key, err)
	}
}

func TestHelpShowsNoSecretValue(t *testing.T) {
	_, err := parseConfig([]string{"--help"}, env(map[string]string{"RADAR_FEED_KEY": fakeFeedKey}))
	if err == nil || strings.Contains(err.Error(), fakeFeedKey) || !strings.Contains(err.Error(), "RADAR_FEED_KEY") || !strings.Contains(err.Error(), "-logs") {
		t.Fatalf("help = %v", err)
	}
}

func TestRunNeverLogsTheKey(t *testing.T) {
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_ = run(ctx, []string{"--logs", t.TempDir(), "--ingest-url", "http://127.0.0.1:1", "--poll", "5ms"}, env(map[string]string{"RADAR_FEED_KEY": fakeFeedKey}), &out)
	if strings.Contains(out.String(), fakeFeedKey) {
		t.Fatalf("log leaks the key: %s", out.String())
	}
}
