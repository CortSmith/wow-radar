package guard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Supply-chain guard (task 8.11): the dependency list is an allowlist. Adding a module means a
// conscious edit of this file plus docs/security/supply-chain.md.

var allowedDirect = map[string]bool{
	"modernc.org/sqlite":          true,
	"github.com/jchv/go-webview2": true,
	"golang.org/x/sys":            true,
}

// allowedIndirect is the reviewed transitive closure of the direct allowlist.
var allowedIndirect = map[string]bool{
	"github.com/dustin/go-humanize":    true,
	"github.com/google/uuid":           true,
	"github.com/jchv/go-winloader":     true,
	"github.com/mattn/go-isatty":       true,
	"github.com/ncruces/go-strftime":   true,
	"github.com/remyoudompheng/bigfft": true,
	"modernc.org/libc":                 true,
	"modernc.org/mathutil":             true,
	"modernc.org/memory":               true,
}

func scRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test directory")
		}
		dir = parent
	}
}

type scModReq struct {
	path, version string
	indirect      bool
}

// scParseGoMod reads require lines and flags replace/exclude/toolchain-free directives by hand
// (no x/mod dependency on purpose).
func scParseGoMod(t *testing.T, text string) (reqs []scModReq, replaces []string) {
	t.Helper()
	block := ""
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || strings.HasPrefix(line, "//"):
			continue
		case line == ")":
			block = ""
			continue
		case strings.HasSuffix(line, "(") && !strings.Contains(line, "//"):
			block = strings.TrimSpace(strings.TrimSuffix(line, "("))
			continue
		}
		if block == "" {
			switch {
			case strings.HasPrefix(line, "require "):
				line = strings.TrimSpace(strings.TrimPrefix(line, "require "))
			case strings.HasPrefix(line, "replace "):
				replaces = append(replaces, line)
				continue
			default:
				continue
			}
		} else if block == "replace" {
			replaces = append(replaces, line)
			continue
		} else if block != "require" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		reqs = append(reqs, scModReq{path: fields[0], version: fields[1], indirect: strings.Contains(line, "// indirect")})
	}
	return reqs, replaces
}

func TestDirectDepsAllowlist(t *testing.T) {
	root := scRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	reqs, replaces := scParseGoMod(t, string(data))
	if len(reqs) == 0 {
		t.Fatal("no requirements parsed from go.mod")
	}
	for _, r := range reqs {
		switch {
		case !r.indirect && !allowedDirect[r.path]:
			t.Errorf("direct dependency %s %s is not on the allowlist (see docs/security/supply-chain.md)", r.path, r.version)
		case r.indirect && !allowedIndirect[r.path] && !allowedDirect[r.path]:
			t.Errorf("indirect dependency %s %s is outside the reviewed closure (see docs/security/supply-chain.md)", r.path, r.version)
		}
	}
	for _, d := range replaces {
		t.Errorf("go.mod must not use replace directives: %q", d)
	}
}

var scGoSumLine = regexp.MustCompile(`^(\S+) (v\S+?)(/go\.mod)? h1:[A-Za-z0-9+/]{43}=$`)

func TestGoSumPinsEveryRequirement(t *testing.T) {
	root := scRoot(t)
	mod, _ := os.ReadFile(filepath.Join(root, "go.mod"))
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatalf("go.sum missing: %v", err)
	}
	haveZip, haveMod := map[string]bool{}, map[string]bool{}
	for _, l := range strings.Split(strings.ReplaceAll(string(sum), "\r", ""), "\n") {
		if l == "" {
			continue
		}
		m := scGoSumLine.FindStringSubmatch(l)
		if m == nil {
			t.Errorf("malformed go.sum line: %q", l)
			continue
		}
		if m[3] != "" {
			haveMod[m[1]+" "+m[2]] = true
		} else {
			haveZip[m[1]+" "+m[2]] = true
		}
	}
	reqs, _ := scParseGoMod(t, string(mod))
	for _, r := range reqs {
		k := r.path + " " + r.version
		if !haveMod[k] {
			t.Errorf("go.sum has no go.mod hash for %s", k)
		}
		// Every requirement that contributes code needs a zip hash; modules only used for
		// graph pruning may legitimately lack one, so only direct requirements are enforced.
		if !r.indirect && !haveZip[k] {
			t.Errorf("go.sum has no content hash for direct dependency %s", k)
		}
	}
}

func TestNoVendorDirectory(t *testing.T) {
	// Decision recorded in docs/security/supply-chain.md: not vendoring; go.sum + proxy checksum DB.
	if _, err := os.Stat(filepath.Join(scRoot(t), "vendor")); err == nil {
		t.Error("vendor/ exists; the documented decision is not to vendor (update supply-chain.md if that changed)")
	}
}

func TestBuildScriptsReproducibleFlags(t *testing.T) {
	root := scRoot(t)
	for _, name := range []string{"build.sh", "build.ps1"} {
		b, err := os.ReadFile(filepath.Join(root, "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, want := range []string{"CGO_ENABLED", "-trimpath", "-buildvcs=false", "-s -w -buildid=", "SHA256SUMS"} {
			if !strings.Contains(s, want) {
				t.Errorf("scripts/%s lacks %q", name, want)
			}
		}
	}
}

func TestSecurityScanScriptsPresent(t *testing.T) {
	root := scRoot(t)
	for _, name := range []string{"security-scan.ps1", "security-scan.sh"} {
		b, err := os.ReadFile(filepath.Join(root, "scripts", name))
		if err != nil {
			t.Fatalf("scripts/%s missing: %v", name, err)
		}
		for _, want := range []string{"gofmt", "go vet", "govulncheck", "staticcheck", "gosec", "mod verify", "SKIPPED"} {
			if !strings.Contains(string(b), want) {
				t.Errorf("scripts/%s does not run or mention %q", name, want)
			}
		}
	}
}

// TestNoNetworkUpdateCode: nothing in the shipped code downloads and runs or replaces code.
// The only exec.Command users are the client's browser opener and the audit alert hook, both
// with operator-chosen argv and no shell; neither may mention self-update or installers.
func TestNoNetworkUpdateCode(t *testing.T) {
	root := scRoot(t)
	banned := []string{"selfupdate", "self-update", "autoupdate", "auto-update", "go install", "go get ", "go-getter", "curl ", "wget ", "Invoke-WebRequest", "DownloadFile("}
	for _, dir := range []string{"cmd", "internal"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, _ := os.ReadFile(p)
			low := strings.ToLower(string(b))
			for _, w := range banned {
				if strings.Contains(low, strings.ToLower(w)) {
					t.Errorf("%s mentions %q; shipped code must never fetch or install code", p, w)
				}
			}
			return nil
		})
	}
}
