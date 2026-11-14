package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Rules for the key manager (docs/keys.md): radar-keys installed, the vault and its sealing key readable by root only,
// the keyring readable by the service, the usage file written by the service, the sealing key never in a backup.

func requireAll(t *testing.T, file, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("%s lacks %q", file, w)
		}
	}
}

func TestInstallSetsUpKeyManagerLayout(t *testing.T) {
	s := read(t, "install.sh")
	requireAll(t, "install.sh", s,
		"radar-keys-linux-$arch", "install_bin radar-keys", "backup-keys.sh",
		`install -d -o root -g "$SVC_USER" -m 0750 "$KEYRING_DIR"`,
		`install -d -o "$SVC_USER" -g "$SVC_USER" -m 0700 "$USAGE_DIR"`,
		`install -d -o root -g root -m 0700 "$VAULT_DIR"`,
		`chown root:"$SVC_USER" "$KEYRING_FILE"`, `chmod 0640 "$KEYRING_FILE"`,
		"KEYS_DIR=/var/lib/radar-keys", "RADAR_KEYRING_FILE", "RADAR_KEYUSAGE_FILE", "keys.md",
	)
	// The vault and its sealing key are root only, never group radar.
	if strings.Contains(s, `"$VAULT_DIR"`) {
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "$VAULT_DIR") && strings.Contains(line, "SVC_USER") {
				t.Errorf("the vault directory must not be given to the service user: %q", line)
			}
		}
	}
	// An existing settings file only gets the two lines when they are absent (never rewritten).
	requireAll(t, "install.sh", s, `grep -q '^RADAR_KEYRING_FILE='`, `grep -q '^RADAR_KEYUSAGE_FILE='`, `>>"$ENV_FILE"`)
	// The vault settings never go into the settings file the service reads.
	if strings.Contains(s, "RADAR_KEYVAULT") {
		t.Error("install.sh must not write RADAR_KEYVAULT_* into radar.env (the service user can read it)")
	}
	// The key manager is checked against SHA256SUMS like every other file.
	if !strings.Contains(s, `files="radar-db-linux-$arch`) || !strings.Contains(s, "radar-keys-linux-$arch") {
		t.Error("radar-keys must be in the verified file list")
	}
}

func TestEnvExampleSetsKeyringAndUsageButNotTheVault(t *testing.T) {
	s := read(t, "radar.env.example")
	requireAll(t, "radar.env.example", s,
		"\nRADAR_KEYRING_FILE=/var/lib/radar-keys/keyring/keyring.json\n",
		"\nRADAR_KEYUSAGE_FILE=/var/lib/radar-keys/usage/keyusage.json\n")
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "RADAR_KEYVAULT") {
			t.Errorf("the vault must not be configured in the service's settings: %q", line)
		}
	}
}

func TestIngestUnitReachesOnlyTheUsageDirectory(t *testing.T) {
	u := unit(t, "radar-ingest.service")
	if got := u["ReadWritePaths"]; len(got) != 1 || got[0] != "/var/lib/radar-keys/usage" {
		t.Errorf("ReadWritePaths = %q", got)
	}
	for _, k := range []string{"InaccessiblePaths", "ReadOnlyPaths", "StateDirectory"} {
		if _, ok := u[k]; ok {
			t.Errorf("unexpected %s in radar-ingest.service", k)
		}
	}
}

func TestBackupIncludesKeyringAndVaultButNotTheSealingKey(t *testing.T) {
	s := read(t, "backup-keys.sh")
	requireAll(t, "backup-keys.sh", s, "keyring/keyring.json", "keyvault.bin", "-m 0700", "umask 077", `id -u`)
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "#") {
			continue
		}
		if strings.Contains(l, "keyvault.key") {
			t.Errorf("the sealing key must never be copied: %q", l)
		}
	}
	cron := read(t, "backup.cron")
	requireAll(t, "backup.cron", cron, "root /usr/local/sbin/radar-keys-backup", "keyvault.key")
	if !regexp.MustCompile(`(?m)^\d+ \d+ \* \* \* root /usr/local/sbin/radar-keys-backup$`).MatchString(cron) {
		t.Error("backup.cron needs a root line for radar-keys-backup")
	}
	requireAll(t, "install.sh", read(t, "install.sh"), "/usr/local/sbin/radar-keys-backup")
}

func TestVerifyHardeningChecksKeyManagerFiles(t *testing.T) {
	s := read(t, "verify-hardening.sh")
	requireAll(t, "verify-hardening.sh", s,
		`"750 root $SVC_USER" "keyring dir"`, `"640 root $SVC_USER" "keyring file"`,
		`"700 $SVC_USER $SVC_USER" "key usage dir"`, `"700 root root" "key vault dir"`,
		`"600 root root" "key vault file"`, `"600 $SVC_USER $SVC_USER" "key usage file"`,
		"RADAR_KEYRING_FILE", "RADAR_KEYUSAGE_FILE", "RADAR_KEYVAULT_FILE", "sealing key",
		"/usr/local/bin/radar-keys",
	)
}

func TestUninstallRemovesKeyManagerOnlyOnPurge(t *testing.T) {
	s := read(t, "uninstall.sh")
	requireAll(t, "uninstall.sh", s, "/usr/local/bin/radar-keys", "/usr/local/sbin/radar-keys-backup")
	purge := regexp.MustCompile(`(?m)^\s+rm -rf [^\n]*/var/lib/radar-keys[^\n]*$`)
	if !purge.MatchString(s) {
		t.Error("--purge must remove /var/lib/radar-keys")
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "rm -rf") && strings.Contains(line, "radar-keys") && !strings.HasPrefix(line, "    ") {
			t.Errorf("the keyring and vault are removed outside --purge: %q", line)
		}
	}
}

func TestBuildScriptsShipRadarKeysAndItsGuide(t *testing.T) {
	for _, f := range []string{"build.sh", "build.ps1"} {
		b, err := os.ReadFile(filepath.Join("..", "scripts", f))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		requireAll(t, f, s, "radar-keys", "keys.md")
		for _, arch := range []string{"amd64", "arm64"} {
			if !regexp.MustCompile(`radar-keys"?,? "?linux"?,? ?"?` + arch + `|build radar-keys linux ` + arch).MatchString(s) {
				t.Errorf("%s does not build radar-keys for linux/%s", f, arch)
			}
		}
		if strings.Contains(s, "radar-keys windows") || strings.Contains(s, `"radar-keys", "windows"`) {
			t.Errorf("%s builds a windows radar-keys; it runs on the laptop only", f)
		}
	}
}

func TestKeysGuideCoversTheTopics(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "docs", "keys.md"))
	if err != nil {
		t.Fatal(err)
	}
	requireAll(t, "docs/keys.md", string(b),
		"radar-keys add", "radar-keys block", "radar-keys unblock", "radar-keys delete", "radar-keys regen",
		"key_disabled", "blocked", "keyvault.key", "/var/lib/radar-keys", "RADAR_KEYRING_FILE", "RADAR_KEYUSAGE_FILE",
		"RADAR_INGEST_KEYS", "restore", "eye", "10 seconds", "RADAR_FEED_KEY",
	)
	for _, f := range []string{"README-xubuntu.md", filepath.Join("..", "README.md")} {
		if !strings.Contains(read(t, f), "docs/keys.md") && !strings.Contains(read(t, f), "keys.md") {
			t.Errorf("%s does not point to the keys guide", f)
		}
	}
}
