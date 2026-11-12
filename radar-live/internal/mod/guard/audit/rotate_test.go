package audit

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRotatingFileRotatesAndKeepsN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sec.log")
	r, err := openRotating(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	line := strings.Repeat("x", 39) + "\n" // 40 bytes: two per file
	for i := 0; i < 12; i++ {
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 3 {
		t.Fatalf("want current + 2 kept generations, got %d files", len(entries))
	}
	for _, name := range []string{"sec.log", "sec.log.1", "sec.log.2"} {
		st, err := os.Stat(filepath.Join(filepath.Dir(path), name))
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() > 100 {
			t.Fatalf("%s is %d bytes, over the limit", name, st.Size())
		}
	}
}

func TestRotatingFileKeepZeroDiscardsOld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sec.log")
	r, err := openRotating(path, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < 6; i++ {
		_, _ = r.Write([]byte(strings.Repeat("y", 29) + "\n"))
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("%d files, want 1", len(entries))
	}
}

func TestRotatingFileOversizedLineStillWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sec.log")
	r, _ := openRotating(path, 10, 1)
	defer r.Close()
	if _, err := r.Write([]byte(strings.Repeat("z", 50) + "\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); len(b) != 51 {
		t.Fatalf("len = %d", len(b))
	}
}

func TestRotatingFileModeIs0600AndAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "sec.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := openRotating(path, 1<<20, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r.Write([]byte("new\n"))
	_ = r.Close()
	if b, _ := os.ReadFile(path); string(b) != "old\nnew\n" {
		t.Fatalf("content %q", b)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(path)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, want 0600", st.Mode().Perm())
		}
	}
	if _, err := r.Write([]byte("x")); err == nil {
		t.Fatal("write after close succeeded")
	}
}

func TestRotatingFileRejectsBadLimit(t *testing.T) {
	if _, err := openRotating(filepath.Join(t.TempDir(), "a"), 0, 1); err == nil {
		t.Fatal("zero limit accepted")
	}
}
