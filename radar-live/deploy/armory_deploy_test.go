package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Stage 15, task R7: radar-armory.service / .timer, env placeholders, installer wiring, docs.

func TestArmoryServiceIsOneShotAndSandboxed(t *testing.T) {
	u := unit(t, "radar-armory.service")
	want := map[string]string{
		"Type":                    "oneshot",
		"User":                    "radar",
		"Group":                   "radar",
		"ExecStart":               "/usr/local/bin/radar-armory",
		"EnvironmentFile":         "/etc/radar-live/radar.env",
		"TimeoutStartSec":         "6h 30min",
		"NoNewPrivileges":         "yes",
		"ProtectSystem":           "strict",
		"ProtectHome":             "yes",
		"PrivateTmp":              "yes",
		"PrivateDevices":          "yes",
		"ProtectKernelTunables":   "yes",
		"ProtectKernelModules":    "yes",
		"ProtectKernelLogs":       "yes",
		"ProtectControlGroups":    "yes",
		"ProtectClock":            "yes",
		"ProtectHostname":         "yes",
		"ProtectProc":             "invisible",
		"ProcSubset":              "pid",
		"RestrictNamespaces":      "yes",
		"RestrictRealtime":        "yes",
		"RestrictSUIDSGID":        "yes",
		"RemoveIPC":               "yes",
		"LockPersonality":         "yes",
		"MemoryDenyWriteExecute":  "yes",
		"KeyringMode":             "private",
		"CapabilityBoundingSet":   "",
		"AmbientCapabilities":     "",
		"RestrictAddressFamilies": "AF_INET AF_INET6 AF_UNIX",
		"SystemCallArchitectures": "native",
		"SystemCallErrorNumber":   "EPERM",
		"UMask":                   "0077",
		"MemoryMax":               "128M",
		"MemoryHigh":              "96M",
		"TasksMax":                "128",
		"LimitNOFILE":             "1024",
		"CPUQuota":                "100%",
		"Nice":                    "10",
		"LogsDirectory":           "radar",
		"LogsDirectoryMode":       "0700",
	}
	for k, v := range want {
		got, ok := u[k]
		if !ok {
			t.Errorf("radar-armory.service lacks %s=", k)
			continue
		}
		if len(got) != 1 || got[0] != v {
			t.Errorf("radar-armory.service: %s=%q, want %q", k, got, v)
		}
	}
	if sc := strings.Join(u["SystemCallFilter"], "|"); sc != "@system-service|~@privileged @resources" {
		t.Errorf("SystemCallFilter lines = %q", sc)
	}
	// Writes nothing but its log directory; never touches the data directory.
	for _, k := range []string{"ReadWritePaths", "StateDirectory", "RuntimeDirectory", "ReadOnlyPaths"} {
		if _, ok := u[k]; ok {
			t.Errorf("radar-armory.service must not set %s (no writable paths besides LogsDirectory)", k)
		}
	}
	// Exit 1 (partial) must show up as a failed unit: no SuccessExitStatus, no Restart, no flags on ExecStart.
	for _, k := range []string{"SuccessExitStatus", "Restart"} {
		if _, ok := u[k]; ok {
			t.Errorf("radar-armory.service must not set %s: exit 1/2 must be visible as failed", k)
		}
	}
	all := read(t, "radar-armory.service")
	if strings.Contains(all, "[Install]") {
		t.Error("the timer starts the service: no [Install] section in the service")
	}
	if !strings.Contains(all, "After=network-online.target radar-db.service") || !strings.Contains(all, "Wants=network-online.target radar-db.service") {
		t.Error("the service starts after network-online and radar-db")
	}
	if strings.Contains(all, "CAP_") {
		t.Error("no capabilities")
	}
	// No secret value, key variable assignment or key flag in the unit.
	if regexp.MustCompile(`(?m)^Environment=.*(WOW_ARMORY_API_KEY|WOW_ARMORY_CLIENT_ID|RADAR_ARMORY_DB_KEY)`).MatchString(all) {
		t.Error("the unit carries a key variable")
	}
	if regexp.MustCompile(`--(api-key|db-key)`).MatchString(all) {
		t.Error("the unit passes a key as a flag")
	}
}

