package client

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultSettingsReadServerFromEnv(t *testing.T) {
	env := func(name string) (string, bool) {
		if name == EnvServer {
			return "http://ingest.example.test:8788", true
		}
		return "", false
	}
	if got := DefaultSettings(env).Server; got != "http://ingest.example.test:8788" {
		t.Fatalf("server = %q", got)
	}
	none := func(string) (string, bool) { return "", false }
	if got := DefaultSettings(none).Server; got != FallbackServer {
		t.Fatalf("fallback server = %q", got)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "settings.json")
	want := Settings{Server: "http://10.0.0.5:8788", Ruleset: "rp", LogPath: `C:\Logs\WoWCombatLog.txt`, FollowNewest: true, IncludeExisting: true}
	if err := SaveSettings(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(path, Settings{Server: "http://other:1"})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLoadSettingsMissingFileGivesDefaults(t *testing.T) {
	defaults := Settings{Server: "http://127.0.0.1:8788"}
	got, err := LoadSettings(filepath.Join(t.TempDir(), "nope.json"), defaults)
	if err != nil || got != defaults {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestLoadSettingsKeepsDefaultsForMissingFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(path, []byte(`{"logPath":"a.txt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(path, Settings{Server: "http://127.0.0.1:8788"})
	if err != nil || got.Server != "http://127.0.0.1:8788" || got.LogPath != "a.txt" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestLoadSettingsCorruptFileReturnsDefaultsAndError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	defaults := Settings{Server: "http://127.0.0.1:8788"}
	got, err := LoadSettings(path, defaults)
	if err == nil || got != defaults {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestSaveSettingsIsPrivateAndAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := SaveSettings(path, Settings{Server: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(path, Settings{Server: "http://127.0.0.1:2"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 && !isWindows() {
		t.Fatalf("mode %v lets others read the file", perm)
	}
}

func TestSettingsHaveNoSecretField(t *testing.T) {
	typ := reflect.TypeOf(Settings{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name + typ.Field(i).Tag.Get("json"))
		for _, bad := range []string{"key", "secret", "token", "password"} {
			if strings.Contains(name, bad) {
				t.Errorf("Settings.%s looks like a secret", typ.Field(i).Name)
			}
		}
	}
}

func TestValidate(t *testing.T) {
	good := Settings{Server: "https://radar.example.test", Ruleset: "pvp", LogPath: "x.txt"}
	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantErr string
	}{
		{"ok", func(*Settings) {}, ""},
		{"auto ruleset", func(s *Settings) { s.Ruleset = "" }, ""},
		{"ruleset case", func(s *Settings) { s.Ruleset = "RPPVP" }, ""},
		{"bad ruleset", func(s *Settings) { s.Ruleset = "hardcore" }, "ruleset"},
		{"no scheme", func(s *Settings) { s.Server = "radar.example.test" }, "http"},
		{"ftp", func(s *Settings) { s.Server = "ftp://x" }, "http"},
		{"no host", func(s *Settings) { s.Server = "http://" }, "host"},
		{"userinfo", func(s *Settings) { s.Server = "http://u:p@host" }, "user name or password"},
		{"empty server", func(s *Settings) { s.Server = "" }, "server"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := good
			tc.mutate(&s)
			err := s.Validate()
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateForStartNeedsALogPath(t *testing.T) {
	s := Settings{Server: "http://127.0.0.1:8788"}
	if err := s.ValidateForStart(); err == nil || !strings.Contains(err.Error(), "log") {
		t.Fatalf("err = %v", err)
	}
	s.LogPath = "x.txt"
	if err := s.ValidateForStart(); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizedTrimsAndLowercases(t *testing.T) {
	s := Settings{Server: " http://h:1/ ", Ruleset: " PvP ", LogPath: ` "C:\a b\log.txt" `}.Normalized()
	if s.Server != "http://h:1" || s.Ruleset != "pvp" || s.LogPath != `C:\a b\log.txt` {
		t.Fatalf("got %+v", s)
	}
}

func TestDefaultSettingsPathIsPerUser(t *testing.T) {
	p, err := DefaultSettingsPath()
	if err != nil {
		t.Skip("no user config dir:", err)
	}
	if filepath.Base(p) != "settings.json" || !strings.Contains(p, "RadarClient") {
		t.Fatalf("path = %q", p)
	}
}
