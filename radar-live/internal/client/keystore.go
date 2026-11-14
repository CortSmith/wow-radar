package client

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Where the ingest key came from.
const (
	KeyNone    = "none"  // no key known: the UI asks for one
	KeyFromEnv = "env"   // RADAR_CLIENT_INGEST_KEY
	KeyTyped   = "typed" // typed into the UI for this run
	KeySaved   = "saved" // remembered on this device (opt-in), sealed on disk
)

// KeyStore holds the ingest key in memory. The key is never logged or sent to the UI, and it is written
// to disk only when the user asks to remember it (EnableSaved/Remember), then only sealed (savedkey.go).
// A key typed into the UI replaces the environment's key until the process ends; the order is typed,
// then the environment, then the saved key.
type KeyStore struct {
	lookup    func(string) (string, bool)
	mu        sync.Mutex
	typed     string
	saved     string
	savedPath string
	sealer    sealer
}

// NewKeyStore returns a store that falls back to EnvKey read through lookup.
func NewKeyStore(lookup func(string) (string, bool)) *KeyStore {
	return &KeyStore{lookup: lookup}
}

// EnableSaved turns on the opt-in saved key stored at path and loads it. A damaged or foreign file is
// reported and ignored; the store keeps working without a saved key.
func (k *KeyStore) EnableSaved(path string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.savedPath, k.sealer = path, defaultSealer()
	key, err := readSavedKey(path, k.sealer)
	k.saved = key
	return err
}

// CanSave reports whether Remember is available.
func (k *KeyStore) CanSave() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.savedPath != ""
}

// HasSaved reports whether a key is remembered on this device (whether or not another source wins).
func (k *KeyStore) HasSaved() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.saved != ""
}

// Remember uses key for this run and stores it sealed on disk for the next runs.
func (k *KeyStore) Remember(key string) error {
	key = strings.TrimSpace(key)
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.savedPath == "" {
		return errors.New("saving the key is not available")
	}
	if key == "" {
		return errors.New("no key to save")
	}
	if err := writeSavedKey(k.savedPath, key, k.sealer); err != nil {
		return err
	}
	k.typed, k.saved = key, key
	return nil
}

// Forget deletes the saved key (memory and file). A key typed this run stays until the process ends.
func (k *KeyStore) Forget() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.saved = ""
	if k.savedPath == "" {
		return nil
	}
	if err := os.Remove(k.savedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete saved key: %w", err)
	}
	return nil
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
	typed, saved := k.typed, k.saved
	k.mu.Unlock()
	if typed != "" {
		return typed, KeyTyped
	}
	if v, ok := k.lookup(EnvKey); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), KeyFromEnv
	}
	if saved != "" {
		return saved, KeySaved
	}
	return "", KeyNone
}

// String never reveals the key, so a stray log line or %v cannot leak it.
func (k *KeyStore) String() string { return "KeyStore(" + k.Source() + ")" }