func TestArmoryTimer(t *testing.T) {
	u := unit(t, "radar-armory.timer")
	want := map[string]string{
		"OnCalendar":         "*-*-* 00:00:00",
		"Persistent":         "true",
		"RandomizedDelaySec": "0",
		"Unit":               "radar-armory.service",
		"WantedBy":           "timers.target",
	}
	for k, v := range want {
		if got := u[k]; len(got) != 1 || got[0] != v {
			t.Errorf("radar-armory.timer: %s=%q, want %q", k, got, v)
		}
	}
}

// TestArmoryTimeoutMatchesDefaultRuntime: TimeoutStartSec is the default RADAR_ARMORY_MAX_RUNTIME plus 30 minutes.
func TestArmoryTimeoutMatchesDefaultRuntime(t *testing.T) {
	cfg, err := os.ReadFile(filepath.Join("..", "cmd", "radar-armory", "config.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "defaultRuntime = 6 * time.Hour") {
		t.Fatal("default runtime changed: update TimeoutStartSec in radar-armory.service (default + 30 minutes) and this test")
	}
	if got := unit(t, "radar-armory.service")["TimeoutStartSec"]; len(got) != 1 || got[0] != "6h 30min" {
		t.Errorf("TimeoutStartSec = %q", got)
	}
}

// TestEnvExampleDocumentsArmory: every RADAR_ARMORY_* name read by config.go is documented, secrets are placeholders.
func TestEnvExampleDocumentsArmory(t *testing.T) {
	cfg, err := os.ReadFile(filepath.Join("..", "cmd", "radar-armory", "config.go"))
	if err != nil {
		t.Fatal(err)
	}
	ex := read(t, "radar.env.example")
	names := regexp.MustCompile(`fromEnv\("([A-Z_]+)"`).FindAllStringSubmatch(string(cfg), -1)
	if len(names) < 9 {
		t.Fatalf("found only %d fromEnv names in config.go", len(names))
	}
	for _, m := range names {
		if !strings.Contains(ex, "RADAR_ARMORY_"+m[1]) {
			t.Errorf("radar.env.example does not document RADAR_ARMORY_%s", m[1])
		}
	}
	for _, want := range []string{
		"WOW_ARMORY_API_KEY_FILE=/etc/radar-live/secrets/armory-api-key",
		"WOW_ARMORY_CLIENT_ID_FILE=/etc/radar-live/secrets/armory-client-id",
		"RADAR_ARMORY_DB_KEY=CHANGE_ME_INGEST_KEY",
		"RADAR_ARMORY_DB_KEY_FILE",
		"RADAR_ARMORY_REGION", "RADAR_ARMORY_NAMESPACE", "RADAR_ARMORY_DEFAULT_REALM", "RADAR_ARMORY_TOKEN_URL", "PROVISIONAL",
	} {
		if !strings.Contains(ex, want) {
			t.Errorf("radar.env.example lacks %q", want)
		}
	}
	for _, line := range strings.Split(ex, "\n") {
		l := strings.TrimSpace(line)
		// The plain key variable is never given a value in the example (not even commented out).
		if strings.Contains(l, "WOW_ARMORY_API_KEY=") || strings.Contains(l, "WOW_ARMORY_CLIENT_ID=") {
			t.Errorf("radar.env.example assigns a plain armory secret variable: %q", l)
		}
		// Every other RADAR_ARMORY_* setting is commented out (the defaults apply).
		if strings.HasPrefix(l, "RADAR_ARMORY_") && !strings.HasPrefix(l, "RADAR_ARMORY_DB_KEY=") {
			t.Errorf("only RADAR_ARMORY_DB_KEY may be active in the example: %q", l)
		}
	}
}

func TestInstallWiresArmory(t *testing.T) {
	s := read(t, "install.sh")
	for _, want := range []string{
		"radar-armory-linux-$arch", "radar-armory.service", "radar-armory.timer", "install_bin radar-armory",
		`install -d -o root -g "$SVC_USER" -m 0750 "$SECRETS_DIR"`, "SECRETS_DIR=$ETC_DIR/secrets",
		"armory-api-key", "armory-client-id", "ARMORY_ID_FILE", `systemctl enable --now radar-armory.timer`,
		"RADAR_ARMORY_DB_KEY", "RADAR_ARMORY_DEFAULT_REALM", "docs/armory.md",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("install.sh lacks %q", want)
		}
	}
	// The binary and units are part of the checksummed file list (verified before anything is installed).
	files := regexp.MustCompile(`(?m)^files="([^"]*)"`).FindStringSubmatch(s)
	if files == nil {
		t.Fatal("no files= line in install.sh")
	}
	for _, f := range []string{"radar-armory-linux-$arch", "radar-armory.service", "radar-armory.timer"} {
		if !strings.Contains(files[1], f) {
			t.Errorf("files= does not list %s (it would skip the SHA256SUMS check)", f)
		}
	}
	if strings.Index(s, "install_bin radar-armory") < strings.Index(s, `echo "verifying checksums"`) {
		t.Error("radar-armory must be installed after the checksum check")
	}
	// The timer is enabled only when BOTH secret files exist: the enable line sits inside a test on both files.
	i := strings.Index(s, "systemctl enable --now radar-armory.timer")
	if i < 0 {
		t.Fatal("no timer enable line")
	}
	if !strings.Contains(s[:i], `[ -f "$ARMORY_KEY_FILE" ] && [ -f "$ARMORY_ID_FILE" ]`) {
		t.Error("the timer must be enabled only inside a check that both secret files exist")
	}
	if strings.Contains(s, "RADAR_ARMORY_BASE_URL in") {
		t.Error("the base URL is optional now (region default): install.sh must not require it")
	}
	// Never print or copy the key: no cat/echo/printf of the key file or variables holding it.
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "#") {
			continue
		}
		if regexp.MustCompile(`(cat|head|tail|printf|echo|sed|awk)\b[^|;&]*\$ARMORY_(KEY|ID)_FILE`).MatchString(l) ||
			regexp.MustCompile(`(echo|printf)\b[^|;&]*\$\{?(WOW_ARMORY_API_KEY|api_key)`).MatchString(l) {
			t.Errorf("install.sh may print the armory key: %q", l)
		}
	}
	// The key file mode is re-asserted when it exists; the dir is not left group-writable.
	for _, f := range []string{"ARMORY_KEY_FILE", "ARMORY_ID_FILE"} {
		if !strings.Contains(s, `chown root:"$SVC_USER" "$`+f+`"`) || !strings.Contains(s, `chmod 0640 "$`+f+`"`) {
			t.Errorf("install.sh must re-assert 0640 root:radar on an existing %s", f)
		}
	}
}

