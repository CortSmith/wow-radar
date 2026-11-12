package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radarlive/internal/core"
)

func none(string) (string, bool) { return "", false }

func TestDefaultsApplyBelowEverythingElse(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ingest.env")
	if err := os.WriteFile(file, []byte("# c\nexport RADAR_CONTACTX_FLUSH=5s\nRADAR_LISTEN = :9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := core.NewConfig("RADAR")
	for _, s := range []core.Setting{
		{Key: "listen", Kind: core.KindString, Default: "x"},
		{Key: "contactx-flush", Kind: core.KindDuration, Default: "1s"},
		{Key: "bountyx-flush", Kind: core.KindDuration, Default: "1s"},
		{Key: "max-body", Kind: core.KindInt, Default: "1"},
	} {
		if err := cfg.Declare(s); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--env-file=" + file, "--bountyx-flush=7s"}
	lookup := func(n string) (string, bool) { return "123", n == "RADAR_MAX_BODY" }
	if err := cfg.Load(args, withDefaults(args, lookup)); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Duration("contactx-flush"); got != 5*time.Second {
		t.Fatalf("env file lost: %v", got)
	}
	if got := cfg.Duration("bountyx-flush"); got != 7*time.Second {
		t.Fatalf("flag lost: %v", got)
	}
	if cfg.Int("max-body") != 123 || cfg.String("listen") != ":9" {
		t.Fatalf("env/file lost: %d %q", cfg.Int("max-body"), cfg.String("listen"))
	}
}

func TestDefaultsFill(t *testing.T) {
	lookup := withDefaults(nil, none)
	for name, want := range map[string]string{"RADAR_LISTEN": "127.0.0.1:8788", "RADAR_CONTACTX_FLUSH": "2s", "RADAR_BOUNTYX_FLUSH": "2s"} {
		if got, ok := lookup(name); !ok || got != want {
			t.Fatalf("%s = %q %v", name, got, ok)
		}
	}
	if _, ok := lookup("RADAR_DB_URL"); ok {
		t.Fatal("unexpected default")
	}
}

func TestEnvFilePathForms(t *testing.T) {
	cases := []struct {
		args []string
		env  string
		want string
	}{
		{[]string{"--env-file=a"}, "", "a"},
		{[]string{"-env-file", "b"}, "", "b"},
		{nil, "c", "c"},
		{[]string{"--env-file"}, "", ""},
	}
	for _, c := range cases {
		lookup := func(n string) (string, bool) { return c.env, n == "RADAR_ENV_FILE" && c.env != "" }
		if got := envFilePath(c.args, lookup); got != c.want {
			t.Fatalf("%v: %q want %q", c.args, got, c.want)
		}
	}
	if len(envFileKeys(filepath.Join(t.TempDir(), "missing"))) != 0 {
		t.Fatal("missing file must give no keys")
	}
}

func TestModulesListIsComplete(t *testing.T) {
	names := map[string]bool{}
	for _, m := range modules() {
		names[m.Name()] = true
	}
	for _, want := range []string{"relay", "ratelimit", "abuse", "limits", "keys", "logparse", "contactx", "bountyx", "streamapi"} {
		if !names[want] {
			t.Fatalf("missing module %s", want)
		}
	}
	if modules()[0].Name() != "relay" {
		t.Fatal("relay must be first so it stops last")
	}
}

func TestRunStartsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--listen=127.0.0.1:0", "--db-url=http://127.0.0.1:1"}, none)
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not stop")
	}
}

func TestRunNeedsDBURL(t *testing.T) {
	if err := run(context.Background(), []string{"--listen=127.0.0.1:0"}, none); err == nil {
		t.Fatal("expected error without --db-url")
	}
}

func TestRunRejectsBadFlag(t *testing.T) {
	if err := run(context.Background(), []string{"--nope"}, none); err == nil {
		t.Fatal("expected error")
	}
}

func TestRefusesOpenAuthOnPublicListener(t *testing.T) {
	err := run(context.Background(), []string{"--listen=0.0.0.0:0", "--db-url=http://127.0.0.1:1"}, none)
	if err == nil || !strings.Contains(err.Error(), "REQUIRE_KEYS") {
		t.Fatalf("want refusal, got %v", err)
	}
	env := func(k string) (string, bool) {
		if k == "RADAR_INGEST_KEYS" {
			return "test-ingest-key-0000000", true
		}
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := run(ctx, []string{"--listen=127.0.0.1:0", "--db-url=http://127.0.0.1:1", "--require-keys=true"}, env); err != nil {
		t.Fatalf("keyed host must start: %v", err)
	}
}

// The ingest limits come from RADAR_STREAM_* and a bad value stops the binary instead of silently
// switching a limit off.
func TestStreamLimitsAreConfigurableAndValidated(t *testing.T) {
	env := func(k string) (string, bool) {
		if k == "RADAR_STREAM_LINE_RATE" {
			return "-1", true
		}
		return "", false
	}
	err := run(context.Background(), []string{"--listen=127.0.0.1:0", "--db-url=http://127.0.0.1:1"}, env)
	if err == nil || !strings.Contains(err.Error(), "stream-line-rate") {
		t.Fatalf("want refusal naming the setting, got %v", err)
	}
	good := func(k string) (string, bool) {
		if k == "RADAR_STREAM_LINE_RATE" {
			return "100", true
		}
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if err := run(ctx, []string{"--listen=127.0.0.1:0", "--db-url=http://127.0.0.1:1"}, good); err != nil {
		t.Fatal(err)
	}
}

func TestDBURLWithCredentialsIsRefused(t *testing.T) {
	err := run(context.Background(), []string{"--listen=127.0.0.1:0", "--db-url=http://user:pw@127.0.0.1:1"}, none)
	if err == nil || strings.Contains(err.Error(), "pw@") {
		t.Fatalf("got %v", err)
	}
}

func TestSecretFlagsAreRefusedAndHelpIsMasked(t *testing.T) {
	const k = "test-key-0000000000000000"
	for flagName, env := range map[string]string{"--db-key": "RADAR_DB_KEY", "--ingest-keys": "RADAR_INGEST_KEYS", "--view-keys": "RADAR_VIEW_KEYS"} {
		err := run(context.Background(), []string{flagName + "=" + k, "--db-url=http://127.0.0.1:1"}, none)
		if err == nil || !strings.Contains(err.Error(), env) || strings.Contains(err.Error(), k) {
			t.Errorf("%s: err = %v", flagName, err)
		}
	}
	envKeys := func(name string) (string, bool) {
		return k, strings.HasPrefix(name, "RADAR_DB_KEY") || name == "RADAR_INGEST_KEYS"
	}
	err := run(context.Background(), []string{"--help"}, envKeys)
	if err == nil || strings.Contains(err.Error(), k) || !strings.Contains(err.Error(), "RADAR_DB_KEY") {
		t.Fatalf("help: %v", err)
	}
}
