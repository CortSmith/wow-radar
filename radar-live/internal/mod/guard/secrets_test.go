package guard

// Repository secret scan (task 8.13, docs/security/secrets-policy.md). It walks the files that would be
// committed and fails on key-like strings, private-key blocks, personal paths, e-mail addresses and public
// IPv4 addresses, and on tracked env/key/pem/db files. scripts/scan-secrets.ps1|sh apply the same rules.
//
// Known fake values live in docs/security/secrets-allowlist.txt, one entry per line:
//
//	<rule> <path-glob> <reason...>
//
// A reason is mandatory. This file builds its sample secrets at run time so it does not trip the scan.

import (
	"bufio"
	"bytes"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	allowlistRel   = "docs/security/secrets-allowlist.txt"
	maxScanBytes   = 2 << 20
	minTokenLen    = 20
	minTokenEntrpy = 3.5
)

// Rule names (also the first column of the allowlist).
const (
	ruleToken   = "token"
	rulePEM     = "pem"
	ruleBearer  = "bearer"
	ruleAWS     = "aws"
	ruleWinPath = "path"
	ruleEmail   = "email"
	ruleIPv4    = "ipv4"
	ruleFile    = "file"
)

var secSkipDirs = map[string]bool{".git": true, "node_modules": true, "dist": true, "maps": true}

var (
	rePEM     = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----`)
	reAWS     = regexp.MustCompile(`\b(AKIA|ASIA|AGPA|AIDA)[0-9A-Z]{16}\b`)
	reAWSSec  = regexp.MustCompile(`(?i)aws_secret_access_key["']?\s*[:=]\s*["']?[A-Za-z0-9/+=]{40}`)
	reBearer  = regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9._~+/=-]{20,})`)
	reAssign  = regexp.MustCompile("(?i)(?:key|secret|token|passw(?:or)?d|credential)[\\w.-]*[\"']?\\s*(?::=|[:=])\\s*[\"'`]?([A-Za-z0-9_+/=.-]{" + "20" + ",})")
	reEmail   = regexp.MustCompile(`[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,})`)
	reIPv4    = regexp.MustCompile(`(^|[^0-9A-Za-z.])(\d{1,3}(?:\.\d{1,3}){3})(?:[^0-9.]|\.$|\.\s|$)`)
	reWinPath = regexp.MustCompile(`(?i)\b[A-Z]:(?:\\\\?|/)Users(?:\\\\?|/)([^\\/"'<>*?|\s]+)`)
	reNixHome = regexp.MustCompile(`(?:^|[\s"'=(])/(?:Users|home)/([A-Za-z0-9._-]+)/`)
)

// genericUsers are the names a doc may use as an example instead of a real account.
var genericUsers = map[string]bool{
	"you": true, "name": true, "user": true, "username": true, "<you>": true, "public": true, "default": true,
	"%username%": true, "$env:username": true, "radar": true, "me": true, "example": true, "foo": true,
	"yourname": true, "your-name": true, "someone": true,
}

// placeholderWords mark a value as an obvious fake or placeholder.
var placeholderWords = []string{"change_me", "changeme", "test", "fake", "example", "dummy", "placeholder", "sample",
	"xxxx", "0000", "abcdef", "1234", "your", "secret-key", "<", "$", "{{", "todo", "none", "redacted"}

// secFinding is one hit; the matched text is shortened so reports do not spread a secret further.
type secFinding struct {
	Rule string
	Path string
	Line int
	Text string
}

func shorten(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 12 {
		return s[:6] + "..." + s[len(s)-3:]
	}
	return s
}

func entropy(s string) float64 {
	if s == "" {
		return 0
	}
	counts := map[rune]float64{}
	for _, r := range s {
		counts[r]++
	}
	var h float64
	n := float64(len([]rune(s)))
	for _, c := range counts {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}

func isPlaceholder(v string) bool {
	low := strings.ToLower(v)
	for _, w := range placeholderWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// looksLikeSecret: long, mixed letters and digits, high entropy, not an identifier or a path.
func looksLikeSecret(v string) bool {
	if len(v) < minTokenLen || isPlaceholder(v) {
		return false
	}
	hasDigit, hasLetter := false, false
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLetter = true
		}
	}
	if !hasDigit || !hasLetter || strings.Count(v, "/") > 2 || strings.Count(v, ".") > 2 {
		return false
	}
	return entropy(v) >= minTokenEntrpy
}

func publicIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	var o [4]int
	for i, p := range parts {
		if len(p) > 1 && p[0] == '0' || len(p) > 3 {
			return false
		}
		n := 0
		for _, r := range p {
			n = n*10 + int(r-'0')
		}
		if n > 255 {
			return false
		}
		o[i] = n
	}
	switch {
	case o[0] == 0 || o[0] == 10 || o[0] == 127 || o[0] >= 224: // this-network, private, loopback, multicast/reserved/broadcast
		return false
	case o[0] == 172 && o[1] >= 16 && o[1] <= 31, o[0] == 192 && o[1] == 168, o[0] == 169 && o[1] == 254:
		return false
	case o[0] == 192 && o[1] == 0 && o[2] == 2, o[0] == 198 && o[1] == 51 && o[2] == 100, o[0] == 203 && o[1] == 0 && o[2] == 113: // documentation
		return false
	case o[0] == 100 && o[1] >= 64 && o[1] <= 127: // CGNAT
		return false
	}
	return true
}

func allowedEmailDomain(d string) bool {
	d = strings.ToLower(d)
	return d == "example.com" || d == "example.org" || d == "example.net" || d == "localhost" ||
		strings.HasSuffix(d, ".invalid") || strings.HasSuffix(d, ".example") || strings.HasSuffix(d, ".test") ||
		strings.HasSuffix(d, ".example.com")
}

// scanText applies every content rule to one file's text.
func scanText(rel, text string) []secFinding {
	var out []secFinding
	add := func(rule string, line int, hit string) { out = append(out, secFinding{rule, rel, line, shorten(hit)}) }
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		if m := rePEM.FindString(line); m != "" {
			add(rulePEM, n, m)
		}
		if m := reAWS.FindString(line); m != "" {
			add(ruleAWS, n, m)
		}
		if m := reAWSSec.FindString(line); m != "" {
			add(ruleAWS, n, m)
		}
		for _, m := range reBearer.FindAllStringSubmatch(line, -1) {
			if looksLikeSecret(m[1]) {
				add(ruleBearer, n, m[1])
			}
		}
		for _, m := range reAssign.FindAllStringSubmatch(line, -1) {
			if looksLikeSecret(m[1]) {
				add(ruleToken, n, m[1])
			}
		}
		for _, m := range reEmail.FindAllStringSubmatch(line, -1) {
			if !allowedEmailDomain(m[1]) {
				add(ruleEmail, n, m[0])
			}
		}
		for _, m := range reIPv4.FindAllStringSubmatch(line, -1) {
			if publicIPv4(m[2]) {
				add(ruleIPv4, n, m[2])
			}
		}
		for _, m := range reWinPath.FindAllStringSubmatch(line, -1) {
			if !genericUsers[strings.ToLower(m[1])] {
				add(ruleWinPath, n, m[0])
			}
		}
		for _, m := range reNixHome.FindAllStringSubmatch(line, -1) {
			if !genericUsers[strings.ToLower(m[1])] {
				add(ruleWinPath, n, m[0])
			}
		}
	}
	return out
}

// badFileName reports tracked files that must never be committed.
func badFileName(rel string) bool {
	base := strings.ToLower(path.Base(filepath.ToSlash(rel)))
	switch {
	case base == ".env" || strings.HasPrefix(base, ".env.") && !strings.HasSuffix(base, ".example"):
		return true
	}
	for _, ext := range []string{".env", ".key", ".pem", ".p12", ".db", ".db-wal", ".db-shm", ".sqlite"} {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

type allowEntry struct{ rule, glob, reason string }

func (a allowEntry) covers(f secFinding) bool {
	if a.rule != "*" && a.rule != f.Rule {
		return false
	}
	if strings.HasSuffix(a.glob, "/**") && strings.HasPrefix(filepath.ToSlash(f.Path), strings.TrimSuffix(a.glob, "**")) {
		return true
	}
	ok, _ := path.Match(a.glob, filepath.ToSlash(f.Path))
	return ok || a.glob == filepath.ToSlash(f.Path)
}

func parseAllowlist(text string) (entries []allowEntry, problems []string) {
	sc := bufio.NewScanner(strings.NewReader(text))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			problems = append(problems, "allowlist line "+itoa(n)+" needs: <rule> <path-glob> <reason>")
			continue
		}
		entries = append(entries, allowEntry{f[0], f[1], strings.Join(f[2:], " ")})
	}
	return entries, problems
}

