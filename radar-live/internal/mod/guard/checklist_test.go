package guard

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const (
	checklistRel = "docs/security/checklist.md"
	finalEnv     = "RADAR_SECURITY_FINAL"
	columnCount  = 6
)

var (
	idPattern    = regexp.MustCompile(`^SEC-\d{3}$`)
	ownerPattern = regexp.MustCompile(`^8\.(\d|1[0-3])$`)
	statuses     = map[string]bool{"todo": true, "partial": true, "done": true}
)

// item is one row of the checklist table.
type item struct{ id, owner, control, proof, status string }

// repoRoot finds the module root from this file's location (internal/mod/guard).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// loadChecklist parses the table that starts at the "| ID |" header row.
func loadChecklist(t *testing.T) []item {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(checklistRel)))
	if err != nil {
		t.Fatalf("read checklist: %v", err)
	}
	var items []item
	inTable := false
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "| ID |") {
			inTable = true
			continue
		}
		if !inTable || !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "|---") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != columnCount {
			t.Fatalf("line %d: want %d columns, got %d", n+1, columnCount, len(cells))
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		items = append(items, item{cells[0], cells[1], cells[2], cells[3], cells[4]})
	}
	return items
}

func TestChecklistIsWellFormed(t *testing.T) {
	items := loadChecklist(t)
	if len(items) == 0 {
		t.Fatal("checklist has no rows")
	}
	seen := map[string]bool{}
	for _, it := range items {
		if !idPattern.MatchString(it.id) {
			t.Errorf("%q: id must look like SEC-001", it.id)
		}
		if seen[it.id] {
			t.Errorf("%s: duplicate id", it.id)
		}
		seen[it.id] = true
		if !ownerPattern.MatchString(it.owner) || it.owner == "8.0" || it.owner == "8.1" {
			t.Errorf("%s: owner %q must be 8.2 to 8.12", it.id, it.owner)
		}
		if it.control == "" {
			t.Errorf("%s: empty control", it.id)
		}
		if it.proof == "" {
			t.Errorf("%s: empty proof", it.id)
		}
		if !statuses[it.status] {
			t.Errorf("%s: status %q must be todo, partial or done", it.id, it.status)
		}
	}
}

// proofNames returns the test names of a proof cell: the part after "::" of each ";" separated entry.
func proofNames(proof string) []string {
	var names []string
	for _, entry := range strings.Split(proof, ";") {
		if _, name, ok := strings.Cut(entry, "::"); ok {
			names = append(names, strings.TrimSpace(name))
		}
	}
	return names
}

// testSources concatenates every *_test.go and *.test.js file under root.
func testSources(t *testing.T, root string) string {
	t.Helper()
	var all strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if !d.IsDir() && (strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".test.js")) {
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			all.Write(b)
			all.WriteByte('\n')
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	return all.String()
}

// TestChecklistProofsExist is the final gate: every done item must point at tests that exist.
func TestChecklistProofsExist(t *testing.T) {
	if os.Getenv(finalEnv) != "1" {
		t.Skipf("set %s=1 to check that proofs of done items exist", finalEnv)
	}
	sources := testSources(t, repoRoot(t))
	for _, it := range loadChecklist(t) {
		if it.status != "done" {
			continue
		}
		names := proofNames(it.proof)
		if len(names) == 0 {
			t.Errorf("%s: done item has no path::TestName proof", it.id)
		}
		for _, name := range names {
			if !strings.Contains(sources, name) {
				t.Errorf("%s", fmt.Sprintf("%s: proof %q not found in any test file", it.id, name))
			}
		}
	}
}
