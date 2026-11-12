package bountyx

import (
	"crypto/sha1"
	"encoding/hex"
	"strconv"

	"radarlive/internal/domain"
)

// Attribution windows and memory bounds.
const (
	// AssistWindowMs: a hostile player that damaged the victim up to this long before the death assists
	// (or lands the killing blow).
	AssistWindowMs = 15_000
	// KillingBlowWindowMs: without a PARTY_KILL the killer is the source of the last damage, but only
	// when that hit happened at most this long before the death.
	KillingBlowWindowMs = 5_000
	// DuplicateWindowMs: UNIT_DIED, PARTY_KILL and SPELL_INSTAKILL for one victim within this long are
	// one death; the first signal seen wins.
	DuplicateWindowMs = 3_000

	maxHitsPerVictim  = 64
	maxTrackedVictims = 256
	maxKnownPlayers   = 4096
	maxOwners         = 4096
	maxLastDeaths     = 512
	// maxCredits bounds the distinct players credited between two flushes.
	maxCredits   = 4096
	deathIDBytes = 10
)

// Options configure an Analyzer.
type Options struct {
	// RealmSuffix is stripped from names for display ("" = detect from the stream).
	RealmSuffix string
}

// Stats are the counters of one Analyzer.
type Stats struct {
	Deaths     int // friendly player deaths recorded (feigns and duplicate signals excluded)
	NPCDeaths  int // of those, deaths without any hostile player credit
	Duplicates int // death signals dropped by the duplicate window
	Feigns     int // UNIT_DIED events flagged unconscious (feign death), ignored
	// DroppedCredits counts credits not booked because a flush already held maxCredits players.
	DroppedCredits int
}

// Credit is the bounty won by one hostile player since the last Flush.
type Credit struct {
	Raw     string // raw log name
	Name    string // display name
	KB      int64  // killing blows
	Assists int64
	LastMs  int64 // time of the newest death credited
}

// Part is what one Flush yields. Credits are additive deltas keyed by player GUID.
type Part struct {
	Ruleset domain.Ruleset
	Deaths  []domain.Death
	Credits map[string]Credit
}

type hit struct {
	tMs    int64
	credit string // hostile player credited (owner for pets); "" for NPCs and the environment
	friend bool   // damage from a friendly player (duel, mind control, own area damage)
}

type window struct {
	hits    []hit
	touched int64
}

type known struct {
	raw      string
	hostile  bool
	friendly bool
}

// Analyzer turns the events of ONE log source into bounty credits. Events must arrive in log order.
// It is not safe for concurrent use.
type Analyzer struct {
	namer   *domain.Namer
	wins    map[string]*window
	known   map[string]known
	owners  map[string]string // pet, totem or guardian GUID -> owner GUID
	last    map[string]int64  // victim GUID -> time of the last recorded death
	stats   Stats
	ruleset domain.Ruleset

	deaths  []domain.Death
	credits map[string]Credit
}

// NewAnalyzer creates an Analyzer.
func NewAnalyzer(o Options) *Analyzer {
	return &Analyzer{namer: domain.NewNamer(o.RealmSuffix), wins: map[string]*window{}, known: map[string]known{},
		owners: map[string]string{}, last: map[string]int64{}, credits: map[string]Credit{}}
}

// Stats returns the counters.
func (a *Analyzer) Stats() Stats { return a.stats }

// Feed consumes one event.
func (a *Analyzer) Feed(ev domain.Event) {
	if ev.Ruleset != "" {
		a.ruleset = ev.Ruleset
	}
	switch ev.Name {
	case "ZONE_CHANGE", "MAP_CHANGE", "COMBAT_LOG_VERSION":
		return
	}
	a.learn(ev)
	switch {
	case ev.Name == "UNIT_DIED":
		if ev.Unconscious {
			if isFriendlyPlayer(ev.Dst) {
				a.stats.Feigns++
			}
			return
		}
		a.death(ev, nil)
	case ev.Name == "PARTY_KILL" || ev.Name == "SPELL_INSTAKILL":
		killer := ev.Src
		a.death(ev, &killer)
	case ev.IsDamage:
		a.hit(ev)
	}
}

