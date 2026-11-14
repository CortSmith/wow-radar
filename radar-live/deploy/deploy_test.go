package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// unit parses the [Service] section of a systemd unit into key -> all values (repeatable keys keep order).
func unit(t *testing.T, name string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, line := range strings.Split(read(t, name), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Errorf("%s: line without '=': %q", name, line)
			continue
		}
		out[k] = append(out[k], v)
	}
	return out
}

var units = []string{"radar-db.service", "radar-ingest.service"}

// TestServiceUnitsAreSandboxed: every sandbox directive is present with its exact value in both units.
func TestServiceUnitsAreSandboxed(t *testing.T) {
	want := map[string]string{
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
		"LockPersonality":         "yes",
		"MemoryDenyWriteExecute":  "yes",
		"CapabilityBoundingSet":   "",
		"AmbientCapabilities":     "",
		"RestrictAddressFamilies": "AF_INET AF_INET6 AF_UNIX",
		"SystemCallArchitectures": "native",
		"SystemCallErrorNumber":   "EPERM",
		"UMask":                   "0077",
		"User":                    "radar",
		"Group":                   "radar",
		"Restart":                 "on-failure",
		"LimitNOFILE":             "4096",
		"MemoryMax":               "192M",
		"MemoryHigh":              "160M",
		"TasksMax":                "256",
		"CPUQuota":                "150%",
		"LogsDirectory":           "radar",
		"LogsDirectoryMode":       "0700",
		"RemoveIPC":               "yes",
		"KeyringMode":             "private",
	}
	for _, f := range units {
		u := unit(t, f)
		for k, v := range want {
			got, ok := u[k]
			if !ok {
				t.Errorf("%s lacks %s=", f, k)
				continue
			}
			if len(got) != 1 || got[0] != v {
				t.Errorf("%s: %s=%q, want %q", f, k, got, v)
			}
		}
		// Syscall filter: allow @system-service, then deny @privileged and @resources.
		sc := strings.Join(u["SystemCallFilter"], "|")
		if sc != "@system-service|~@privileged @resources" {
			t.Errorf("%s: SystemCallFilter lines = %q", f, sc)
		}
		// StartLimit* belong to [Unit] in current systemd, but are parsed here as plain lines too.
		all := read(t, f)
		for _, k := range []string{"StartLimitIntervalSec=", "StartLimitBurst="} {
			if !strings.Contains(all, k) {
				t.Errorf("%s lacks %s", f, k)
			}
		}
		if i := strings.Index(all, "StartLimitBurst="); i >= 0 && i > strings.Index(all, "[Service]") {
			t.Errorf("%s: StartLimit* must sit in [Unit]", f)
		}
		if !strings.Contains(all, "GOMEMLIMIT") && !strings.Contains(read(t, "radar.env.example"), "GOMEMLIMIT=96MiB") {
			t.Errorf("GOMEMLIMIT is no longer set anywhere")
		}
		// No capabilities and ports > 1024 only.
		if strings.Contains(all, "CAP_NET_BIND_SERVICE") {
			t.Errorf("%s binds low ports; keep ports above 1024", f)
		}
		// Secrets never in the unit itself.
		if strings.Contains(all, "Environment=RADAR_VIEW_KEYS") || strings.Contains(all, "Environment=RADAR_INGEST_KEYS") {
			t.Errorf("%s carries a key", f)
		}
	}
}

