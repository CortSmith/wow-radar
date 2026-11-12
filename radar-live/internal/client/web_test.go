package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// nodeBinary finds node, or returns "" so the test can skip on machines without it.
func nodeBinary() string {
	if p, err := exec.LookPath("node"); err == nil {
		return p
	}
	const windowsDefault = `C:\Program Files\nodejs\node.exe`
	if _, err := os.Stat(windowsDefault); err == nil {
		return windowsDefault
	}
	return ""
}

// TestWebUILogicWithNode runs the page's own tests (web/test/*.test.mjs) with node --test.
func TestWebUILogicWithNode(t *testing.T) {
	node := nodeBinary()
	if node == "" {
		t.Skip("node is not installed")
	}
	tests, err := filepath.Glob(filepath.Join("web", "test", "*.test.mjs"))
	if err != nil || len(tests) == 0 {
		t.Fatalf("no node tests found: %v", err)
	}
	cmd := exec.Command(node, append([]string{"--test"}, tests...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node --test failed: %v\n%s", err, out)
	}
}
