// Package dataset keeps one history store and one bounty ledger per server rule-set
// (normal, pvp, rp, rppvp) and routes every read and write to the ACTIVE rule-set.
//
// The Registry implements everything the rest of the program needs from "the history" and "the
// bounty ledger" (tracker.Recorder, bounty.Sink, server.HistorySource, server.BountySource), so
// callers are unaware of rule-sets: switching the active rule-set is one atomic pointer swap.
//
// Active rule-set: manual mode uses the user's choice; auto mode uses the rule-set detected from
// the log owner's own realm token (ObserveMe), else the last manual choice persisted in
// radar-settings.json, else ruleset.Default.
//
// Files (next to the history path "<dir>/<stem><ext>", default radar-history.json):
//
//	<stem>-<id><ext>           history of one rule-set      (radar-history-pvp.json)
//	<stem>-<id>-bounty<ext>    bounty ledger of one rule-set (radar-history-pvp-bounty.json)
//	radar-settings.json        {mode, ruleset}
//	<stem>-migrated<ext>       marker: legacy data was already copied once
package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nearbyradar/internal/bounty"
	"nearbyradar/internal/history"
	"nearbyradar/internal/ruleset"
)

// Selection modes.
const (
	ModeAuto   = "auto"
	ModeManual = "manual"
)

// SettingsFile is the settings file name, next to the history file.
const SettingsFile = "radar-settings.json"

// ErrUnknownRuleset is returned by Select for an unknown id.
var ErrUnknownRuleset = errors.New("unknown ruleset (use auto, normal, pvp, rp or rppvp)")

// Options configure a Registry.
type Options struct {
	HistoryPath string // legacy/base history path; "" = memory only (nothing is persisted)
	// Initial is the --ruleset flag: "" or "auto" keeps the persisted selection, anything else
	// is applied as a manual choice (and persisted).
	Initial string
	Logf    func(string, ...any)
}

// Brief is the cheap part of the status (also embedded in /api/state).
type Brief struct {
	Active   ruleset.ID  `json:"active"`
	Mode     string      `json:"mode"`
	Detected *ruleset.ID `json:"detected"`
}

// RulesetStatus describes one rule-set's dataset.
type RulesetStatus struct {
	ID      ruleset.ID `json:"id"`
	Name    string     `json:"name"`
	HasData bool       `json:"hasData"`
	Players int        `json:"players"`
}

// Status is the payload of GET /api/ruleset.
type Status struct {
	Brief
	Rulesets []RulesetStatus `json:"rulesets"`
}

type set struct {
	id ruleset.ID
	h  *history.Store
	b  *bounty.Store
}

type settings struct {
	Mode    string `json:"mode"`
	Ruleset string `json:"ruleset,omitempty"` // last manual choice
}

// Registry owns the per-rule-set stores. It is safe for concurrent use.
type Registry struct {
	opt            Options
	dir, stem, ext string
	settingsPath   string
	cur            atomic.Pointer[set] // hot path: read by every Record / API call

	mu       sync.RWMutex // guards everything below
	sets     map[ruleset.ID]*set
	mode     string
	manual   ruleset.ID // last manual choice ("" = never chosen)
	detected ruleset.ID // ruleset of the log owner's realm ("" = unknown)
	hint     ruleset.ID // weaker: ruleset of the realm suffix shared by the players seen (used while "me" is unknown)
	active   ruleset.ID
}

// New builds a Registry and opens (and, once, migrates legacy data into) the active rule-set.
func New(opt Options) (*Registry, error) {
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	r := &Registry{opt: opt, sets: map[ruleset.ID]*set{}, mode: ModeAuto}
	if p := opt.HistoryPath; p != "" {
		r.dir = filepath.Dir(p)
		r.ext = filepath.Ext(p)
		if r.ext == "" {
			r.ext = ".json"
		}
		r.stem = strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		r.settingsPath = filepath.Join(r.dir, SettingsFile)
		r.loadSettings()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if v := strings.TrimSpace(opt.Initial); v != "" && !strings.EqualFold(v, ModeAuto) {
		id, ok := ruleset.Parse(v)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownRuleset, opt.Initial)
		}
		r.mode, r.manual = ModeManual, id
		r.saveSettings()
	}
	r.activate()
	return r, nil
}

func (r *Registry) loadSettings() {
	b, err := os.ReadFile(r.settingsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			r.opt.Logf("settings: %v (using defaults)", err)
		}
		return
	}
	var s settings
	if err := json.Unmarshal(b, &s); err != nil {
		r.opt.Logf("settings: %s is corrupt (%v); using defaults", SettingsFile, err)
		return
	}
	if id, ok := ruleset.Parse(s.Ruleset); ok {
		r.manual = id
		if s.Mode == ModeManual {
			r.mode = ModeManual
		}
	}
}