// TestUnitWritablePaths: only radar-db may write, and only its data (and log) directory.
func TestUnitWritablePaths(t *testing.T) {
	db := unit(t, "radar-db.service")
	if got := db["ReadWritePaths"]; len(got) != 1 || got[0] != "/var/lib/radar-live" {
		t.Errorf("radar-db ReadWritePaths = %q", got)
	}
	if got := db["StateDirectory"]; len(got) != 1 || got[0] != "radar-live" {
		t.Errorf("radar-db StateDirectory = %q", got)
	}
	if got := db["StateDirectoryMode"]; len(got) != 1 || got[0] != "0700" {
		t.Errorf("radar-db StateDirectoryMode = %q", got)
	}
	ing := unit(t, "radar-ingest.service")
	// Ingest writes only keyusage.json (key manager), in its own directory; never the data directory.
	if got := ing["ReadWritePaths"]; len(got) != 1 || got[0] != "/var/lib/radar-keys/usage" {
		t.Errorf("radar-ingest ReadWritePaths = %q, want only /var/lib/radar-keys/usage", got)
	}
	if _, ok := ing["StateDirectory"]; ok {
		t.Error("radar-ingest has no state directory")
	}
	for _, f := range units {
		u := unit(t, f)
		// Separate names: a shared one would be removed when either service stops.
		wantRT := map[string]string{"radar-db.service": "radar-live", "radar-ingest.service": "radar-ingest"}[f]
		if got := u["RuntimeDirectory"]; len(got) != 1 || got[0] != wantRT {
			t.Errorf("%s RuntimeDirectory = %q", f, got)
		}
		if got := u["RuntimeDirectoryMode"]; len(got) != 1 || got[0] != "0700" {
			t.Errorf("%s RuntimeDirectoryMode = %q", f, got)
		}
		if env := strings.Join(u["Environment"], "\n"); !strings.Contains(env, "RADAR_AUDIT_LOG=/var/log/radar/") {
			t.Errorf("%s does not point the security log into /var/log/radar", f)
		}
	}
}

// TestUnitsKeepLoopbackAndOrdering: ingest and the internal listener are loopback, db starts first.
func TestUnitsKeepLoopbackAndOrdering(t *testing.T) {
	if !strings.Contains(read(t, "radar-db.service"), "Environment=RADAR_DB_INTERNAL_LISTEN=127.0.0.1:8789") {
		t.Error("internal listener must default to loopback")
	}
	if !strings.Contains(read(t, "radar-ingest.service"), "Environment=RADAR_INGEST_LISTEN=127.0.0.1:8788") {
		t.Error("ingest must default to loopback")
	}
	if !strings.Contains(read(t, "radar-ingest.service"), "After=network.target radar-db.service") {
		t.Error("ingest starts after db")
	}
}

func TestEnvExampleHardeningLines(t *testing.T) {
	s := read(t, "radar.env.example")
	for _, want := range []string{"RADAR_REQUIRE_KEYS", "RADAR_HEADERS_HSTS", "RADAR_RATELIMIT_TRUSTED_PROXIES", "RADAR_AUDIT_LOG", "CHANGE_ME_VIEW_KEY", "CHANGE_ME_INGEST_KEY", "127.0.0.1:8787", "RADAR_TUNNEL_TOKEN"} {
		if !strings.Contains(s, want) {
			t.Errorf("radar.env.example lacks %s", want)
		}
	}
}

// TestInstallCreatesRestrictedUser: nologin system user, 0750 root:radar etc dir, 0640 env file, log dir 0700.
func TestInstallCreatesRestrictedUser(t *testing.T) {
	s := read(t, "install.sh")
	for _, want := range []string{
		"--shell /usr/sbin/nologin", "--system", `install -d -o root -g "$SVC_USER" -m 0750 "$ETC_DIR"`,
		"chmod 0640", `-m 0700 "$LOG_DIR"`, `[ "$(id -u)" -eq 0 ]`, "--with-ufw", "--with-fail2ban",
		"SHA256SUMS", "sha256sum -c", "flock",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("install.sh lacks %q", want)
		}
	}
	// The checksum must be mandatory: no "if SHA256SUMS exists" skip.
	if strings.Contains(s, `if [ -f "$src/SHA256SUMS" ] &&`) {
		t.Error("checksum verification must not be optional")
	}
	if strings.Index(s, "SHA256SUMS") > strings.Index(s, "install_bin radar-db\n") {
		t.Error("checksums must be verified before the binaries are installed")
	}
}

