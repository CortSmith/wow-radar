package core

import (
	"errors"
	"strings"
	"testing"
)

const fakeSecret = "test-key-0000000000000000"

func secretConfig(t *testing.T) *Config {
	t.Helper()
	c := NewConfig("RADAR")
	for _, s := range []Setting{
		{Key: "view-keys", Kind: KindString, Secret: true, Usage: "keys that may read"},
		{Key: "db-key", Kind: KindString, Secret: true, Default: "dflt-secret-value-0001", Usage: "key sent to the database"},
		{Key: "name", Kind: KindString, Default: "plain", Usage: "a plain setting"},
	} {
		if err := c.Declare(s); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestSecretSettingRefusesFlag(t *testing.T) {
	for _, args := range [][]string{
		{"--view-keys", fakeSecret}, {"-view-keys", fakeSecret}, {"--view-keys=" + fakeSecret}, {"--name", "x", "--db-key=" + fakeSecret},
	} {
		err := secretConfig(t).Load(args, envOf(nil))
		var sf *SecretFlagError
		if !errors.As(err, &sf) {
			t.Fatalf("%v: want SecretFlagError, got %v", args, err)
		}
		if !strings.Contains(err.Error(), "RADAR_VIEW_KEYS") && !strings.Contains(err.Error(), "RADAR_DB_KEY") {
			t.Errorf("message must name the env var: %v", err)
		}
		if strings.Contains(err.Error(), fakeSecret) {
			t.Errorf("error leaks the value: %v", err)
		}
	}
}

func TestSecretSettingFromEnvAndEnvFile(t *testing.T) {
	c := secretConfig(t)
	if err := c.Load(nil, envOf(map[string]string{"RADAR_VIEW_KEYS": fakeSecret})); err != nil || c.String("view-keys") != fakeSecret {
		t.Fatalf("env: %v %q", err, c.String("view-keys"))
	}
}

func TestSecretIsMaskedInUsageAndDump(t *testing.T) {
	c := secretConfig(t)
	if err := c.Load(nil, envOf(map[string]string{"RADAR_VIEW_KEYS": fakeSecret})); err != nil {
		t.Fatal(err)
	}
	text := c.Usage() + "\n" + strings.Join(c.Dump(), "\n")
	for _, bad := range []string{fakeSecret, "dflt-secret-value-0001"} {
		if strings.Contains(text, bad) {
			t.Fatalf("secret leaked: %s", text)
		}
	}
	for _, want := range []string{"RADAR_VIEW_KEYS=<set>", "RADAR_DB_KEY=<set>", "RADAR_NAME=plain", "default: <unset>", "default: <set>"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(c.Usage(), "--view-keys") || strings.Contains(c.Usage(), "--db-key") {
		t.Error("help must not offer flags for secrets")
	}
}

func TestHelpFlagReturnsMaskedUsage(t *testing.T) {
	c := secretConfig(t)
	err := c.Load([]string{"--help"}, envOf(map[string]string{"RADAR_VIEW_KEYS": fakeSecret}))
	if !errors.Is(err, ErrHelp) || strings.Contains(err.Error(), fakeSecret) || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("help: %v", err)
	}
}

func TestSecretParseErrorDoesNotEchoValue(t *testing.T) {
	c := NewConfig("RADAR")
	_ = c.Declare(Setting{Key: "n", Kind: KindInt, Secret: true})
	err := c.Load(nil, envOf(map[string]string{"RADAR_N": fakeSecret}))
	if err == nil || strings.Contains(err.Error(), fakeSecret) {
		t.Fatalf("got %v", err)
	}
}

func TestEnvNamesListsEverySetting(t *testing.T) {
	got := strings.Join(secretConfig(t).EnvNames(), " ")
	for _, want := range []string{"RADAR_VIEW_KEYS", "RADAR_DB_KEY", "RADAR_NAME", "RADAR_ENV_FILE"} {
		if !strings.Contains(got, want) {
			t.Errorf("EnvNames lacks %s: %s", want, got)
		}
	}
}