// saveSettings writes the settings atomically. Caller holds r.mu.
func (r *Registry) saveSettings() {
	if r.settingsPath == "" {
		return
	}
	b, _ := json.Marshal(settings{Mode: r.mode, Ruleset: string(r.manual)})
	if err := writeAtomic(r.settingsPath, b); err != nil {
		r.opt.Logf("settings: %v", err)
	}
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (r *Registry) pathFor(id ruleset.ID) string {
	if r.opt.HistoryPath == "" {
		return ""
	}
	return filepath.Join(r.dir, r.stem+"-"+string(id)+r.ext)
}

func exists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// resolve computes the active rule-set. Caller holds r.mu.
func (r *Registry) resolve() ruleset.ID {
	if r.mode == ModeManual && r.manual != "" {
		return r.manual
	}
	if d := r.detectedLocked(); d != "" {
		return d
	}
	if r.manual != "" {
		return r.manual
	}
	return ruleset.Default
}

// activate (re)computes the active rule-set and swaps the hot-path pointer. Caller holds r.mu
// for writing. Stores of the previous rule-set stay open and are flushed with the rest, so a
// write racing with the switch is never lost.
func (r *Registry) activate() {
	id := r.resolve()
	r.active = id
	r.cur.Store(r.openLocked(id))
}

// openLocked returns the stores of id, opening them (lazily, once) from disk. Caller holds r.mu.
func (r *Registry) openLocked(id ruleset.ID) *set {
	if s := r.sets[id]; s != nil {
		return s
	}
	hp := r.pathFor(id)
	s := &set{id: id, h: history.New(""), b: bounty.New("")}
	if hp != "" {
		var err error
		r.migrateLegacy(id)
		if s.h, err = history.Open(hp); err != nil {
			r.opt.Logf("history: %v (continuing with an empty history)", err)
		}
		if s.b, err = bounty.Open(bounty.PathFor(hp)); err != nil {
			r.opt.Logf("bounty: %v (continuing with an empty ledger)", err)
		}
	}
	r.sets[id] = s
	return s
}

// migrateLegacy copies (never moves) the pre-ruleset files into the dataset of id, once: only when
// legacy files exist, no rule-set file exists yet and no marker was written.
func (r *Registry) migrateLegacy(id ruleset.ID) {
	if r.opt.HistoryPath == "" {
		return
	}
	marker := filepath.Join(r.dir, r.stem+"-migrated"+r.ext)
	legacyH, legacyB := r.opt.HistoryPath, bounty.PathFor(r.opt.HistoryPath)
	if exists(marker) || (!exists(legacyH) && !exists(legacyB)) {
		return
	}
	for _, info := range ruleset.All() {
		p := r.pathFor(info.ID)
		if exists(p) || exists(bounty.PathFor(p)) {
			return
		}
	}
	dst := r.pathFor(id)
	for _, c := range [][2]string{{legacyH, dst}, {legacyB, bounty.PathFor(dst)}} {
		if !exists(c[0]) {
			continue
		}
		b, err := os.ReadFile(c[0])
		if err == nil {
			err = writeAtomic(c[1], b)
		}
		if err != nil {
			r.opt.Logf("migration: %v (will retry next start)", err)
			return
		}
	}
	m, _ := json.Marshal(map[string]any{"ruleset": id, "time": time.Now().UTC().Format(time.RFC3339)})
	if err := writeAtomic(marker, m); err != nil {
		r.opt.Logf("migration: %v", err)
	}
	r.opt.Logf("migration: older data copied into the %s dataset (originals kept)", ruleset.NameOf(id))
}

// ObserveMe tells the registry the raw name of the log owner ("Name-PvP-US"). In auto mode the
// dataset follows the realm type found in it.
func (r *Registry) ObserveMe(raw string) {
	id, ok := ruleset.DetectFromRawName(raw)
	if !ok {
		return
	}
	r.mu.RLock()
	same := r.detected == id
	r.mu.RUnlock()
	if same {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detected = id
	r.activate()
}

// ObserveSuffix is the weaker sibling of ObserveMe: it takes the realm suffix shared by the players
// in the log ("-PvP-US", as detected by the tracker) and is used only while the log owner is still
// unknown, e.g. when a read-from-tail window does not contain any event naming them.
func (r *Registry) ObserveSuffix(suffix string) {
	id, ok := ruleset.DetectFromRawName("x-" + strings.TrimPrefix(suffix, "-"))
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hint == id {
		return
	}
	r.hint = id
	r.activate()
}

// Select applies a user choice: "auto" or a ruleset id. The choice is persisted.
func (r *Registry) Select(sel string) (Status, error) {
	r.mu.Lock()
	if strings.EqualFold(strings.TrimSpace(sel), ModeAuto) {
		r.mode = ModeAuto
	} else if id, ok := ruleset.Parse(sel); ok {
		r.mode, r.manual = ModeManual, id
	} else {
		r.mu.Unlock()
		return Status{}, fmt.Errorf("%w: %q", ErrUnknownRuleset, sel)
	}
	r.saveSettings()
	r.activate()
	r.mu.Unlock()
	return r.Status(), nil
}

func (r *Registry) briefLocked() Brief {
	b := Brief{Active: r.active, Mode: r.mode}
	if d := r.detectedLocked(); d != "" {
		b.Detected = &d
	}
	return b
}

// Brief is the cheap summary (no file access).
func (r *Registry) Brief() Brief {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.briefLocked()
}

// Active is the rule-set currently read and written.
func (r *Registry) Active() ruleset.ID { return r.cur.Load().id }

// Mode is "auto" or "manual".
func (r *Registry) Mode() string { return r.Brief().Mode }

// Status lists every rule-set with its data counts. Rule-sets whose files exist but are not
// open yet are loaded (once) so the counts are exact; rule-sets without files are not created.
func (r *Registry) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := Status{Brief: r.briefLocked()}
	for _, info := range ruleset.All() {
		s := r.sets[info.ID]
		if s == nil && (exists(r.pathFor(info.ID)) || exists(bounty.PathFor(r.pathFor(info.ID)))) {
			s = r.openLocked(info.ID)
		}
		e := RulesetStatus{ID: info.ID, Name: info.Name}
		if s != nil {
			e.Players = s.h.Len()
			bs := s.b.Stats()
			e.HasData = e.Players > 0 || bs.Players > 0 || bs.Deaths > 0
		}
		st.Rulesets = append(st.Rulesets, e)
	}
	return st
}

// --- history side (tracker.Recorder, server.HistorySource) ---

// Record writes a sample into the active rule-set's history.
func (r *Registry) Record(sm history.Sample) { r.cur.Load().h.Record(sm) }

// Snapshot returns the active rule-set's history entries.
func (r *Registry) Snapshot() []history.Entry { return r.cur.Load().h.Snapshot() }

// Search searches the active rule-set's players.
func (r *Registry) Search(q string, limit int) []history.Match {
	return r.cur.Load().h.Search(q, limit)
}

// Track returns a player's track from the active rule-set.
func (r *Registry) Track(guid string, mapID int) (history.TrackResult, bool) {
	return r.cur.Load().h.Track(guid, mapID)
}

// --- bounty side (bounty.Sink, server.BountySource) ---

// RecordDeath credits a death in the active rule-set's ledger.
func (r *Registry) RecordDeath(d bounty.Death) bool { return r.cur.Load().b.RecordDeath(d) }

// Leaderboard reads the active rule-set's ledger.
func (r *Registry) Leaderboard(limit int) []bounty.Row { return r.cur.Load().b.Leaderboard(limit) }

// Player reads one hostile player from the active rule-set's ledger.
func (r *Registry) Player(guid string) (bounty.Detail, bool) { return r.cur.Load().b.Player(guid) }

// Stats returns the active rule-set's ledger counters.
func (r *Registry) Stats() bounty.Stats { return r.cur.Load().b.Stats() }

// --- persistence ---

// Flush saves every open dataset that changed (the active one and any previously active ones).
func (r *Registry) Flush() error {
	r.mu.RLock()
	all := make([]*set, 0, len(r.sets))
	for _, s := range r.sets {
		all = append(all, s)
	}
	r.mu.RUnlock()
	var first error
	for _, s := range all {
		if err := s.h.Flush(); err != nil && first == nil {
			first = err
		}
		if err := s.b.Flush(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Run flushes every interval until ctx is cancelled, then once more. It is the only goroutine
// the registry needs; it returns when ctx ends.
func (r *Registry) Run(ctx context.Context, interval time.Duration) {
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = r.Flush()
			return
		case <-tk.C:
			if err := r.Flush(); err != nil {
				r.opt.Logf("save: %v", err)
			}
		}
	}
}

// detectedLocked is the detected rule-set: the log owner's realm if known, else the shared realm hint.
func (r *Registry) detectedLocked() ruleset.ID {
	if r.detected != "" {
		return r.detected
	}
	return r.hint
}