func scripts() []string {
	return []string{"install.sh", "uninstall.sh", "ufw-setup.sh", "verify-hardening.sh", "backup-keys.sh"}
}

// TestShellScriptsParse runs `sh -n` (dash on the laptop) on every script when a POSIX shell exists.
func TestShellScriptsParse(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	for _, f := range scripts() {
		out, err := exec.Command(sh, "-n", f).CombinedOutput()
		if err != nil {
			t.Errorf("sh -n %s: %v\n%s", f, err, out)
		}
		s := read(t, f)
		if !strings.HasPrefix(s, "#!/bin/sh\n") || !strings.Contains(s, "set -eu") {
			t.Errorf("%s needs #!/bin/sh and set -eu", f)
		}
		if strings.Contains(s, "\r") {
			t.Errorf("%s has CR line ends", f)
		}
		if bash := regexp.MustCompile(`\[\[ |\bfunction \w+|<<<|\bsource `).FindString(s); bash != "" {
			t.Errorf("%s uses a bashism: %q", f, bash)
		}
	}
}

func runUFW(t *testing.T, args ...string) (string, error) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	out, err := exec.Command(sh, append([]string{"ufw-setup.sh"}, args...)...).CombinedOutput()
	return string(out), err
}

func TestFirewallScriptDefaultDeny(t *testing.T) {
	s := read(t, "ufw-setup.sh")
	for _, want := range []string{"default deny incoming", "limit", "--dry-run", "--public", "ufw status verbose", "ufw --force reset", "ROLLBACK"} {
		if !strings.Contains(s, want) {
			t.Errorf("ufw-setup.sh lacks %q", want)
		}
	}
	// The loopback-only ports are never opened.
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "#") {
			continue
		}
		if strings.Contains(l, "8788") || strings.Contains(l, "8789") {
			if strings.Contains(l, "allow") || strings.Contains(l, "limit") {
				t.Errorf("opens a loopback port: %q", l)
			}
		}
	}
}

func TestFirewallDryRunLAN(t *testing.T) {
	out, err := runUFW(t, "--dry-run", "192.0.2.0/24")
	if err != nil {
		t.Fatalf("dry run failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"default deny incoming", "default allow outgoing",
		"limit from 192.0.2.0/24 to any port 22 proto tcp",
		"allow from 192.0.2.0/24 to any port 8787 proto tcp",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "8788") || strings.Contains(out, "8789") || strings.Contains(out, "allow 8787") || strings.Contains(out, "from any") {
		t.Errorf("LAN mode opened too much:\n%s", out)
	}
	if !strings.Contains(out, "IPV6=yes") {
		t.Errorf("IPv6 handling not reported:\n%s", out)
	}
}

func TestFirewallDryRunPublicNeedsFlagAndCIDRStillRequiredForSSH(t *testing.T) {
	out, err := runUFW(t, "--dry-run", "--public", "192.0.2.0/24")
	if err != nil {
		t.Fatalf("dry run failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "allow 8787/tcp") || !strings.Contains(out, "limit from 192.0.2.0/24 to any port 22 proto tcp") {
		t.Errorf("public mode output:\n%s", out)
	}
	if strings.Contains(out, "8788") || strings.Contains(out, "8789") {
		t.Errorf("public mode opened a loopback port:\n%s", out)
	}
}

func TestFirewallRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{
		{"--dry-run"},                         // no CIDR
		{"--dry-run", "not-a-cidr"},           // garbage
		{"--dry-run", "0.0.0.0/0"},            // that is "anywhere": needs --public
		{"--dry-run", "::/0"},                 // same for v6
		{"--dry-run", "192.0.2.0/24; reboot"}, // injection
		{"--dry-run", "--bogus", "192.0.2.0/24"},
	} {
		if out, err := runUFW(t, args...); err == nil {
			t.Errorf("%v accepted:\n%s", args, out)
		}
	}
}

// ---- fail2ban --------------------------------------------------------------------------------------------

func f2bPattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	var re string
	for _, line := range strings.Split(read(t, filepath.Join("fail2ban", "radar-live.conf")), "\n") {
		if v, ok := strings.CutPrefix(line, "failregex"); ok {
			_, v, _ = strings.Cut(v, "=")
			re = strings.TrimSpace(v)
		}
	}
	if re == "" {
		t.Fatal("no failregex in fail2ban/radar-live.conf")
	}
	// fail2ban expands <HOST> to an address group; use the same shape here.
	re = strings.ReplaceAll(re, "<HOST>", `(?P<host>[0-9a-fA-F:.]+)`)
	return regexp.MustCompile(re)
}

