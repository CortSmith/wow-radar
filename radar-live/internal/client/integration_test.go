package client

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radarlive/cmd/radar-ingest/e2e"
	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// This test streams a synthetic log through the real Session and feed.Streamer into an in-process
// radar-ingest (the same module list as the binary) that relays to an in-process database.

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func noEnvironment(string) (string, bool) { return "", false }

// startTestDatabase serves the one endpoint the relay calls and applies pings to a memory store.
func startTestDatabase(t *testing.T) (url string, store core.Store) {
	t.Helper()
	host := core.NewHost(core.WithLogger(quietLogger()))
	if err := host.Register(e2e.DB()...); err != nil {
		t.Fatal(err)
	}
	store, err := host.Stores.Default()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ping domain.Ping
		if err := json.NewDecoder(r.Body).Decode(&ping); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		res, err := store.ApplyPing(r.Context(), ping)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		core.WriteJSON(w, http.StatusOK, res)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, store
}

// startTestIngest runs radar-ingest in-process on a free port and returns its base URL.
func startTestIngest(t *testing.T, dbURL string) string {
	t.Helper()
	host := core.NewHost(core.WithLogger(quietLogger()))
	if err := host.Register(e2e.Ingest()...); err != nil {
		t.Fatal(err)
	}
	args := []string{"--listen=127.0.0.1:0", "--logparse-allow-replay=true", "--db-url=" + dbURL, "--contactx-flush=100ms", "--bountyx-flush=100ms"}
	if err := host.Config.Load(args, noEnvironment); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Run(ctx) }()
	select {
	case <-host.Ready():
	case err := <-done:
		t.Fatalf("ingest stopped early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("ingest not ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("ingest did not stop")
		}
	})
	return "http://" + host.Addr()
}

func TestLiveLogReachesTheDatabaseThroughTheRealStack(t *testing.T) {
	dbURL, store := startTestDatabase(t)
	ingestURL := startTestIngest(t, dbURL)
	synthetic, err := os.ReadFile(filepath.Join("..", "mod", "logparse", "testdata", "session.log"))
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "WoWCombatLog.txt")
	writeLog(t, logPath, "")

	session := NewSession(SessionOptions{Poll: testPoll})
	defer session.Stop()
	err = session.Start(StartConfig{Settings: Settings{Server: ingestURL, LogPath: logPath, Ruleset: "pvp"}, Source: "integration-pc"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "log opened", func() bool { return session.Status().File != "" })

	appendLog(t, logPath, string(synthetic)) // the game "writes" the log after Start
	eventually(t, "lines uploaded", func() bool { return session.Status().LinesSent >= 10 })
	eventually(t, "contacts in the database", func() bool {
		res, err := store.Sweep(context.Background(), domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopeAll, ActiveWithinMs: 1 << 40})
		return err == nil && len(res.Contacts) >= 3
	})

	final := session.Stop()
	if final.State != StateIdle || !strings.Contains(final.LastReply, "lines") {
		t.Fatalf("after stop the server's summary should be shown: %+v", final)
	}
}

func TestDefaultSourceUsesTheComputerName(t *testing.T) {
	if got := DefaultSource("My PC"); got != "My-PC-client" {
		t.Fatalf("source = %q", got)
	}
	if got := DefaultSource(""); got != "client" {
		t.Fatalf("empty host: %q", got)
	}
}
