package keys_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radarlive/internal/core"
	"radarlive/internal/mod/keys"
)

const (
	viewKey   = "view-key-0123456789"
	viewKey2  = "second-view-key-0123"
	ingestKey = "ingest-key-0123456789"
)

// start builds a host with the keys module, loads args, runs start hooks and returns it plus a log buffer.
func start(t *testing.T, args ...string) (*core.Host, *bytes.Buffer, error) {
	t.Helper()
	logs := &bytes.Buffer{}
	host := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	if err := host.Register(keys.New()); err != nil {
		t.Fatal(err)
	}
	args, lookup := secretsToEnv(args)
	if err := host.Config.Load(args, lookup); err != nil {
		t.Fatal(err)
	}
	return host, logs, host.Hooks.RunStart(context.Background())
}

// status calls a handler protected by role with the given headers.
func status(host *core.Host, role core.Role, headers map[string]string) int {
	h := host.Auth.Require(role, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestAuthMatrix(t *testing.T) {
	host, _, err := start(t, "--view-keys", viewKey+","+viewKey2, "--ingest-keys", ingestKey)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		role    core.Role
		headers map[string]string
		want    int
	}{
		{"view no key", core.RoleView, nil, 401},
		{"view wrong key", core.RoleView, map[string]string{"X-View-Key": "nope-nope-nope-nope"}, 401},
		{"view right key", core.RoleView, map[string]string{"X-View-Key": viewKey}, 200},
		{"view second key", core.RoleView, map[string]string{"X-View-Key": viewKey2}, 200},
		{"view bearer", core.RoleView, map[string]string{"Authorization": "Bearer " + viewKey}, 200},
		{"view bearer wrong", core.RoleView, map[string]string{"Authorization": "Bearer x" + viewKey}, 401},
		{"view basic scheme ignored", core.RoleView, map[string]string{"Authorization": "Basic " + viewKey}, 401},
		{"view prefix of key", core.RoleView, map[string]string{"X-View-Key": viewKey[:10]}, 401},
		{"view with ingest key", core.RoleView, map[string]string{"X-View-Key": ingestKey}, 401},
		{"view with ingest header", core.RoleView, map[string]string{"X-Ingest-Key": viewKey}, 401},
		{"ingest right key", core.RoleIngest, map[string]string{"X-Ingest-Key": ingestKey}, 200},
		{"ingest bearer", core.RoleIngest, map[string]string{"Authorization": "Bearer " + ingestKey}, 200},
		{"ingest with view key", core.RoleIngest, map[string]string{"X-Ingest-Key": viewKey}, 401},
		{"ingest view header", core.RoleIngest, map[string]string{"X-View-Key": ingestKey}, 401},
		{"ingest none", core.RoleIngest, nil, 401},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := status(host, tc.role, tc.headers); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNoKeysMeansOpen(t *testing.T) {
	host, logs, err := start(t)
	if err != nil {
		t.Fatal(err)
	}
	if got := status(host, core.RoleView, nil); got != 200 {
		t.Fatalf("view status = %d, want 200 (open)", got)
	}
	if got := status(host, core.RoleIngest, nil); got != 200 {
		t.Fatalf("ingest status = %d, want 200 (open)", got)
	}
	if !strings.Contains(logs.String(), "open") {
		t.Fatalf("expected the core's open warning, log: %q", logs.String())
	}
}

func TestOnlyOneRoleConfigured(t *testing.T) {
	host, _, err := start(t, "--ingest-keys", ingestKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := status(host, core.RoleView, nil); got != 200 {
		t.Fatalf("view should stay open, got %d", got)
	}
	if got := status(host, core.RoleIngest, nil); got != 401 {
		t.Fatalf("ingest should be closed, got %d", got)
	}
}

func TestKeysFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "view.keys")
	content := "# comment\n" + viewKey + "\n\n  " + viewKey2 + "  \n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	host, _, err := start(t, "--view-keys-file", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{viewKey, viewKey2} {
		if got := status(host, core.RoleView, map[string]string{"X-View-Key": k}); got != 200 {
			t.Fatalf("key from file rejected: %d", got)
		}
	}
	if got := status(host, core.RoleView, map[string]string{"X-View-Key": "# comment"}); got != 401 {
		t.Fatalf("comment line must not be a key, got %d", got)
	}
}

func TestStartErrorsNeverLeakKeys(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	tests := []struct {
		name string
		args []string
	}{
		{"short view key", []string{"--view-keys", "short-secret"}},
		{"short ingest key", []string{"--ingest-keys", ingestKey + ",tiny-secret"}},
		{"missing file", []string{"--view-keys-file", missing}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, logs, err := start(t, tc.args...)
			if err == nil {
				t.Fatal("expected an error")
			}
			text := err.Error() + logs.String()
			for _, secret := range []string{"short-secret", "tiny-secret", ingestKey} {
				if strings.Contains(text, secret) {
					t.Fatalf("secret %q leaked: %s", secret, text)
				}
			}
		})
	}
}