// Synthetic lines in the format of docs/security/logging.md (documentation addresses only).
func TestFail2banFilterMatchesAuditLines(t *testing.T) {
	re := f2bPattern(t)
	match := map[string]string{
		`{"ts":"2026-11-12T00:53:30Z","event":"auth_failure","ip":"203.0.113.9","class":"public","reason":"bad_key","count":1}`:    "203.0.113.9",
		`{"ts":"2026-11-12T00:54:30Z","event":"rate_limited","ip":"198.51.100.7","class":"sweep","reason":"ip_budget","count":42}`: "198.51.100.7",
		`{"ts":"2026-11-12T00:55:30Z","event":"auth_failure","ip":"2001:db8::1","class":"ingest","reason":"bad_key","count":3}`:    "2001:db8::1",
		`{"ts":"2026-11-12T00:56:30Z","event":"rate_limited","ip":"2001:db8:0:1::","class":"page","reason":"ip_budget","count":2}`: "2001:db8:0:1::",
	}
	for line, host := range match {
		m := re.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("no match: %s", line)
			continue
		}
		if got := m[re.SubexpIndex("host")]; got != host {
			t.Errorf("host = %q, want %q for %s", got, host, line)
		}
	}
	for _, line := range []string{
		`{"ts":"2026-11-12T00:53:30Z","event":"ban","ip":"203.0.113.9","class":"public","reason":"lockout","count":1}`,
		`{"ts":"2026-11-12T00:53:30Z","event":"validation_reject","ip":"203.0.113.9","class":"public","reason":"query_invalid","count":1}`,
		`{"ts":"2026-11-12T00:53:30Z","event":"integrity_degraded","reason":"quick_check","count":1}`,
		`{"ts":"2026-11-12T00:53:30Z","event":"auth_failure","ip":"invalid","class":"public","reason":"bad_key","count":1}`,
		`time=2026-11-12T00:53:30Z level=INFO msg="auth_failure" ip=203.0.113.9`,
		// Injection attempt through a quoted field must not move the host group.
		`{"ts":"x","event":"ban","ip":"198.51.100.1","class":"\"event\":\"auth_failure\",\"ip\":\"203.0.113.99\"","reason":"x","count":1}`,
	} {
		if re.MatchString(line) {
			t.Errorf("must not match: %s", line)
		}
	}
}

func TestFail2banFilesAndJail(t *testing.T) {
	f := read(t, filepath.Join("fail2ban", "radar-live.conf"))
	if !strings.Contains(f, "[Definition]") || !strings.Contains(f, "ignoreregex") {
		t.Error("filter needs [Definition] and ignoreregex")
	}
	j := read(t, filepath.Join("fail2ban", "jail.local"))
	for _, want := range []string{"[radar-live]", "enabled  = true", "filter   = radar-live", "logpath  = /var/log/radar/", "maxretry", "findtime", "bantime", "ignoreip", "127.0.0.1/8 ::1", "banaction = ufw", "backend"} {
		if !strings.Contains(j, want) {
			t.Errorf("jail.local lacks %q", want)
		}
	}
}

