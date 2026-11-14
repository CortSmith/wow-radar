package client

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const savedTestKey = "saved-key-9999999999999999"

func newSavedStore(t *testing.T, env string) (*KeyStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "RadarClient", savedKeyFileName)
	ks := NewKeyStore(envWith(env))
	if err := ks.EnableSaved(path); err != nil {
		t.Fatal(err)
	}
	return ks, path
}

func TestSavedKeySurvivesARestartAndIsNotInClearText(t *testing.T) {
	ks, path := newSavedStore(t, "")
	if err := ks.Remember("  " + savedTestKey + "\n"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(savedTestKey)) {
		t.Fatal("the key is in the file in clear text")
	}
	if !bytes.HasPrefix(raw, []byte(savedKeyMagic)) {
		t.Fatalf("unexpected file start %q", raw[:4])
	}
	again := NewKeyStore(envWith(""))
	if err := again.EnableSaved(path); err != nil {
		t.Fatal(err)
	}
	if again.Key() != savedTestKey || again.Source() != KeySaved || !again.HasSaved() {
		t.Fatalf("after restart: key %q source %q", again.Key(), again.Source())
	}
}

func TestSavedKeyPrecedenceIsTypedThenEnvThenSaved(t *testing.T) {
	ks, _ := newSavedStore(t, fakeKey)
	if err := ks.Remember(savedTestKey); err != nil {
		t.Fatal(err)
	}
	ks.Set("") // forget the typed copy Remember made, as a restart would
	if ks.Key() != fakeKey || ks.Source() != KeyFromEnv {
		t.Fatalf("env must beat saved: %q %q", ks.Key(), ks.Source())
	}
	ks.Set("typed-key-3333333333")
	if ks.Source() != KeyTyped {
		t.Fatalf("typed must win: %q", ks.Source())
	}
}

func TestForgetDeletesTheFile(t *testing.T) {
	ks, path := newSavedStore(t, "")
	if err := ks.Remember(savedTestKey); err != nil {
		t.Fatal(err)
	}
	ks.Set("")
	if err := ks.Forget(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still there: %v", err)
	}
	if ks.HasSaved() || ks.Key() != "" || ks.Source() != KeyNone {
		t.Fatalf("still has a key: %q %q", ks.Key(), ks.Source())
	}
	if err := ks.Forget(); err != nil {
		t.Fatalf("forgetting twice must be fine: %v", err)
	}
}

func TestDamagedSavedKeyIsReportedAndIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), savedKeyFileName)
	for name, content := range map[string]string{
		"not a key file": "hello",
		"bad seal":       savedKeyMagic + "garbage-garbage-garbage-garbage",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		ks := NewKeyStore(envWith(""))
		if err := ks.EnableSaved(path); err == nil {
			t.Fatalf("%s: want an error", name)
		}
		if ks.Key() != "" || ks.Source() != KeyNone {
			t.Fatalf("%s: a damaged file gave key %q", name, ks.Key())
		}
	}
}

func TestRememberNeedsEnableSavedAndANonEmptyKey(t *testing.T) {
	if err := NewKeyStore(envWith("")).Remember(savedTestKey); err == nil {
		t.Fatal("Remember without EnableSaved must fail")
	}
	ks, _ := newSavedStore(t, "")
	if err := ks.Remember("   "); err == nil {
		t.Fatal("an empty key must not be saved")
	}
}

func TestKeyStoreStringNeverShowsTheSavedKey(t *testing.T) {
	ks, _ := newSavedStore(t, "")
	if err := ks.Remember(savedTestKey); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ks.String(), savedTestKey) {
		t.Fatal("key leaked through String")
	}
}

func TestAESGCMFallbackRoundTripsAndRejectsTampering(t *testing.T) {
	sealed, err := sealAESGCM([]byte(savedTestKey))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(savedTestKey)) {
		t.Fatal("sealed bytes contain the key")
	}
	plain, err := openAESGCM(sealed)
	if err != nil || string(plain) != savedTestKey {
		t.Fatalf("round trip: %q %v", plain, err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := openAESGCM(sealed); err == nil {
		t.Fatal("tampered data was accepted")
	}
}
