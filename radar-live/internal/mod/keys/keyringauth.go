package keys

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/keyring"
)

const (
	defaultKeyringPoll = 2 * time.Second
	keyDisabledCode    = "key_disabled"
)

// ringEntry is one keyring key reduced to what request authentication needs.
type ringEntry struct {
	hash      [sha256.Size]byte
	info      core.KeyInfo
	disabled  bool
	expiresAt *time.Time
}

func (e *ringEntry) active(now time.Time) bool {
	return !e.disabled && (e.expiresAt == nil || e.expiresAt.After(now))
}

// ringSnapshot is an immutable set of keyring entries.
type ringSnapshot struct{ entries []ringEntry }

func newSnapshot(kr *keyring.Keyring) *ringSnapshot {
	s := &ringSnapshot{}
	for _, e := range kr.Keys {
		raw, err := hex.DecodeString(e.Hash)
		if err != nil || len(raw) != sha256.Size {
			continue // Validate already guarantees this
		}
		re := ringEntry{
			info:      core.KeyInfo{ID: e.ID, FromKeyring: true, Limits: core.KeyLimits(e.Limits)},
			disabled:  e.Disabled,
			expiresAt: e.ExpiresAt,
		}
		copy(re.hash[:], raw)
		s.entries = append(s.entries, re)
	}
	return s
}

// lookup compares the presented key against every hash (no early exit) and returns the match.
func (s *ringSnapshot) lookup(presented string) *ringEntry {
	if presented == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(presented))
	var found *ringEntry
	for i := range s.entries {
		if subtle.ConstantTimeCompare(sum[:], s.entries[i].hash[:]) == 1 {
			found = &s.entries[i]
		}
	}
	return found
}

// keyringAuth authenticates ingest keys from the keyring file and hot-reloads it.
type keyringAuth struct {
	header string
	path   string
	log    *slog.Logger
	snap   atomic.Pointer[ringSnapshot]

	// poll-goroutine state (also touched once at start, before the goroutine runs)
	lastSize int64
	lastMod  time.Time
	lastOK   bool // lastSize/lastMod describe a file we have looked at

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// Authenticate implements core.Authenticator.
func (a *keyringAuth) Authenticate(r *http.Request) bool {
	_, ok := a.Identify(r)
	return ok
}

// Identify implements core.Identifier: only ACTIVE entries match.
func (a *keyringAuth) Identify(r *http.Request) (core.KeyInfo, bool) {
	e := a.snap.Load().lookup(presentedKey(r, a.header))
	if e == nil || !e.active(time.Now()) {
		return core.KeyInfo{}, false
	}
	return e.info, true
}

// lookupID returns the entry with the given key id.
func (s *ringSnapshot) lookupID(id string) *ringEntry {
	for i := range s.entries {
		if s.entries[i].info.ID == id {
			return &s.entries[i]
		}
	}
	return nil
}

// Lookup implements core.KeyLookup: the current info of a key and whether it is still active.
func (a *keyringAuth) Lookup(id string) (core.KeyInfo, bool) {
	e := a.snap.Load().lookupID(id)
	if e == nil {
		return core.KeyInfo{}, false
	}
	return e.info, e.active(time.Now())
}

// Reject implements core.Rejecter: a known but disabled or expired key is 403, not 401.
func (a *keyringAuth) Reject(r *http.Request) (int, string, string, bool) {
	e := a.snap.Load().lookup(presentedKey(r, a.header))
	if e == nil || e.active(time.Now()) {
		return 0, "", "", false
	}
	return http.StatusForbidden, keyDisabledCode, "key is disabled", true
}

// refresh reloads the file when its size or mtime changed. A bad file keeps the previous set.
func (a *keyringAuth) refresh() {
	st, err := os.Stat(a.path)
	if err != nil {
		a.lastOK = false // a file that appears later is loaded
		return
	}
	if a.lastOK && st.Size() == a.lastSize && st.ModTime().Equal(a.lastMod) {
		return
	}
	a.lastOK, a.lastSize, a.lastMod = true, st.Size(), st.ModTime()
	kr, err := keyring.Load(a.path)
	if err != nil {
		a.log.Warn("keyring file changed but is invalid; keeping the previous keys")
		return
	}
	a.snap.Store(newSnapshot(kr))
	a.log.Info("keyring reloaded", "count", len(kr.Keys))
}

// startKeyring registers the keyring authenticator when keyring-file is set and starts polling.
func (m *Module) startKeyring() error {
	path := m.host.Config.String(KeyKeyringFile)
	if path == "" {
		return nil
	}
	a := &keyringAuth{header: ingestHeader, path: path, log: m.host.Log}
	a.snap.Store(&ringSnapshot{})
	if st, err := os.Stat(path); err == nil {
		a.lastOK, a.lastSize, a.lastMod = true, st.Size(), st.ModTime()
	}
	kr, err := keyring.Load(path)
	switch {
	case err == nil:
		a.snap.Store(newSnapshot(kr))
	case errors.Is(err, os.ErrNotExist):
		a.lastOK = false
	default:
		return fmt.Errorf("keys: keyring: %w", err)
	}
	m.host.Auth.Add(core.RoleIngest, a)
	m.host.Auth.SetKeyLookup(a.Lookup)
	m.host.Log.Info("keyring loaded", "count", len(a.snap.Load().entries))

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel, a.done = cancel, make(chan struct{})
	m.ring = a
	go func() {
		defer close(a.done)
		t := time.NewTicker(m.poll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.refresh()
			}
		}
	}()
	return nil
}

func (m *Module) stopKeyring(context.Context) error {
	if a := m.ring; a != nil {
		a.once.Do(func() { a.cancel(); <-a.done })
	}
	return nil
}