// ---- verify-hardening.sh ---------------------------------------------------------------------------------

func TestVerifyHardeningScript(t *testing.T) {
	s := read(t, "verify-hardening.sh")
	for _, want := range []string{
		"PASS", "FAIL", "exit 1", "systemctl show", "ss -ltn", "8787", "8788", "8789", "ufw status", "fail2ban-client",
		"unattended-upgrades", "CHANGE_ME", "RADAR_REQUIRE_KEYS", "stat -c", "NoNewPrivileges", "ProtectSystem",
		"MemoryDenyWriteExecute", "CapabilityBoundingSet", "SystemCallFilter", "/var/lib/radar-live",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("verify-hardening.sh lacks %q", want)
		}
	}
	// Read-only: no state-changing commands.
	for _, bad := range []string{"systemctl restart", "systemctl stop", "systemctl enable", "chmod ", "chown ", "rm -", "ufw allow", "ufw enable", "apt "} {
		for _, line := range strings.Split(s, "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "#") || strings.HasPrefix(l, "echo") {
				continue
			}
			if strings.Contains(l, bad) {
				t.Errorf("verify-hardening.sh must be read-only, found %q in %q", bad, l)
			}
		}
	}
}

// TestHostingDocsPresent: the guide covers every required topic.
func TestHostingDocsPresent(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "docs", "security", "hardening.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"Cloudflare Tunnel", "cloudflared", "RADAR_DB_LISTEN=127.0.0.1:8787", "RADAR_RATELIMIT_TRUSTED_PROXIES=127.0.0.1/32",
		"RADAR_HEADERS_HSTS=true", "RADAR_REQUIRE_KEYS=true", "Caddy", "rate_limit", "volumetric", "unattended-upgrades",
		"PasswordAuthentication no", "Key rotation", "restore drill", "logrotate", "/internal/metrics", "systemd-analyze security",
		"Origin IP", "verify-hardening.sh", "ufw-setup.sh", "MemoryDenyWriteExecute",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("hardening.md lacks %q", want)
		}
	}
}

func TestVerifyHardeningScriptExists(t *testing.T) {
	if _, err := os.Stat("verify-hardening.sh"); err != nil {
		t.Fatal(err)
	}
}

// TestNoRealHostDataInNewFiles: placeholders only (documentation address ranges, .invalid / example names).
func TestNoRealHostDataInNewFiles(t *testing.T) {
	ip := regexp.MustCompile(`\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\b`)
	ok := func(a string) bool {
		for _, p := range []string{"127.", "0.0.0.0", "192.0.2.", "198.51.100.", "203.0.113.", "10.0.0.0", "192.168.0.0", "172.16.0.0"} {
			if strings.HasPrefix(a, p) || a == p {
				return true
			}
		}
		return false
	}
	files := append(scripts(), "radar-db.service", "radar-ingest.service", "backup.cron", "README-xubuntu.md",
		filepath.Join("fail2ban", "radar-live.conf"), filepath.Join("fail2ban", "jail.local"),
		filepath.Join("..", "docs", "security", "hardening.md"))
	for _, f := range files {
		s := read(t, f)
		for _, m := range ip.FindAllString(s, -1) {
			if !ok(m) {
				t.Errorf("%s: address %s is not a placeholder", f, m)
			}
		}
		if regexp.MustCompile(`[A-Za-z0-9._-]+@[A-Za-z0-9-]+\.[a-z]{2,}`).MatchString(s) {
			for _, m := range regexp.MustCompile(`[A-Za-z0-9._-]+@[A-Za-z0-9-]+\.[a-z]{2,}`).FindAllString(s, -1) {
				if !strings.HasSuffix(m, ".invalid") && !strings.HasSuffix(m, "example.com") {
					t.Errorf("%s: e-mail %s", f, m)
				}
			}
		}
	}
}