func itoa(n int) string { return strconv.Itoa(n) }

// repoFiles lists what would be committed: tracked plus untracked-but-not-ignored files via git when it
// is available, else every file under root outside the skipped folders.
func repoFiles(root string) []string {
	cmd := exec.Command("git", "ls-files", "-z", "-c", "-o", "--exclude-standard")
	cmd.Dir = root
	if raw, err := cmd.Output(); err == nil && len(raw) > 0 {
		var files []string
		for _, f := range bytes.Split(raw, []byte{0}) {
			if len(f) > 0 && !skipPath(string(f)) {
				if _, err := os.Stat(filepath.Join(root, string(f))); err == nil {
					files = append(files, string(f))
				}
			}
		}
		return files
	}
	var files []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if secSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files
}

func skipPath(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if secSkipDirs[part] {
			return true
		}
	}
	return false
}

// scanRepo returns every finding under root, before the allowlist.
func scanRepo(t *testing.T, root string) []secFinding {
	t.Helper()
	var out []secFinding
	for _, rel := range repoFiles(root) {
		if badFileName(rel) {
			out = append(out, secFinding{ruleFile, rel, 0, path.Base(filepath.ToSlash(rel))})
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil || len(data) > maxScanBytes || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			continue
		}
		out = append(out, scanText(rel, string(data))...)
	}
	return out
}

func TestRepoHasNoSecrets(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(allowlistRel)))
	if err != nil {
		t.Fatalf("read allowlist: %v", err)
	}
	allow, problems := parseAllowlist(string(raw))
	for _, p := range problems {
		t.Error(p)
	}
	used := make([]bool, len(allow))
	var bad []secFinding
	for _, f := range scanRepo(t, root) {
		hit := false
		for i, a := range allow {
			if a.covers(f) {
				used[i], hit = true, true
			}
		}
		if !hit {
			bad = append(bad, f)
		}
	}
	sort.Slice(bad, func(i, j int) bool {
		return bad[i].Path < bad[j].Path || bad[i].Path == bad[j].Path && bad[i].Line < bad[j].Line
	})
	for _, f := range bad {
		t.Errorf("%s: %s:%d: %s (fix it, or allowlist a known fake with a reason in %s)", f.Rule, f.Path, f.Line, f.Text, allowlistRel)
	}
	for i, a := range allow {
		if !used[i] {
			t.Errorf("stale allowlist entry (matches nothing): %s %s", a.rule, a.glob)
		}
	}
}

// sample builds a secret-looking value at run time so this file never contains one.
func sample(parts ...string) string { return strings.Join(parts, "") }

