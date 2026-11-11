package maps

import (
	"os"
	"path/filepath"
	"testing"
)

const idx = `{"zones": {"dunmorogh": {"name": "Dun Morogh", "file": "DunMorogh.png", "w": 1002, "h": 668, "revealed": true},
 "evil": {"name": "Evil", "file": "../secret.png", "w": 1, "h": 1, "revealed": false}}}`

func mk(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "index.json"), []byte(idx), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "DunMorogh.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestNormalize(t *testing.T) {
	tests := map[string]string{"Dun Morogh": "dunmorogh", "Ironforge": "ironforge", "Eversong Woods!": "eversongwoods", "": "", "A-b_9": "ab9"}
	for in, want := range tests {
		if got := Normalize(in); got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
}

func TestLoadAndLookup(t *testing.T) {
	d := mk(t)
	ix, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	z, ok := ix.Lookup("Dun Morogh")
	if !ok || z.File != "DunMorogh.png" || z.W != 1002 || z.H != 668 || !z.Revealed || z.Key != "dunmorogh" {
		t.Fatalf("%+v %v", z, ok)
	}
	if _, ok := ix.Lookup("Nowhere"); ok {
		t.Fatal("unexpected")
	}
	if ix.Dir != d {
		t.Fatal("dir")
	}
	if len(ix.List()) != 2 {
		t.Fatal("list")
	}
}

func TestLoadSearchOrderAndMissing(t *testing.T) {
	d := mk(t)
	ix, err := LoadFirst([]string{filepath.Join(t.TempDir(), "nope"), "", d})
	if err != nil || ix.Dir != d {
		t.Fatalf("%v %v", ix, err)
	}
	if _, err := LoadFirst([]string{filepath.Join(t.TempDir(), "nope")}); err != ErrNotFound {
		t.Fatalf("got %v", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadBadJSON(t *testing.T) {
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "index.json"), []byte("{bad"), 0o600)
	if _, err := Load(d); err == nil {
		t.Fatal("expected error")
	}
}

func TestFilePathIsConfined(t *testing.T) {
	ix, _ := Load(mk(t))
	if p, ok := ix.FilePath("DunMorogh.png"); !ok || filepath.Base(p) != "DunMorogh.png" {
		t.Fatalf("%q %v", p, ok)
	}
	for _, bad := range []string{"../secret.png", "index.json", "nothere.png", "", `..\x.png`, "sub/DunMorogh.png"} {
		if _, ok := ix.FilePath(bad); ok {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestCandidates(t *testing.T) {
	c := Candidates(filepath.Join("C:", "app", "dist"), "X")
	if c[0] != "X" || len(c) < 3 {
		t.Fatalf("%v", c)
	}
}
