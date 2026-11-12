package client

import (
	"fmt"
	"strings"
	"testing"
)

const fakeKey = "test-key-0000000000000000"

func envWith(key string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if name == EnvKey && key != "" {
			return key, true
		}
		return "", false
	}
}

func TestKeyStoreUsesTheEnvironmentKey(t *testing.T) {
	ks := NewKeyStore(envWith(fakeKey))
	if ks.Key() != fakeKey || ks.Source() != KeyFromEnv {
		t.Fatalf("key %q source %q", ks.Key(), ks.Source())
	}
}

func TestKeyStoreTypedKeyOverridesEnvAndCanBeCleared(t *testing.T) {
	ks := NewKeyStore(envWith(fakeKey))
	ks.Set("  typed-key-1111111111  ")
	if ks.Key() != "typed-key-1111111111" || ks.Source() != KeyTyped {
		t.Fatalf("key %q source %q", ks.Key(), ks.Source())
	}
	ks.Set("")
	if ks.Key() != fakeKey || ks.Source() != KeyFromEnv {
		t.Fatalf("after clear: key %q source %q", ks.Key(), ks.Source())
	}
}

func TestKeyStoreWithoutAnyKey(t *testing.T) {
	ks := NewKeyStore(envWith(""))
	if ks.Key() != "" || ks.Source() != KeyNone {
		t.Fatalf("key %q source %q", ks.Key(), ks.Source())
	}
}

func TestKeyStoreNeverPrintsTheKey(t *testing.T) {
	ks := NewKeyStore(envWith(fakeKey))
	for _, out := range []string{fmt.Sprint(ks), fmt.Sprintf("%v %+v %s", ks, ks, ks)} {
		if strings.Contains(out, fakeKey) {
			t.Fatalf("key leaked: %s", out)
		}
	}
}

func TestKeyStoreTrimsWhitespaceAndNewlinesFromEveryKeySource(t *testing.T) {
	ks := NewKeyStore(envWith("\r\n  " + fakeKey + " \n"))
	if ks.Key() != fakeKey {
		t.Fatalf("env key %q", ks.Key())
	}
	ks.Set("\ttyped-key-2222222222\r\n")
	if ks.Key() != "typed-key-2222222222" {
		t.Fatalf("typed key %q", ks.Key())
	}
	ks.Set(" \n ") // only whitespace counts as no key
	if ks.Key() != fakeKey || ks.Source() != KeyFromEnv {
		t.Fatalf("whitespace-only key: %q %q", ks.Key(), ks.Source())
	}
}
