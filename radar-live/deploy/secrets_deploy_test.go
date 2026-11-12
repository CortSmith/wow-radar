package deploy

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// secretName matches variables that hold a secret value.
var secretName = regexp.MustCompile(`(?i)(KEYS?|TOKEN|SECRET|PASSWORD)$`)

// TestEnvExampleHoldsPlaceholdersOnly: every assigned secret variable carries a CHANGE_ME value.
func TestEnvExampleHoldsPlaceholdersOnly(t *testing.T) {
	seen := 0
	for _, line := range strings.Split(read(t, "radar.env.example"), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "# "))
		name, value, ok := strings.Cut(line, "=")
		if !ok || !strings.HasPrefix(name, "RADAR_") || !secretName.MatchString(name) || name == "RADAR_REQUIRE_KEYS" {
			continue
		}
		seen++
		if !strings.HasPrefix(value, "CHANGE_ME") {
			t.Errorf("%s has a non-placeholder value %q", name, value)
		}
	}
	if seen < 3 {
		t.Fatalf("only %d secret lines found", seen)
	}
	if strings.Contains(read(t, "radar.env.example"), "RADAR_CORS_ORIGIN=") {
		t.Error("stale RADAR_CORS_ORIGIN (the variable is RADAR_CORS_ORIGINS)")
	}
}

func TestInstallGeneratesKeysOnTheHost(t *testing.T) {
	s := read(t, "install.sh")
	for _, want := range []string{`"$BIN_DIR/radar-db" keygen`, "chmod 0640", `chown root:"$SVC_USER"`, "-m 0700 \"$DATA_DIR\"", "umask 077", "CHANGE_ME_VIEW_KEY"} {
		if !strings.Contains(s, want) {
			t.Errorf("install.sh lacks %q", want)
		}
	}
	if strings.Contains(s, "-m 0750 \"$DATA_DIR\"") {
		t.Error("data dir must be 0700")
	}
}

// TestNoSecretOnACommandLineInDeployFiles: units, cron and docs never pass a key as a flag.
func TestNoSecretOnACommandLineInDeployFiles(t *testing.T) {
	flag := regexp.MustCompile(`--(key|db-key|view-keys|ingest-keys|ingest-key|token|tunnel-token|password)[ =]`)
	for _, f := range []string{"radar-db.service", "radar-ingest.service", "backup.cron", "README-xubuntu.md", "install.sh", "uninstall.sh"} {
		if m := flag.FindString(read(t, f)); m != "" {
			t.Errorf("%s offers a secret flag: %q", f, m)
		}
	}
}
