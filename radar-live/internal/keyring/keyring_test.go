package keyring

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const secretEmail = "secret.person@example.com"

func i64(v int64) *int64 { return &v }

func newTestEntry(t *testing.T, email string) (Entry, string) {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return NewEntry(k, email, time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)), k
}

func TestKeyDerivation(t *testing.T) {
	k, err := GenerateKey()
	if err != nil || len(k) != 43 || strings.ContainsAny(k, "=+/") {
		t.Fatalf("GenerateKey = %q, %v", k, err)
	}
	k2, _ := GenerateKey()
	if k == k2 {
		t.Fatal("keys repeat")
	}
	// Same derivation as streamapi keyOf: first 16 bytes of sha256 of the presented key.
	sample := "sample-key-value"
	sum := sha256.Sum256([]byte(sample))
	if got, want := IDFromKey(sample), hex.EncodeToString(sum[:16]); got != want {
		t.Fatalf("IDFromKey = %s want %s", got, want)
	}
	if got := HashKey(sample); got != hex.EncodeToString(sum[:]) || !strings.HasPrefix(got, IDFromKey(sample)) {
		t.Fatalf("HashKey = %s", got)
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	e, _ := newTestEntry(t, secretEmail)
	e.Player, e.Notes, e.Tags = "Arthas-Faerlina", "free text", []string{"guild:ashes", "tester"}
	exp := e.CreatedAt.Add(24 * time.Hour)
	e.ExpiresAt = &exp
	e.Limits = Limits{LineRate: i64(100), MaxStreams: i64(2)}
	kr := &Keyring{Keys: []Entry{e}}
	if err := Save(path, kr); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Keys) != 1 || got.Version != 1 {
		t.Fatalf("got %+v", got)
	}
	g := got.Keys[0]
	if g.ID != e.ID || g.Email != e.Email || g.Player != e.Player || len(g.Tags) != 2 ||
		!g.CreatedAt.Equal(e.CreatedAt) || g.ExpiresAt == nil || !g.ExpiresAt.Equal(exp) ||
		*g.Limits.LineRate != 100 || g.Limits.LineBurst != nil || *g.Limits.MaxStreams != 2 {
		t.Fatalf("mismatch: %+v", g)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{`"lineBurst": null`, `"expiresAt"`, `"disabledAt": null`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("file missing %s", want)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("mode %v", fi.Mode())
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keyring.json")
	for i := 0; i < 5; i++ {
		e, _ := newTestEntry(t, "a@example.com")
		if err := Update(path, func(kr *Keyring) error { kr.Keys = append(kr.Keys, e); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	// A failing save must not leave a temp file or touch the original.
	before, _ := os.ReadFile(path)
	bad := &Keyring{Version: 1, Keys: []Entry{{ID: "x"}}}
	if err := Save(path, bad); err == nil {
		t.Fatal("expected error")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("file changed by failed save")
	}
	ents, _ := os.ReadDir(dir)
	for _, en := range ents {
		if n := en.Name(); n != "keyring.json" && n != "keyring.json.lock" {
			t.Errorf("stray file %s", n)
		}
	}
}

func TestUpdateFnErrorWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	boom := errors.New("boom")
	if err := Update(path, func(*Keyring) error { return boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file created")
	}
}

func TestConcurrentUpdateLosesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	const workers, each = 8, 6
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				k, _ := GenerateKey()
				e := NewEntry(k, "p@example.com", time.Now())
				errs <- Update(path, func(kr *Keyring) error { kr.Keys = append(kr.Keys, e); return nil })
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	kr, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(kr.Keys) != workers*each {
		t.Fatalf("have %d entries, want %d", len(kr.Keys), workers*each)
	}
}

func TestValidateRejects(t *testing.T) {
	base, _ := newTestEntry(t, "ok@example.com")
	cases := map[string]func(e *Entry){
		"empty email":      func(e *Entry) { e.Email = "" },
		"no at":            func(e *Entry) { e.Email = "nobody" },
		"display name":     func(e *Entry) { e.Email = "Bob <bob@example.com>" },
		"no domain dot":    func(e *Entry) { e.Email = "bob@localhost" },
		"long email":       func(e *Entry) { e.Email = strings.Repeat("a", 250) + "@example.com" },
		"long player":      func(e *Entry) { e.Player = strings.Repeat("p", MaxPlayerLen+1) },
		"ctrl player":      func(e *Entry) { e.Player = "a\nb" },
		"long notes":       func(e *Entry) { e.Notes = strings.Repeat("n", MaxNotesLen+1) },
		"long reason":      func(e *Entry) { e.DisabledReason = strings.Repeat("r", MaxReasonLen+1) },
		"too many tags":    func(e *Entry) { e.Tags = make([]string, MaxTags+1); fillTags(e.Tags) },
		"long tag":         func(e *Entry) { e.Tags = []string{strings.Repeat("t", MaxTagLen+1)} },
		"empty tag":        func(e *Entry) { e.Tags = []string{""} },
		"bad tag char":     func(e *Entry) { e.Tags = []string{"a b"} },
		"dup tag":          func(e *Entry) { e.Tags = []string{"a", "a"} },
		"zero rate":        func(e *Entry) { e.Limits.LineRate = i64(0) },
		"negative burst":   func(e *Entry) { e.Limits.LineBurst = i64(-1) },
		"zero byte rate":   func(e *Entry) { e.Limits.ByteRate = i64(0) },
		"zero byte burst":  func(e *Entry) { e.Limits.ByteBurst = i64(0) },
		"zero streams":     func(e *Entry) { e.Limits.MaxStreams = i64(0) },
		"huge rate":        func(e *Entry) { e.Limits.LineRate = i64(MaxLineRate + 1) },
		"huge streams":     func(e *Entry) { e.Limits.MaxStreams = i64(MaxMaxStreams + 1) },
		"huge byte burst":  func(e *Entry) { e.Limits.ByteBurst = i64(MaxByteBurst + 1) },
		"short hash":       func(e *Entry) { e.Hash = e.Hash[:10] },
		"upper hash":       func(e *Entry) { e.Hash = strings.ToUpper(e.Hash) },
		"id/hash mismatch": func(e *Entry) { e.ID = strings.Repeat("0", 32) },
	}
	for name, mut := range cases {
		e := base
		mut(&e)
		kr := &Keyring{Version: 1, Keys: []Entry{e}}
		err := kr.Validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		checkNoSecrets(t, err, base.Email, e.Email)
	}
	good := base
	good.Tags = []string{"guild:ashes", "Tester_1.x-y"}
	good.Limits = Limits{LineRate: i64(1), LineBurst: i64(MaxLineBurst), ByteRate: i64(MaxByteRate), ByteBurst: i64(MaxByteBurst), MaxStreams: i64(MaxMaxStreams)}
	if err := (&Keyring{Version: 1, Keys: []Entry{good}}).Validate(); err != nil {
		t.Fatalf("good rejected: %v", err)
	}
}

func fillTags(t []string) {
	for i := range t {
		t[i] = fmt.Sprintf("t%d", i)
	}
}

func TestDuplicates(t *testing.T) {
	a, _ := newTestEntry(t, "a@example.com")
	b, _ := newTestEntry(t, "b@example.com")
	if err := (&Keyring{Version: 1, Keys: []Entry{a, b}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (&Keyring{Version: 1, Keys: []Entry{a, a}}).Validate(); err == nil {
		t.Fatal("duplicate accepted")
	}
	// Same hash, different id cannot pass either (id must match hash), so test hash dup via id dup path above.
	c := b
	c.Hash = a.Hash
	if err := (&Keyring{Version: 1, Keys: []Entry{a, c}}).Validate(); err == nil {
		t.Fatal("hash duplicate accepted")
	}
}

func TestLoadBadFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	e, _ := newTestEntry(t, secretEmail)
	good := Keyring{Version: 1, Keys: []Entry{e}}
	if err := Save(filepath.Join(dir, "good.json"), &good); err != nil {
		t.Fatal(err)
	}
	goodRaw, _ := os.ReadFile(filepath.Join(dir, "good.json"))
	for name, content := range map[string]string{
		"corrupt":    `{"version":1,"keys":[` + secretEmail,
		"garbage":    "\x00\x01\x02",
		"empty":      "",
		"version0":   `{"version":0,"keys":[]}`,
		"version2":   `{"version":2,"keys":[]}`,
		"unknownfld": `{"version":1,"keys":[],"extra":1}`,
		"trailing":   string(goodRaw) + `{"x":1}`,
		"emailtype":  `{"version":1,"keys":[{"email":123}]}`,
		"bademail":   strings.Replace(string(goodRaw), secretEmail, "not an email "+secretEmail, 1),
	} {
		_, err := Load(write(name+".json", content))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		checkNoSecrets(t, err, secretEmail)
	}
	big := write("big.json", `{"version":1,"keys":[],"pad":"`+strings.Repeat("x", MaxFileBytes)+`"}`)
	if _, err := Load(big); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("oversized: %v", err)
	}
	if _, err := Load(filepath.Join(dir, "good.json")); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsHaveNoSecrets(t *testing.T) {
	e, key := newTestEntry(t, secretEmail)
	e.Notes = strings.Repeat("n", MaxNotesLen+1)
	err := (&Keyring{Version: 1, Keys: []Entry{e}}).Validate()
	checkNoSecrets(t, err, key, secretEmail, e.Hash)
	e.Notes = ""
	e.Email = "bad" + secretEmail + " x"
	err = (&Keyring{Version: 1, Keys: []Entry{e}}).Validate()
	checkNoSecrets(t, err, key, secretEmail)
	path := filepath.Join(t.TempDir(), "k.json")
	err = Save(path, &Keyring{Keys: []Entry{e}})
	checkNoSecrets(t, err, key, secretEmail)
}

func checkNoSecrets(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(err.Error(), s) {
			t.Errorf("error leaks %q: %v", s, err)
		}
	}
	if strings.Contains(err.Error(), "@") {
		t.Errorf("error contains an address: %v", err)
	}
}
