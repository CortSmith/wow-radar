package core

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAddrIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8787": true, "[::1]:8787": true, "localhost:1": true, "127.5.5.5:1": true,
		":8787": false, "0.0.0.0:1": false, "[::]:1": false, "192.168.1.5:1": false, "example.org:1": false, "garbage": false,
	} {
		if got := addrIsLoopback(addr); got != want {
			t.Errorf("addrIsLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// TestKeyPolicyMatrix covers setting x listener x configured keys.
func TestKeyPolicyMatrix(t *testing.T) {
	loop, public := []string{"127.0.0.1:8787", "127.0.0.1:8789"}, []string{"127.0.0.1:8789", ":8787"}
	type tc struct {
		name    string
		setting string
		addrs   []string
		keyed   bool
		wantErr bool
	}
	cases := []tc{
		{"auto, loopback, open: allowed", "", loop, false, false},
		{"auto, public, open: refused", "", public, false, true},
		{"auto, public, keyed: allowed", "", public, true, false},
		{"true, loopback, open: refused", "true", loop, false, true},
		{"true, loopback, keyed: allowed", "true", loop, true, false},
		{"false, public, open: allowed (explicit opt-out)", "false", public, false, false},
		{"explicit bad value", "maybe", loop, true, true},
		{"auto, one wildcard listener", "auto", []string{"0.0.0.0:80"}, false, true},
	}
	for _, c := range cases {
		a := NewAuth(discardLogger())
		a.Require(RoleView, http.NotFoundHandler())
		if c.keyed {
			a.Add(RoleView, AuthenticatorFunc(func(*http.Request) bool { return true }))
		}
		err := checkKeyPolicy(c.setting, c.addrs, a)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if err != nil && c.setting != "maybe" && !strings.Contains(err.Error(), "REQUIRE_KEYS") {
			t.Errorf("%s: error should name the setting: %v", c.name, err)
		}
	}
}

func TestKeyPolicyOnlyJudgesRolesInUse(t *testing.T) {
	a := NewAuth(discardLogger())
	a.Require(RoleIngest, http.NotFoundHandler())
	a.Add(RoleIngest, AuthenticatorFunc(func(*http.Request) bool { return true }))
	// The view role is not used by this binary, so having no view key is fine.
	if err := checkKeyPolicy("true", []string{":1"}, a); err != nil {
		t.Fatal(err)
	}
	// A role in use without keys is named in the error.
	b := NewAuth(discardLogger())
	b.Require(RoleIngest, http.NotFoundHandler())
	err := checkKeyPolicy("true", []string{":1"}, b)
	if err == nil || !strings.Contains(err.Error(), "ingest") {
		t.Fatalf("want error naming ingest, got %v", err)
	}
	if got := b.RolesInUse(); len(got) != 1 || got[0] != RoleIngest {
		t.Fatalf("RolesInUse = %v", got)
	}
}

// roleModule uses a role the way readapi does.
type roleModule struct{ role Role }

func (roleModule) Name() string { return "rolemod" }
func (m roleModule) Init(h *Host) error {
	return h.Routes.HandleFunc(http.MethodGet, "/x", h.Auth.Require(m.role, http.NotFoundHandler()).ServeHTTP)
}

func runWithArgs(t *testing.T, args ...string) error {
	t.Helper()
	log, _ := bufferLogger()
	h := NewHost(WithLogger(log))
	if err := h.Register(roleModule{RoleView}); err != nil {
		t.Fatal(err)
	}
	if err := h.Config.Load(args, envOf(nil)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	select {
	case err := <-done:
		return err
	case <-h.Ready():
		cancel()
		return <-done
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timeout")
		return nil
	}
}

func TestHostRefusesOpenAuthOnPublicListener(t *testing.T) {
	err := runWithArgs(t, "--listen", "0.0.0.0:0")
	if err == nil || !strings.Contains(err.Error(), "REQUIRE_KEYS") {
		t.Fatalf("want refusal, got %v", err)
	}
	if err := runWithArgs(t, "--listen", "127.0.0.1:0"); err != nil {
		t.Fatalf("loopback open auth must start: %v", err)
	}
	if err := runWithArgs(t, "--listen", "0.0.0.0:0", "--require-keys", "false"); err != nil {
		t.Fatalf("explicit opt-out must start: %v", err)
	}
	if err := runWithArgs(t, "--listen", "127.0.0.1:0", "--require-keys=true"); err == nil {
		t.Fatal("require-keys=true must refuse open auth even on loopback")
	}
}

func discardLogger() *slog.Logger { l, _ := bufferLogger(); return l }
