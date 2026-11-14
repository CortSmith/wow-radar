package keys_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/keyring"
	"radarlive/internal/mod/keys"
)

const (
	ringKey  = "keyring-key-0123456789"
	ringKey2 = "keyring-key-second-0123"
	ringMail = "someone@example.com"
)

type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

var mtimeTick atomic.Int64

// writeRing saves a keyring and bumps the mtime so every write is seen as a change.
func writeRing(t *testing.T, path string, entries ...keyring.Entry) {
	t.Helper()
	if err := keyring.Save(path, &keyring.Keyring{Version: keyring.Version, Keys: entries}); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(time.Duration(mtimeTick.Add(1)) * time.Second)
	_ = os.Chtimes(path, ts, ts)
}

func ringStart(t *testing.T, path string, env map[string]string) (*core.Host, *syncWriter, error) {
	t.Helper()
	logs := &syncWriter{}
	host := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	if err := host.Register(keys.New(keys.WithKeyringPoll(5 * time.Millisecond))); err != nil {
		t.Fatal(err)
	}
	e := map[string]string{"RADAR_KEYRING_FILE": path}
	for k, v := range env {
		e[k] = v
	}
	if err := host.Config.Load(nil, func(k string) (string, bool) { v, ok := e[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	err := host.Hooks.RunStart(context.Background())
	t.Cleanup(func() { _ = host.Hooks.RunStop(context.Background()) })
	return host, logs, err
}

func ingestCall(host *core.Host, key string) (int, string, core.KeyInfo, bool) {
	var info core.KeyInfo
	var got bool
	h := host.Auth.Require(core.RoleIngest, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		info, got = core.KeyInfoFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	if key != "" {
		req.Header.Set("X-Ingest-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), info, got
}

func waitStatus(t *testing.T, host *core.Host, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, _, _, _ := ingestCall(host, key)
		if code == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %d, want %d", code, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestKeyringAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	now := time.Now()
	past := now.Add(-time.Hour)
	disabled := keyring.NewEntry("disabled-key-0123456789", ringMail, now)
	disabled.Disabled = true
	expired := keyring.NewEntry("expired-key-0123456789", ringMail, now)
	expired.ExpiresAt = &past
	rate := int64(7)
	good := keyring.NewEntry(ringKey, ringMail, now)
	good.Limits.LineRate = &rate
	writeRing(t, path, good, disabled, expired)
	host, _, err := ringStart(t, path, map[string]string{"RADAR_INGEST_KEYS": ingestKey})
	if err != nil {
		t.Fatal(err)
	}
	code, _, info, got := ingestCall(host, ringKey)
	if code != 200 || !got || !info.FromKeyring || info.ID != keyring.IDFromKey(ringKey) || info.Limits.LineRate == nil || *info.Limits.LineRate != 7 {
		t.Fatalf("keyring key: code=%d info=%+v got=%v", code, info, got)
	}
	code, _, info, got = ingestCall(host, ingestKey)
	if code != 200 || !got || info.FromKeyring || info.ID != keyring.IDFromKey(ingestKey) {
		t.Fatalf("env key: code=%d info=%+v", code, info)
	}
	for _, k := range []string{"disabled-key-0123456789", "expired-key-0123456789"} {
		code, body, _, _ := ingestCall(host, k)
		if code != 403 || !strings.Contains(body, "key_disabled") {
			t.Fatalf("%s: %d %s", k, code, body)
		}
	}
	code, body, _, _ := ingestCall(host, "unknown-key-0123456789")
	if code != 401 || !strings.Contains(body, "unauthorized") {
		t.Fatalf("unknown: %d %s", code, body)
	}
	if code, _, _, _ := ingestCall(host, ""); code != 401 {
		t.Fatalf("no key: %d", code)
	}
}

func TestKeyringHotReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	now := time.Now()
	e1 := keyring.NewEntry(ringKey, ringMail, now)
	writeRing(t, path, e1)
	host, logs, err := ringStart(t, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, host, ringKey, 200)
	waitStatus(t, host, ringKey2, 401)

	e2 := keyring.NewEntry(ringKey2, ringMail, now)
	writeRing(t, path, e1, e2)
	waitStatus(t, host, ringKey2, 200)

	e2.Disabled = true
	writeRing(t, path, e1, e2)
	waitStatus(t, host, ringKey2, 403)

	e2.Disabled = false
	writeRing(t, path, e1, e2)
	waitStatus(t, host, ringKey2, 200)

	// a corrupt file keeps the previous set
	if err := os.WriteFile(path, []byte("{not json "+ringMail), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(time.Duration(mtimeTick.Add(1)) * time.Second)
	_ = os.Chtimes(path, ts, ts)
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(logs.String(), "invalid") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), "invalid") {
		t.Fatal("corrupt file not noticed")
	}
	waitStatus(t, host, ringKey, 200)
	waitStatus(t, host, ringKey2, 200)

	out := logs.String()
	if strings.Contains(out, ringMail) || strings.Contains(out, ringKey) || strings.Contains(out, ringKey2) {
		t.Fatalf("log leaks: %s", out)
	}
}

func TestKeyringMissingAtStartThenCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	host, _, err := ringStart(t, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _, _ := ingestCall(host, ringKey); code != 401 {
		t.Fatalf("empty keyring must reject, got %d", code)
	}
	writeRing(t, path, keyring.NewEntry(ringKey, ringMail, time.Now()))
	waitStatus(t, host, ringKey, 200)
}

func TestKeyringEmptyFileRejectsAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	writeRing(t, path)
	host, _, err := ringStart(t, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !host.Auth.HasAuthenticator(core.RoleIngest) {
		t.Fatal("ingest role must not be open")
	}
	if code, _, _, _ := ingestCall(host, ringKey); code != 401 {
		t.Fatalf("code = %d", code)
	}
}

func TestKeyringCorruptAtStartFailsWithoutEcho(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, []byte("secret-contents "+ringMail), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := ringStart(t, path, nil)
	if err == nil || strings.Contains(err.Error(), "secret-contents") || strings.Contains(err.Error(), ringMail) {
		t.Fatalf("err = %v", err)
	}
}

func TestKeyringOffByDefault(t *testing.T) {
	host, _, err := start(t)
	if err != nil {
		t.Fatal(err)
	}
	if host.Auth.HasAuthenticator(core.RoleIngest) {
		t.Fatal("ingest must stay open without keys or keyring-file")
	}
}

func TestKeyringLookupFollowsHotReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	e := keyring.NewEntry(ringKey, ringMail, time.Now())
	writeRing(t, path, e)
	host, _, err := ringStart(t, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := keyring.IDFromKey(ringKey)
	if info, active, ok := host.Auth.LookupKey(id); !ok || !active || info.ID != id || !info.FromKeyring {
		t.Fatalf("lookup: %+v active=%v ok=%v", info, active, ok)
	}
	e.Disabled = true
	writeRing(t, path, e)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, active, _ := host.Auth.LookupKey(id); !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disabled key still active")
		}
		time.Sleep(5 * time.Millisecond)
	}
	writeRing(t, path)
	for {
		if _, active, _ := host.Auth.LookupKey(id); !active {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
}