// Flush returns the deaths and credits since the previous Flush and resets them.
func (a *Analyzer) Flush() Part {
	part := Part{Ruleset: a.ruleset, Deaths: a.deaths, Credits: a.credits}
	a.deaths, a.credits = nil, map[string]Credit{}
	for g, c := range part.Credits {
		if c.Raw != "" { // the suffix may have been detected after the credit was booked
			c.Name = a.namer.Display(c.Raw)
			part.Credits[g] = c
		}
	}
	if len(part.Credits) == 0 {
		part.Credits = nil
	}
	return part
}

func isFriendlyPlayer(u domain.EventUnit) bool {
	return u.IsPlayer() && u.Reaction() == domain.ReactionFriendly
}

// learn remembers which players are hostile or friendly and who owns which pet or totem.
func (a *Analyzer) learn(ev domain.Event) {
	for _, u := range []domain.EventUnit{ev.Src, ev.Dst} {
		if !u.IsPlayer() || u.Flags == 0 {
			continue
		}
		a.namer.Observe(u.Raw)
		if len(a.known) >= maxKnownPlayers {
			dropHalf(a.known, maxKnownPlayers/2)
		}
		r := u.Reaction()
		a.known[u.GUID] = known{raw: u.Raw, hostile: r == domain.ReactionHostile, friendly: r == domain.ReactionFriendly}
	}
	if in := ev.Info; in != nil && in.Owner != "" && domain.KindOf(in.GUID) != domain.KindPlayer {
		a.setOwner(in.GUID, in.Owner)
	}
	if ev.Name == "SPELL_SUMMON" && ev.Src.IsPlayer() && ev.Dst.GUID != "" {
		a.setOwner(ev.Dst.GUID, ev.Src.GUID)
	}
}

func (a *Analyzer) setOwner(pet, owner string) {
	if len(a.owners) >= maxOwners {
		dropHalf(a.owners, maxOwners/2)
	}
	a.owners[pet] = owner
}

// dropHalf deletes arbitrary entries until at most keep remain.
func dropHalf[V any](m map[string]V, keep int) {
	for k := range m {
		if len(m) <= keep {
			return
		}
		delete(m, k)
	}
}

// creditOf resolves who earns credit for damage dealt by src: a hostile player, or the hostile player
// owning a pet, guardian or totem. friend is true when the source is a friendly player or owned by one.
func (a *Analyzer) creditOf(src domain.EventUnit) (guid string, friend bool) {
	switch src.Kind() {
	case domain.KindPlayer:
		if src.Reaction() == domain.ReactionHostile {
			return src.GUID, false
		}
		return "", src.Reaction() == domain.ReactionFriendly
	case domain.KindPet, domain.KindNPC:
		owner, ok := a.owners[src.GUID]
		if !ok {
			return "", false
		}
		if k, ok := a.known[owner]; ok {
			if k.hostile {
				return owner, false
			}
			return "", k.friendly
		}
	}
	return "", false
}

// hit records damage on a friendly player in that victim's bounded sliding window.
func (a *Analyzer) hit(ev domain.Event) {
	if !isFriendlyPlayer(ev.Dst) {
		return
	}
	h := hit{tMs: ev.TimeMs}
	h.credit, h.friend = a.creditOf(ev.Src)
	w := a.wins[ev.Dst.GUID]
	if w == nil {
		if len(a.wins) >= maxTrackedVictims {
			a.evictVictim()
		}
		w = &window{}
		a.wins[ev.Dst.GUID] = w
	}
	w.touched = ev.TimeMs
	kept := w.hits[:0]
	for _, o := range w.hits {
		if ev.TimeMs-o.tMs <= AssistWindowMs {
			kept = append(kept, o)
		}
	}
	w.hits = append(kept, h)
	if over := len(w.hits) - maxHitsPerVictim; over > 0 {
		w.hits = append(w.hits[:0], w.hits[over:]...)
	}
}

func (a *Analyzer) evictVictim() {
	oldest, oldT := "", int64(0)
	for g, w := range a.wins {
		if oldest == "" || w.touched < oldT {
			oldest, oldT = g, w.touched
		}
	}
	delete(a.wins, oldest)
}

// deathID is stable across runs: the first 10 bytes of sha1(victim|ms|source), hex encoded.
func deathID(victim string, tMs int64, source string) string {
	sum := sha1.Sum([]byte(victim + "|" + strconv.FormatInt(tMs, 10) + "|" + source))
	return hex.EncodeToString(sum[:deathIDBytes])
}
