package client

import (
	"strings"
	"sync"
)

// Where the ingest key came from.
const (
	KeyNone    = "none"  // no key known: the UI asks for one
	KeyFromEnv = "env"   // RADAR_CLIENT_INGEST_KEY
	KeyTyped   = "typed" // typed into the UI for this run
)

// KeyStore holds the ingest key in memory. The key is never written to disk, logged or sent to the
// UI; a key typed into the UI replaces the environment's key until the process ends.
type KeyStore struct {
	lookup func(string) (string, bool)
	mu     sync.Mutex
	typed  string
}

// NewKeyStore returns a store that falls back to EnvKey read through lookup.
func NewKeyStore(lookup func(string) (string, bool)) *KeyStore {
	return &KeyStore{lookup: lookup}
}

// Set remembers a typed key for this run; an empty key forgets it.
func (k *KeyStore) Set(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.typed = strings.TrimSpace(key)
}

// Key returns the key to send ("" if there is none).
func (k *KeyStore) Key() string {
	key, _ := k.resolve()
	return key
}

// Source says where Key comes from: KeyTyped, KeyFromEnv or KeyNone.
func (k *KeyStore) Source() string {
	_, source := k.resolve()
	return source
}

func (k *KeyStore) resolve() (key, source string) {
	k.mu.Lock()
	typed := k.typed
	k.mu.Unlock()
	if typed != "" {
		return typed, KeyTyped
	}
	if v, ok := k.lookup(EnvKey); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), KeyFromEnv
	}
	return "", KeyNone
}

// String never reveals the key, so a stray log line or %v cannot leak it.
func (k *KeyStore) String() string { return "KeyStore(" + k.Source() + ")" }
