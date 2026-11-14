package streamapi

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// maxTrackedContacts bounds the per-session set used to count distinct players.
const maxTrackedContacts = 10_000

var (
	errSourceBusy = errors.New("source already connected")
	errTooMany    = errors.New("too many concurrent sources")
	errTooStreams = errors.New("too many concurrent streams")
	errKeyStreams = errors.New("too many concurrent streams for this key")
)

// session is one live connection (a stream or a batch) identified by its source id.
type session struct {
	source string
	rs     domain.Ruleset // explicit override, "" when auto-detected
	since  time.Time
	keyID  keyID
	lim    *limiter // this session's own buckets, nil when the limits are off
	key    *limiter // the key's shared buckets

	ring    string      // keyring id when the key comes from the keyring, else ""
	use     *usageEntry // usage counters of the key, nil when keyusage-file is off
	blocked atomic.Bool // the key was disabled, expired or removed while open

	throttleLast  time.Time // when the reader last had to wait; only the reading goroutine
	throttleSince time.Time // start of the current uninterrupted throttling; only the reading goroutine
	throttledMs   atomic.Int64

	lines     atomic.Int64
	events    atomic.Int64
	rejected  atomic.Int64
	lastEvent atomic.Int64 // newest event time (ms since epoch)

	mu       sync.Mutex
	detected domain.Ruleset
	players  map[string]struct{}
}

// observe records one parsed event of this session.
func (s *session) observe(ev domain.Event) {
	s.events.Add(1)
	if ev.TimeMs > s.lastEvent.Load() {
		s.lastEvent.Store(ev.TimeMs)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.Ruleset != "" {
		s.detected = ev.Ruleset
	}
	for _, u := range []domain.EventUnit{ev.Src, ev.Dst} {
		if u.IsPlayer() && len(s.players) < maxTrackedContacts {
			s.players[u.GUID] = struct{}{}
		}
	}
}

func (s *session) ruleset() domain.Ruleset {
	if s.rs != "" {
		return s.rs
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detected
}

func (s *session) contacts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.players)
}

// summary is the reply to a finished stream or batch.
type summary struct {
	Source     string `json:"source"`
	Lines      int64  `json:"lines"`
	Events     int64  `json:"events"`
	Contacts   int    `json:"contacts"`
	Rejected   int64  `json:"rejected"`
	DurationMs int64  `json:"durationMs"`
	Throttled  int64  `json:"throttledMs"`
	Ended      string `json:"ended"`
}

func (s *session) summary(now time.Time, ended string) summary {
	return summary{Source: s.source, Lines: s.lines.Load(), Events: s.events.Load(), Contacts: s.contacts(),
		Rejected: s.rejected.Load(), DurationMs: now.Sub(s.since).Milliseconds(), Throttled: s.throttledMs.Load(), Ended: ended}
}

// sourceInfo is one row of GET /v1/sources.
type sourceInfo struct {
	Source      string  `json:"source"`
	RS          string  `json:"rs"`
	Since       int64   `json:"since"`
	Lines       int64   `json:"lines"`
	LinesPerSec float64 `json:"linesPerSec"`
	LastEvent   int64   `json:"lastEvent"`
	Rejected    int64   `json:"rejected"`
	ThrottledMs int64   `json:"throttledMs"`
}

func (s *session) info(now time.Time) sourceInfo {
	lines := s.lines.Load()
	rate := 0.0
	if secs := now.Sub(s.since).Seconds(); secs > 0 {
		rate = float64(lines) / secs
	}
	return sourceInfo{Source: s.source, RS: string(s.ruleset()), Since: s.since.UnixMilli(), Lines: lines,
		LinesPerSec: rate, LastEvent: s.lastEvent.Load(), Rejected: s.rejected.Load(), ThrottledMs: s.throttledMs.Load()}
}

// registry holds the live sessions; at most max exist and a source id is unique among them. It also
// counts open streams and keeps the shared limiter per ingest key.
type registry struct {
	max int
	lim limits

	mu       sync.RWMutex
	sessions map[string]*session
	keys     map[keyID]*keyState
}

func newRegistry(max int) *registry {
	return &registry{max: max, sessions: map[string]*session{}, keys: map[keyID]*keyState{}}
}

func (r *registry) open(source string, rs domain.Ruleset, key keyID, now time.Time, info ...core.KeyInfo) (*session, error) {
	var ki core.KeyInfo
	if len(info) > 0 {
		ki = info[0]
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.sessions[source]; busy {
		return nil, errSourceBusy
	}
	if len(r.sessions) >= r.max {
		return nil, errTooMany
	}
	if r.lim.maxStreams > 0 && len(r.sessions) >= r.lim.maxStreams {
		return nil, errTooStreams
	}
	kl := r.lim.keyLimits(ki, ki.FromKeyring)
	ks := r.keyFor(key, now, kl)
	if kl.maxStreams > 0 && ks.streams >= kl.maxStreams {
		return nil, errKeyStreams
	}
	ks.streams++
	s := &session{source: source, rs: rs, since: now, keyID: key, key: ks.lim, ring: ringID(ki), players: map[string]struct{}{},
		lim: newLimiter(r.lim.lineRate, r.lim.lineBurst, r.lim.byteRate, r.lim.byteBurst, now)}
	r.sessions[source] = s
	return s, nil
}

// keyFor returns the key's state, creating it; the table is bounded by dropping idle keys.
func (r *registry) keyFor(key keyID, now time.Time, kl keyLim) *keyState {
	ks := r.keys[key]
	if ks == nil {
		if len(r.keys) >= maxTrackedKeys {
			r.dropIdleKeys()
		}
		ks = &keyState{lim: newLimiter(kl.lineRate, kl.lineBurst, kl.byteRate, kl.byteBurst, now), cur: kl}
		r.keys[key] = ks
	}
	if ks.cur != kl {
		ks.lim.reconfigure(kl, now)
		ks.cur = kl
	}
	ks.used = now
	return ks
}

func (r *registry) dropIdleKeys() {
	for k, ks := range r.keys {
		if ks.streams == 0 && len(r.keys) > maxTrackedKeys/2 {
			delete(r.keys, k)
		}
	}
}

func (r *registry) close(s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[s.source] == s {
		delete(r.sessions, s.source)
		if ks := r.keys[s.keyID]; ks != nil && ks.streams > 0 {
			ks.streams--
		}
	}
}

func (r *registry) get(source string) *session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions[source]
}

func (r *registry) list(now time.Time) []sourceInfo {
	r.mu.RLock()
	out := make([]sourceInfo, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s.info(now))
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// ringID is the keyring id of a session's key, "" for env keys and unidentified requests.
func ringID(info core.KeyInfo) string {
	if info.FromKeyring {
		return info.ID
	}
	return ""
}

// applyLimits makes a changed keyring limit effective for a key that already has state (its open
// streams share that limiter); tokens are kept, not refilled.
func (r *registry) applyLimits(key keyID, lim core.KeyLimits, now time.Time) {
	kl := r.lim.keyLimits(core.KeyInfo{Limits: lim}, true)
	r.mu.Lock()
	defer r.mu.Unlock()
	if ks := r.keys[key]; ks != nil && ks.cur != kl {
		ks.lim.reconfigure(kl, now)
		ks.cur = kl
	}
}