func rules(fs []secFinding) string {
	var names []string
	for _, f := range fs {
		names = append(names, f.Rule)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestScannerFindsWhatItShould(t *testing.T) {
	realKey := sample("qZ8vN3", "kT5wXp", "R7yLd2", "Hs9AeUf", "4GbJm1")
	home := sample("C:", `\Users\`, "alice", `\Desktop`)
	cases := []struct {
		name, text, want string
	}{
		{"pem", sample("-----BEGIN ", "RSA PRIVATE KEY", "-----"), rulePEM},
		{"pem openssh", sample("-----BEGIN ", "OPENSSH PRIVATE KEY", "-----"), rulePEM},
		{"token assign", `ingest_key = "` + realKey + `"`, ruleToken},
		{"token colon", `"apiToken": "` + realKey + `"`, ruleToken},
		{"password env", `DB_PASSWORD=` + realKey, ruleToken},
		{"bearer", `Authorization: Bearer ` + realKey, ruleBearer},
		{"aws id", sample("AKIA", "IOSFODNN7", "REALKE1"), ruleAWS},
		{"aws secret", sample("aws_secret_access_key = ", strings.Repeat("aB3/", 10)), ruleAWS},
		{"windows path", home, ruleWinPath},
		{"windows path escaped", sample("C:", `\\Users\\`, "alice", `\\x`), ruleWinPath},
		{"linux home", `ExecStart=/home/alice/bin/x`, ruleWinPath},
		{"email", sample("alice", "@", "gmail.com"), ruleEmail},
		{"public ip", `server = 8.8.4.4`, ruleIPv4},
		{"public ip in url", `http://93.184.216.34:8788/`, ruleIPv4},
	}
	for _, c := range cases {
		if got := rules(scanText("x.txt", c.text)); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestScannerAcceptsPlaceholdersAndGenericExamples(t *testing.T) {
	for _, text := range []string{
		`RADAR_INGEST_KEYS=CHANGE_ME_INGEST_KEY`,
		`ingestKey = "test-key-0000000000000000"`,
		`const viewKey = "view-secret-key-000001"`,
		`Authorization: Bearer <token>`,
		`curl -H "X-View-Key: $KEY"`,
		`key := os.Getenv("RADAR_FEED_KEY")`,
		`KeyViewKeys     = "view-keys"`,
		`C:\Users\<you>\radar\ingest.key`,
		`/home/radar/bin`,
		`mailto:security@example.invalid`,
		`someone@example.com`,
		`listen 127.0.0.1:8787 and 0.0.0.0 and 10.1.2.3 192.168.0.5 172.16.4.4 203.0.113.7 198.51.100.1 192.0.2.9`,
		`version 1.2.3.4.5 and go1.24.0.1`,
		`token, ok := lookup(name)`,
		`passwordFile := filepath.Join(dir, "pw")`,
	} {
		if got := scanText("x.txt", text); len(got) > 0 {
			t.Errorf("%q flagged: %+v", text, got)
		}
	}
}

func TestBadFileNames(t *testing.T) {
	for rel, want := range map[string]bool{
		"deploy/radar.env": true, ".env": true, ".env.local": true, "a/server.key": true, "tls.pem": true,
		"data/radar-live.db": true, "x.db-wal": true, "deploy/radar.env.example": false, ".env.example": false,
		"internal/x/store.go": false, "docs/key.md": false,
	} {
		if got := badFileName(rel); got != want {
			t.Errorf("badFileName(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestAllowlistNeedsReasons(t *testing.T) {
	_, problems := parseAllowlist("token some/file.go\n# comment\n\nemail docs/x.md a reason here\n")
	if len(problems) != 1 {
		t.Fatalf("problems = %v", problems)
	}
	entries, _ := parseAllowlist("email docs/*.md reason words\n")
	if len(entries) != 1 || !entries[0].covers(secFinding{Rule: ruleEmail, Path: "docs/x.md"}) ||
		entries[0].covers(secFinding{Rule: ruleToken, Path: "docs/x.md"}) || entries[0].covers(secFinding{Rule: ruleEmail, Path: "src/x.md"}) {
		t.Fatalf("matching wrong: %+v", entries)
	}
}

func TestScanRepoFindsPlantedSecretsAndSkipsFolders(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("conf/app.env", "A=b")
	write("src/a.go", `view_key = "`+sample("qZ8vN3", "kT5wXp", "R7yLd2", "Hs9AeUf")+`"`)
	write("node_modules/x/b.js", `view_key = "`+sample("qZ8vN3", "kT5wXp", "R7yLd2", "Hs9AeUf")+`"`)
	write("maps/c.txt", "8.8.4.4")
	write("README.md", "fine, see C:"+`\Users\<you>\`)
	got := rules(scanRepo(t, dir))
	if got != "file,token" {
		t.Fatalf("rules = %q", got)
	}
}

func TestScanSecretsScriptsPresent(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"scripts/scan-secrets.sh", "scripts/scan-secrets.ps1"} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, want := range []string{"secrets-allowlist.txt", "PRIVATE KEY", "Bearer", "AKIA", "Users", "ipv4"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s does not implement %q", name, want)
			}
		}
	}
}