func TestGenerateKey(t *testing.T) {
	a, err := keys.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := keys.GenerateKey()
	raw, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil || len(raw) != 32 {
		t.Fatalf("key %q is not 32 bytes of base64url (err %v, len %d)", a, err, len(raw))
	}
	if a == b {
		t.Fatal("two generated keys are equal")
	}
	if len(a) < keys.MinKeyLen {
		t.Fatalf("generated key shorter than MinKeyLen")
	}
}

func TestModuleName(t *testing.T) {
	if keys.New().Name() != "keys" {
		t.Fatal("unexpected module name")
	}
}

// failure captures everything a client can observe of a rejected request.
func failure(host *core.Host, role core.Role, headers map[string]string) (int, string, string) {
	h := host.Auth.Require(role, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header().Get("Content-Type") + "|" + rec.Header().Get("WWW-Authenticate")
}

// TestFailedAuthIsUniform: a missing key, a wrong key, a key of the other role and a huge key all
// produce byte-identical responses, for the view and the ingest role alike, so a response never
// reveals whether a key exists or which role it belongs to.
func TestFailedAuthIsUniform(t *testing.T) {
	host, _, err := start(t, "--view-keys", viewKey, "--ingest-keys", ingestKey)
	if err != nil {
		t.Fatal(err)
	}
	type attempt struct {
		role    core.Role
		headers map[string]string
	}
	attempts := []attempt{
		{core.RoleView, nil},
		{core.RoleView, map[string]string{"X-View-Key": "wrong-wrong-wrong-wrong"}},
		{core.RoleView, map[string]string{"X-View-Key": ingestKey}},
		{core.RoleView, map[string]string{"X-View-Key": strings.Repeat("a", 100000)}},
		{core.RoleView, map[string]string{"X-View-Key": "x"}},
		{core.RoleIngest, nil},
		{core.RoleIngest, map[string]string{"X-Ingest-Key": "wrong-wrong-wrong-wrong"}},
		{core.RoleIngest, map[string]string{"X-Ingest-Key": viewKey}},
		{core.RoleIngest, map[string]string{"Authorization": "Bearer " + viewKey}},
	}
	code0, body0, hdr0 := failure(host, attempts[0].role, attempts[0].headers)
	if code0 != http.StatusUnauthorized {
		t.Fatalf("status %d", code0)
	}
	for i, a := range attempts[1:] {
		code, body, hdr := failure(host, a.role, a.headers)
		if code != code0 || body != body0 || hdr != hdr0 {
			t.Errorf("attempt %d differs: %d %q %q vs %d %q %q", i+1, code, body, hdr, code0, body0, hdr0)
		}
	}
	for _, leak := range []string{"view", "ingest", "role", "exists"} {
		if strings.Contains(strings.ToLower(body0), leak) {
			t.Errorf("failure body %q mentions %q", body0, leak)
		}
	}
}

// TestKeyCompareUsesConstantTimeOnEqualLengthHashes guards the implementation choice: keys are
// reduced to fixed-size SHA-256 digests and only the digests are compared, with
// subtle.ConstantTimeCompare, never with == or bytes.Equal on the secrets.
func TestKeyCompareUsesConstantTimeOnEqualLengthHashes(t *testing.T) {
	src, err := os.ReadFile("keys.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, want := range []string{"subtle.ConstantTimeCompare(sum[:], h[:])", "[sha256.Size]byte"} {
		if !strings.Contains(text, want) {
			t.Errorf("keys.go must contain %q", want)
		}
	}
	for _, banned := range []string{"bytes.Equal", "hmac.Equal", "== h", "== k", "== key", "strings.EqualFold"} {
		if strings.Contains(text, banned) {
			t.Errorf("keys.go must not compare secrets with %q", banned)
		}
	}
}

// secretsToEnv moves --view-keys/--ingest-keys out of args into an environment lookup: secrets are not flags.
func secretsToEnv(args []string) ([]string, func(string) (string, bool)) {
	env := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ingest-keys":
			env["RADAR_INGEST_KEYS"] = args[i+1]
			i++
		case "--view-keys":
			env["RADAR_VIEW_KEYS"] = args[i+1]
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func TestKeysCannotBeGivenAsFlags(t *testing.T) {
	for _, flag := range []string{"--view-keys", "--ingest-keys"} {
		host := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))))
		if err := host.Register(keys.New()); err != nil {
			t.Fatal(err)
		}
		err := host.Config.Load([]string{flag + "=" + viewKey}, func(string) (string, bool) { return "", false })
		if err == nil || !strings.Contains(err.Error(), "RADAR_") || strings.Contains(err.Error(), viewKey) {
			t.Fatalf("%s: err = %v", flag, err)
		}
	}
}

func TestPlaceholderKeysAreRefused(t *testing.T) {
	_, _, err := start(t, "--view-keys", "CHANGE_ME_VIEW_KEY")
	if err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("placeholder accepted: %v", err)
	}
}
