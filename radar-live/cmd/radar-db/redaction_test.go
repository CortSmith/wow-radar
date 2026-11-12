package main

import (
	"bytes"
	"strings"
	"testing"
)

// syncLog is a log sink the server goroutines and the test can share.
type syncLog struct {
	mu  chan struct{}
	buf bytes.Buffer
}

func newSyncLog() *syncLog { l := &syncLog{mu: make(chan struct{}, 1)}; l.mu <- struct{}{}; return l }
func (l *syncLog) Write(p []byte) (int, error) {
	<-l.mu
	defer func() { l.mu <- struct{}{} }()
	return l.buf.Write(p)
}
func (l *syncLog) String() string {
	<-l.mu
	defer func() { l.mu <- struct{}{} }()
	return l.buf.String()
}

// TestKeysNeverAppearInLogsStatusOrErrors: right and wrong keys go through the server; neither the
// configured keys nor the guessed ones show up in the log, the status body or any error body.
func TestKeysNeverAppearInLogsStatusOrErrors(t *testing.T) {
	logs := newSyncLog()
	args, lookup := secretsToEnv([]string{"--store", "memstore", "--listen", "127.0.0.1:0", "--internal-listen", "127.0.0.1:0",
		"--ingest-keys", ingestKey, "--view-keys", viewKey})
	host, err := buildHost(args, lookup, logs)
	if err != nil {
		t.Fatal(err)
	}
	s := runHost(t, host)
	guess := "guessed-key-0123456789"
	var bodies []string
	collect := func(code int, b []byte) { bodies = append(bodies, string(b)) }
	collect(s.doPublic("GET", "/api/v1/status", "", "X-View-Key", viewKey))
	collect(s.doPublic("GET", "/api/v1/status", "", "X-View-Key", guess))
	collect(s.doPublic("GET", "/api/v1/status", "", "Authorization", "Bearer "+guess))
	collect(s.do("POST", "/internal/v1/ping", `{}`, "X-Ingest-Key", guess))
	collect(s.do("POST", "/internal/v1/ping", `{"bad":`, "X-Ingest-Key", ingestKey))
	s.stop()
	all := strings.Join(append(bodies, logs.String()), "\n")
	for _, secret := range []string{ingestKey, viewKey, guess} {
		if strings.Contains(all, secret) {
			t.Errorf("%q leaked in logs or replies:\n%s", secret, all)
		}
	}
}

func TestHelpDoesNotPrintKeys(t *testing.T) {
	_, lookup := secretsToEnv([]string{"--ingest-keys", ingestKey, "--view-keys", viewKey})
	_, err := buildHost([]string{"--store", "memstore", "--help"}, lookup, newSyncLog())
	if err == nil || strings.Contains(err.Error(), ingestKey) || strings.Contains(err.Error(), viewKey) || !strings.Contains(err.Error(), "RADAR_VIEW_KEYS") {
		t.Fatalf("help: %v", err)
	}
}
