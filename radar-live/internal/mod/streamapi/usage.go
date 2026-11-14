package streamapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"radarlive/internal/keyring"
)

// Key usage: when keyusage-file is set, the module counts per key id (the keyring id, also derived for
// env keys) the last time the key was seen and the lines and bytes of the server's local calendar day,
// and flushes them to a small JSON file that radar-keys reads. The file holds only ids, times and counts.
const (
	SettingKeyUsageFile = "keyusage-file"

	usageVersion      = 1
	defaultUsageFlush = 60 * time.Second
	maxUsageKeys      = 4096
	usageMaxAge       = 30 * 24 * time.Hour
	maxUsageFileBytes = 8 << 20
)

// emptyKey is the id of "no key presented" (an open ingest without keys): it is not a key, so not counted.
var emptyKey = func() keyID { s := sha256.Sum256(nil); return keyID(s[:keyIDBytes]) }()

// usageEntry is one key's counters. The hot path only does atomic adds, once per line.
type usageEntry struct {
	last  atomic.Int64 // unix nanoseconds of the most recent accepted line or stream open
	lines atomic.Int64
	bytes atomic.Int64
	dirty atomic.Bool
}

type usageView struct {
	LastSeen   time.Time `json:"lastSeen"`
	LinesToday int64     `json:"linesToday"`
	BytesToday int64     `json:"bytesToday"`
}

type usageDoc struct {
	Version int                  `json:"version"`
	Keys    map[string]usageView `json:"keys"`
}

// usageTracker keeps the entries. The mutex guards the map and the day only; line counting does not take it.
type usageTracker struct {
	path string
	now  func() time.Time

	next atomic.Int64 // unix nanoseconds of the next local midnight

	mu      sync.Mutex
	entries map[string]*usageEntry
}

func newUsageTracker(path string, now func() time.Time) *usageTracker {
	u := &usageTracker{path: path, now: now, entries: map[string]*usageEntry{}}
	u.next.Store(nextMidnight(now()).UnixNano())
	u.load()
	return u
}

func nextMidnight(t time.Time) time.Time {
	y, mo, d := t.Date()
	return time.Date(y, mo, d+1, 0, 0, 0, 0, t.Location())
}

func startOfDay(t time.Time) time.Time {
	y, mo, d := t.Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, t.Location())
}

// load reads the existing file: a missing or invalid file starts empty. Counts survive only when the key was
// last seen today; entries unseen for 30 days are dropped.
func (u *usageTracker) load() {
	f, err := os.Open(u.path)
	if err != nil {
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxUsageFileBytes+1))
	if err != nil || len(data) > maxUsageFileBytes {
		return
	}
	var doc usageDoc
	if json.Unmarshal(data, &doc) != nil || doc.Version != usageVersion {
		return
	}
	now := u.now()
	today := startOfDay(now)
	for id, v := range doc.Keys {
		if id == "" || len(id) > 64 || v.LastSeen.IsZero() || now.Sub(v.LastSeen) > usageMaxAge {
			continue
		}
		e := &usageEntry{}
		e.last.Store(v.LastSeen.UnixNano())
		if !v.LastSeen.Before(today) && v.LinesToday >= 0 && v.BytesToday >= 0 {
			e.lines.Store(v.LinesToday)
			e.bytes.Store(v.BytesToday)
		}
		u.entries[id] = e
	}
	for len(u.entries) > maxUsageKeys {
		u.evictOldest()
	}
}

func (u *usageTracker) evictOldest() {
	var oldest string
	var at int64
	for id, e := range u.entries {
		if l := e.last.Load(); oldest == "" || l < at {
			oldest, at = id, l
		}
	}
	delete(u.entries, oldest)
}

// touch returns the entry of a key and marks it seen (a stream open or a batch).
func (u *usageTracker) touch(id string) *usageEntry {
	now := u.now()
	u.rollover(now)
	u.mu.Lock()
	e := u.entries[id]
	if e == nil {
		for len(u.entries) >= maxUsageKeys {
			u.evictOldest()
		}
		e = &usageEntry{}
		u.entries[id] = e
	}
	u.mu.Unlock()
	e.last.Store(now.UnixNano())
	e.dirty.Store(true)
	return e
}

// add counts lines and bytes accepted on a key's stream or batch.
func (u *usageTracker) add(e *usageEntry, lines, bytes int64) {
	now := u.now()
	u.rollover(now)
	e.lines.Add(lines)
	e.bytes.Add(bytes)
	e.last.Store(now.UnixNano())
	e.dirty.Store(true)
}

// rollover zeroes the day counters when the local calendar day has changed.
func (u *usageTracker) rollover(now time.Time) {
	if now.UnixNano() < u.next.Load() {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if now.UnixNano() < u.next.Load() {
		return
	}
	u.next.Store(nextMidnight(now).UnixNano())
	for _, e := range u.entries {
		e.lines.Store(0)
		e.bytes.Store(0)
		e.dirty.Store(true)
	}
}

// flush writes the file when something changed since the last flush.
func (u *usageTracker) flush() error {
	now := u.now()
	u.rollover(now)
	u.mu.Lock()
	changed := false
	doc := usageDoc{Version: usageVersion, Keys: make(map[string]usageView, len(u.entries))}
	for id, e := range u.entries {
		last := time.Unix(0, e.last.Load())
		if now.Sub(last) > usageMaxAge {
			delete(u.entries, id)
			changed = true
			continue
		}
		if e.dirty.Swap(false) {
			changed = true
		}
		doc.Keys[id] = usageView{LastSeen: last.Truncate(time.Second), LinesToday: e.lines.Load(), BytesToday: e.bytes.Load()}
	}
	u.mu.Unlock()
	if !changed {
		return nil
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := keyring.WriteFileAtomic(u.path, append(data, '\n')); err != nil {
		u.markAllDirty()
		return err
	}
	return nil
}

func (u *usageTracker) markAllDirty() {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, e := range u.entries {
		e.dirty.Store(true)
	}
}

// run flushes every interval until ctx ends, then once more.
func (u *usageTracker) run(ctx context.Context, every time.Duration, warn func(error)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := u.flush(); err != nil {
				warn(err)
			}
			return
		case <-t.C:
			if err := u.flush(); err != nil {
				warn(err)
			}
		}
	}
}

// keyIDHex is the key id as written in keyring.json and the usage file.
func keyIDHex(k keyID) string { return hex.EncodeToString(k[:]) }

// trackUsage attaches the key's usage entry to a freshly opened session (no-op when the feature is off).
func (m *Module) trackUsage(s *session) {
	if m.usage == nil || s.keyID == emptyKey {
		return
	}
	s.use = m.usage.touch(keyIDHex(s.keyID))
}

// countUsage adds accepted lines and bytes to the session's key.
func (m *Module) countUsage(s *session, lines, bytes int64) {
	if s.use != nil {
		m.usage.add(s.use, lines, bytes)
	}
}
