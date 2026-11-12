package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fail2banRegex is the filter documented in docs/security/logging.md (failregex, <HOST> = the ip group).
var fail2banRegex = regexp.MustCompile(`^\{"ts":"[^"]+","event":"(?:auth_failure|rate_limited)","ip":"(?P<host>[0-9a-fA-F:.]+)"`)

// TestSecurityLogMetricsAndRedactionThroughTheRealModules drives the real module set: brute force a key,
// flood a route, then check the log lines, the fail2ban filter, the metrics and that no key leaked.
func TestSecurityLogMetricsAndRedactionThroughTheRealModules(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "security.log")
	env := map[string]string{"RADAR_INGEST_KEYS": ingestKey, "RADAR_VIEW_KEYS": viewKey, "RADAR_AUDIT_LOG": logPath}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	host, err := buildHost([]string{"--store", "sqlite", "--db-path", filepath.Join(dir, "x.db"), "--listen", "127.0.0.1:0",
		"--internal-listen", "127.0.0.1:0", "--abuse-ban-loopback=true", "--ratelimit-sweep-rate", "30", "--ratelimit-sweep-burst", "3"}, lookup, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	s := runHost(t, host)

	if code, _ := s.doPublic("GET", "/internal/metrics", ""); code != http.StatusNotFound {
		t.Fatalf("public listener answered /internal/metrics with %d", code)
	}
	for i := 0; i < 8; i++ { // over the sweep budget with a valid key
		s.doPublic("GET", "/api/v1/sweep?map=1&x=1&y=1", "", "X-View-Key", viewKey)
	}
	const wrong = "wrong-key-0000000000"
	for i := 0; i < 6; i++ { // brute force with the wrong key in a header, the query and the body
		s.doPublic("GET", "/api/v1/status?key="+wrong, `{"key":"`+wrong+`"}`, "X-View-Key", wrong, "Authorization", "Bearer "+wrong)
	}
	s.doPublic("GET", "/api/v1/status", "", "X-View-Key", viewKey) // banned now: refused before auth
	s.stop()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	var authLines, banLines, limited int
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		switch m["event"] {
		case "auth_failure":
			authLines++
			if m["ip"] != "127.0.0.1" || m["reason"] != "bad_key" || !fail2banRegex.MatchString(line) ||
				fail2banRegex.FindStringSubmatch(line)[1] != "127.0.0.1" {
				t.Fatalf("auth_failure line not fail2ban-ready: %s", line)
			}
		case "rate_limited":
			limited++
			if m["class"] != "sweep" || m["ip"] != "127.0.0.1" || !fail2banRegex.MatchString(line) {
				t.Fatalf("rate_limited line: %s", line)
			}
		case "ban":
			banLines++
		}
	}
	if authLines < 1 || banLines != 1 || limited < 1 {
		t.Fatalf("auth_failure %d, ban %d, rate_limited %d:\n%s", authLines, banLines, limited, log)
	}
	for _, secret := range []string{wrong, viewKey, ingestKey, "Bearer", "X-View-Key", "key="} {
		if strings.Contains(log, secret) {
			t.Fatalf("security log contains %q:\n%s", secret, log)
		}
	}
}

// TestMetricsEndpointOnInternalListenerOnly checks the scrape output of a live server and its secrecy.
func TestMetricsEndpointOnInternalListenerOnly(t *testing.T) {
	s := startServer(t, "--store", "sqlite", "--db-path", filepath.Join(t.TempDir(), "x.db"),
		"--ingest-keys", ingestKey, "--view-keys", viewKey)
	s.doPublic("GET", "/api/v1/status", "", "X-View-Key", "wrong-key-0000000000")
	if code, _ := s.doPublic("GET", "/internal/metrics", ""); code != http.StatusNotFound {
		t.Fatalf("public /internal/metrics = %d, want 404", code)
	}
	code, body := s.do("GET", "/internal/metrics", "")
	if code != http.StatusOK {
		t.Fatalf("internal metrics = %d", code)
	}
	text := string(body)
	for _, want := range []string{"radar_http_requests_total{", "radar_http_429_total", "radar_auth_failures_total", "radar_bans_total",
		"radar_inflight_requests{", "radar_connections{", "radar_db_size_bytes", "radar_db_integrity_degraded 0",
		"radar_db_last_write_age_seconds", "radar_bans_active"} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	for _, secret := range []string{viewKey, ingestKey, "wrong-key"} {
		if strings.Contains(text, secret) {
			t.Fatalf("metrics contain %q", secret)
		}
	}
}

// TestAuditModuleIsRemovable: without the audit module the server runs and /internal/metrics is a 404.
func TestAuditModuleIsRemovable(t *testing.T) {
	s := startServerWithout(t, []string{"audit"}, "--store", "memstore")
	if code, _ := s.do("GET", "/internal/metrics", ""); code != http.StatusNotFound {
		t.Fatalf("metrics without the module = %d", code)
	}
	if res, code := s.ping("a1"); code != 200 || res.Received != 0 {
		t.Fatalf("server unhealthy without audit: %d %+v", code, res)
	}
}

// TestValidationRejectionsAreLogged: a bad query and a bad ping leave validation_reject lines, without the payload.
func TestValidationRejectionsAreLogged(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "security.log")
	env := map[string]string{"RADAR_INGEST_KEYS": ingestKey, "RADAR_VIEW_KEYS": viewKey, "RADAR_AUDIT_LOG": logPath}
	host, err := buildHost([]string{"--store", "memstore", "--listen", "127.0.0.1:0", "--internal-listen", "127.0.0.1:0"},
		func(k string) (string, bool) { v, ok := env[k]; return v, ok }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	s := runHost(t, host)
	if code, _ := s.doPublic("GET", "/api/v1/sweep?rs=bogus-ruleset-zzz", "", "X-View-Key", viewKey); code != http.StatusBadRequest {
		t.Fatalf("bad query = %d", code)
	}
	if code, _ := s.do("POST", "/internal/v1/ping", `{"payload-marker-qq":1}`, "X-Ingest-Key", ingestKey); code != http.StatusBadRequest {
		t.Fatalf("bad ping = %d", code)
	}
	s.stop()
	data, _ := os.ReadFile(logPath)
	log := string(data)
	for _, want := range []string{`"event":"validation_reject","ip":"127.0.0.1","class":"public","reason":"query_invalid"`,
		`"event":"validation_reject","ip":"127.0.0.1","class":"internal","reason":"ping_invalid"`} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %s:\n%s", want, log)
		}
	}
	for _, leak := range []string{"bogus-ruleset-zzz", "payload-marker-qq", viewKey, ingestKey} {
		if strings.Contains(log, leak) {
			t.Fatalf("log contains %q", leak)
		}
	}
}