func TestUninstallRemovesArmory(t *testing.T) {
	s := read(t, "uninstall.sh")
	for _, want := range []string{
		"radar-armory.timer", "radar-armory.service", "/usr/local/bin/radar-armory", "/etc/systemd/system/$unit",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("uninstall.sh lacks %q", want)
		}
	}
	// The timer is stopped before the service it triggers.
	if strings.Index(s, "radar-armory.timer") > strings.Index(s, "radar-ingest.service radar-db.service") {
		t.Error("uninstall.sh must disable the armory timer first")
	}
}

func TestVerifyHardeningCoversArmory(t *testing.T) {
	s := read(t, "verify-hardening.sh")
	for _, want := range []string{
		"radar-armory.service", "radar-armory.timer", "SECRETS_DIR", "armory-api-key", "armory-client-id", "ARMORY_ID_FILE", "/usr/local/bin/radar-armory",
		"systemctl list-timers", "OnCalendar",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("verify-hardening.sh lacks %q", want)
		}
	}
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "#") || strings.HasPrefix(l, "echo") {
			continue
		}
		if regexp.MustCompile(`(cat|head|tail|cut|awk)\b[^|;&]*(ARMORY_KEY_FILE|ARMORY_ID_FILE|armory-api-key|armory-client-id)`).MatchString(l) {
			t.Errorf("verify-hardening.sh must never read the key file contents: %q", l)
		}
	}
}

