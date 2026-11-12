package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envOf(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

func newTestConfig(t *testing.T) *Config {
	t.Helper()
	c := NewConfig("RADAR")
	for _, s := range []Setting{
		{Key: "listen", Kind: KindString, Default: ":8787", Usage: "listen address"},
		{Key: "max-body", Kind: KindInt, Default: "1024", Usage: "max body"},
		{Key: "grace", Kind: KindDuration, Default: "5s", Usage: "grace"},
		{Key: "verbose", Kind: KindBool, Default: "false", Usage: "verbose"},
	} {
		if err := c.Declare(s); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestConfigDefaults(t *testing.T) {
	c := newTestConfig(t)
	if c.String("listen") != ":8787" || c.Int("max-body") != 1024 || c.Duration("grace") != 5*time.Second || c.Bool("verbose") {
		t.Fatal("defaults not readable before Load")
	}
}

func TestConfigPrecedence(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "env")
	content := "# comment\n\nRADAR_LISTEN=:1111\nRADAR_MAX_BODY=\"2222\"\nexport RADAR_GRACE=9s\nRADAR_VERBOSE=true\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newTestConfig(t)
	env := envOf(map[string]string{"RADAR_MAX_BODY": "3333", "RADAR_GRACE": "7s"})
	args := []string{"--env-file", file, "--grace", "1s"}
	if err := c.Load(args, env); err != nil {
		t.Fatal(err)
	}
	if got := c.String("listen"); got != ":1111" {
		t.Errorf("env file should beat default, got %q", got)
	}
	if got := c.Int("max-body"); got != 3333 {
		t.Errorf("env should beat env file, got %d", got)
	}
	if got := c.Duration("grace"); got != time.Second {
		t.Errorf("flag should beat env, got %v", got)
	}
	if !c.Bool("verbose") {
		t.Error("bool from env file")
	}
}

func TestConfigEnvFileFromEnvironment(t *testing.T) {
	file := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(file, []byte("RADAR_LISTEN=:4444\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newTestConfig(t)
	if err := c.Load(nil, envOf(map[string]string{"RADAR_ENV_FILE": file})); err != nil {
		t.Fatal(err)
	}
	if c.String("listen") != ":4444" {
		t.Fatal("env file path from environment ignored")
	}
}

func TestConfigErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"bad int env", nil, map[string]string{"RADAR_MAX_BODY": "abc"}, "max-body"},
		{"bad duration flag", []string{"--grace", "soon"}, nil, "grace"},
		{"bad bool env", nil, map[string]string{"RADAR_VERBOSE": "maybe"}, "verbose"},
		{"unknown flag", []string{"--nope"}, nil, "nope"},
		{"missing env file", []string{"--env-file", filepath.Join(t.TempDir(), "absent")}, nil, "env file"},
	}
	for _, tt := range tests {
		err := newTestConfig(t).Load(tt.args, envOf(tt.env))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want mention of %q", tt.name, err, tt.want)
		}
	}
}

func TestConfigEnvFileBadLine(t *testing.T) {
	file := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(file, []byte("no equals sign\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := newTestConfig(t).Load([]string{"--env-file", file}, envOf(nil)); err == nil {
		t.Fatal("malformed env file line must fail")
	}
}

func TestConfigDeclareRules(t *testing.T) {
	c := newTestConfig(t)
	if err := c.Declare(Setting{Key: "listen", Kind: KindString}); err == nil {
		t.Error("duplicate key must fail")
	}
	if err := c.Declare(Setting{Key: "", Kind: KindString}); err == nil {
		t.Error("empty key must fail")
	}
	if err := c.Declare(Setting{Key: "n", Kind: KindInt, Default: "x"}); err == nil {
		t.Error("unparsable default must fail")
	}
	if err := c.Load(nil, envOf(nil)); err != nil {
		t.Fatal(err)
	}
	if err := c.Declare(Setting{Key: "late", Kind: KindString}); err == nil {
		t.Error("declaring after Load must fail")
	}
}

func TestConfigUndeclaredKeyReadsZero(t *testing.T) {
	c := NewConfig("RADAR")
	if c.String("x") != "" || c.Int("x") != 0 || c.Duration("x") != 0 || c.Bool("x") {
		t.Fatal("undeclared keys must read as zero values")
	}
}

func TestEnvName(t *testing.T) {
	if got := NewConfig("RADAR").EnvName("max-body"); got != "RADAR_MAX_BODY" {
		t.Fatalf("EnvName = %q", got)
	}
}
