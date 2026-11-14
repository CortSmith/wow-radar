package keyvault

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	id1 = "9f2c0000000000000000000000000001"
	id2 = "9f2c0000000000000000000000000002"
	key = "super-secret-ingest-key-value-0123456789"
)

func open(t *testing.T) (*Vault, string, string) {
	t.Helper()
	dir := t.TempDir()
	vp, sp := filepath.Join(dir, "keyvault.bin"), filepath.Join(dir, "keyvault.key")
	v, err := Open(vp, sp)
	if err != nil {
		t.Fatal(err)
	}
	return v, vp, sp
}

func TestRoundTrip(t *testing.T) {
	v, vp, sp := open(t)
	if ok, _ := v.Has(id1); ok {
		t.Fatal("has before put")
	}
	if k, ok, err := v.Get(id1); ok || err != nil || k != "" {
		t.Fatalf("empty get: %q %v %v", k, ok, err)
	}
	if err := v.Put(id1, key); err != nil {
		t.Fatal(err)
	}
	if err := v.Put(id2, "other"); err != nil {
		t.Fatal(err)
	}
	if k, ok, err := v.Get(id1); k != key || !ok || err != nil {
		t.Fatalf("get: %q %v %v", k, ok, err)
	}
	if ok, err := v.Has(id2); !ok || err != nil {
		t.Fatal("has")
	}
	raw, _ := os.ReadFile(vp)
	if strings.Contains(string(raw), key) {
		t.Fatal("key stored in clear")
	}
	if sk, _ := os.ReadFile(sp); len(sk) != SealKeyBytes {
		t.Fatalf("seal key len %d", len(sk))
	}
	// Reopen with the same files.
	v2, err := Open(vp, sp)
	if err != nil {
		t.Fatal(err)
	}
	if k, ok, _ := v2.Get(id1); !ok || k != key {
		t.Fatal("reopen")
	}
	// Overwrite and delete.
	if err := v2.Put(id1, "new"); err != nil {
		t.Fatal(err)
	}
	if k, _, _ := v.Get(id1); k != "new" {
		t.Fatal("overwrite")
	}
	if err := v.Delete(id1); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete(id1); err != nil {
		t.Fatal("delete absent:", err)
	}
	if ok, _ := v.Has(id1); ok {
		t.Fatal("still there")
	}
	if ok, _ := v.Has(id2); !ok {
		t.Fatal("id2 lost")
	}
}

func TestRandomNonce(t *testing.T) {
	v, vp, _ := open(t)
	v.Put(id1, key)
	v.Put(id2, key)
	var f file
	raw, _ := os.ReadFile(vp)
	json.Unmarshal(raw, &f)
	if f.Entries[id1] == f.Entries[id2] {
		t.Fatal("identical ciphertexts for same key")
	}
}

func TestWrongSealingKey(t *testing.T) {
	v, vp, _ := open(t)
	v.Put(id1, key)
	other, err := Open(vp, filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	k, ok, err := other.Get(id1)
	if err == nil || ok || k != "" || !errors.Is(err, ErrOpen) {
		t.Fatalf("wrong key: %q %v %v", k, ok, err)
	}
	if strings.Contains(err.Error(), key) {
		t.Fatal("leak")
	}
}

func TestTamper(t *testing.T) {
	v, vp, _ := open(t)
	v.Put(id1, key)
	v.Put(id2, "second")
	var f file
	raw, _ := os.ReadFile(vp)
	json.Unmarshal(raw, &f)

	write := func(f file) {
		b, _ := json.Marshal(f)
		os.WriteFile(vp, b, 0o600)
	}
	// Moving a sealed entry to another id fails (id is additional data).
	swap := file{Version: 1, Entries: map[string]string{id1: f.Entries[id2], id2: f.Entries[id1]}}
	write(swap)
	for _, id := range []string{id1, id2} {
		if k, ok, err := v.Get(id); err == nil || ok || k != "" {
			t.Fatalf("swap accepted for %s", id)
		}
	}
	// Flip a byte of the ciphertext.
	c := []byte(f.Entries[id1])
	if c[20] == 'A' {
		c[20] = 'B'
	} else {
		c[20] = 'A'
	}
	write(file{Version: 1, Entries: map[string]string{id1: string(c)}})
	if _, _, err := v.Get(id1); err == nil {
		t.Fatal("flipped byte accepted")
	}
	// Garbage, truncated, not base64.
	for _, e := range []string{"", "!!!", "AAAA"} {
		write(file{Version: 1, Entries: map[string]string{id1: e}})
		if _, _, err := v.Get(id1); err == nil {
			t.Fatalf("entry %q accepted", e)
		}
	}
}

func TestBadVaultFiles(t *testing.T) {
	v, vp, _ := open(t)
	for name, content := range map[string]string{
		"corrupt": "{not json",
		"version": `{"version":9,"entries":{}}`,
		"big":     `{"version":1,"entries":{},"p":"` + strings.Repeat("x", MaxFileBytes) + `"}`,
	} {
		os.WriteFile(vp, []byte(content), 0o600)
		if _, _, err := v.Get(id1); err == nil {
			t.Errorf("%s accepted by Get", name)
		}
		if err := v.Put(id1, key); err == nil {
			t.Errorf("%s accepted by Put", name)
		}
	}
}

func TestSealKeyFile(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "s.key")
	for _, n := range []int{1, 31, 33, 64} {
		os.WriteFile(sp, make([]byte, n), 0o600)
		if _, err := Open(filepath.Join(dir, "v"), sp); err == nil {
			t.Errorf("seal key of %d bytes accepted", n)
		}
	}
	os.WriteFile(sp, nil, 0o600)
	if _, err := Open(filepath.Join(dir, "v"), sp); err == nil {
		t.Error("empty seal key accepted")
	}
	// Created on first use, kept on reopen.
	sp2 := filepath.Join(dir, "new.key")
	if _, err := Open(filepath.Join(dir, "v"), sp2); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(sp2)
	Open(filepath.Join(dir, "v"), sp2)
	b, _ := os.ReadFile(sp2)
	if string(a) != string(b) || len(a) != SealKeyBytes {
		t.Fatal("seal key changed")
	}
	if fi, _ := os.Stat(sp2); os.PathSeparator == '/' && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestInputChecksAndNoTempFiles(t *testing.T) {
	v, vp, _ := open(t)
	for _, id := range []string{"", "short", strings.ToUpper(id1), strings.Repeat("g", 32)} {
		if v.Put(id, key) == nil {
			t.Errorf("id %q accepted", id)
		}
		if _, _, err := v.Get(id); err == nil {
			t.Errorf("get id %q accepted", id)
		}
	}
	if v.Put(id1, "") == nil || v.Put(id1, strings.Repeat("k", 2000)) == nil {
		t.Error("bad key accepted")
	}
	for i := 0; i < 5; i++ {
		v.Put(fmt.Sprintf("%032x", i), key)
	}
	ents, _ := os.ReadDir(filepath.Dir(vp))
	for _, e := range ents {
		switch e.Name() {
		case "keyvault.bin", "keyvault.bin.lock", "keyvault.key":
		default:
			t.Errorf("stray file %s", e.Name())
		}
	}
}

func TestConcurrentPuts(t *testing.T) {
	v, _, _ := open(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := v.Put(fmt.Sprintf("%032x", i), fmt.Sprintf("key-%d", i)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for i := 0; i < 20; i++ {
		if k, ok, err := v.Get(fmt.Sprintf("%032x", i)); err != nil || !ok || k != fmt.Sprintf("key-%d", i) {
			t.Fatalf("entry %d: %q %v %v", i, k, ok, err)
		}
	}
}