func TestArmoryDocs(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "docs", "armory.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(doc)
	for _, want := range []string{
		"PROVISIONAL", "internal/mod/armoryclient/mapping.go", "WOW_ARMORY_API_KEY_FILE", "/etc/radar-live/secrets/armory-api-key",
		"WOW_ARMORY_CLIENT_ID_FILE", "/etc/radar-live/secrets/armory-client-id", "develop.battle.net", "client credentials",
		"/internal/v1/armory/seeds", "first night", "RADAR_ARMORY_REGION", "RADAR_ARMORY_NAMESPACE", "RADAR_ARMORY_DEFAULT_REALM",
		"403", "guild roster", "UNVERIFIED",
		"read -rs", "umask 077", "systemctl list-timers", "sudo systemctl start radar-armory", "journalctl -u radar-armory",
		"radar-armory: status=", "RADAR_ARMORY_BASE_URL", "RADAR_ARMORY_DB_KEY", "RADAR_ARMORY_MAX_RUNTIME", "TimeoutStartSec",
		"401", "429", "507", "exit 0", "exit 1", "exit 2", "rotate", "00:00", "03:17", "armory_characters",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("docs/armory.md lacks %q", want)
		}
	}
	// The key is never typed on a command line or echoed.
	if regexp.MustCompile(`(?m)(echo|printf)[^\n]*(API_KEY|CLIENT_ID)=`).MatchString(s) || strings.Contains(s, "WOW_ARMORY_API_KEY=") || strings.Contains(s, "WOW_ARMORY_CLIENT_ID=") {
		t.Error("docs/armory.md shows a key assignment; use the hidden-typing recipe only")
	}
	x := read(t, "README-xubuntu.md")
	for _, want := range []string{"radar-armory", "docs/armory.md", "armory-api-key", "armory-client-id", "RADAR_ARMORY_DEFAULT_REALM"} {
		if !strings.Contains(x, want) {
			t.Errorf("README-xubuntu.md lacks %q", want)
		}
	}
}

func TestSecretsPolicyDocumentsArmoryException(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "docs", "security", "secrets-policy.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"WOW_ARMORY_API_KEY", "WOW_ARMORY_API_KEY_FILE", "WOW_ARMORY_CLIENT_ID", "WOW_ARMORY_CLIENT_ID_FILE", "client secret", "RADAR_ARMORY_DB_KEY", "RADAR_ARMORY_DB_KEY_FILE", "exception"} {
		if !strings.Contains(s, want) {
			t.Errorf("secrets-policy.md lacks %q", want)
		}
	}
}

func TestNoSecretFlagOrHostDataInArmoryFiles(t *testing.T) {
	flag := regexp.MustCompile(`--(api-key|db-key|api-key-file|db-key-file|client-id|client-secret|client-id-file)[ =]`)
	for _, f := range []string{"radar-armory.service", "radar-armory.timer", "install.sh", "uninstall.sh", "verify-hardening.sh", "README-xubuntu.md"} {
		if m := flag.FindString(read(t, f)); m != "" {
			t.Errorf("%s offers a secret flag: %q", f, m)
		}
	}
	ip := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	for _, f := range []string{"radar-armory.service", "radar-armory.timer", filepath.Join("..", "docs", "armory.md")} {
		for _, m := range ip.FindAllString(read(t, f), -1) {
			if !strings.HasPrefix(m, "127.") && !strings.HasPrefix(m, "192.0.2.") {
				t.Errorf("%s: address %s is not a placeholder", f, m)
			}
		}
	}
}
