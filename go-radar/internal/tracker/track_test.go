package tracker

import (
	"testing"
	"time"

	"nearbyradar/internal/parser"
)

func TestExactFixGoesToHistoryTrackWithLogTime(t *testing.T) {
	tr, fc, h := newT(t, Config{})
	fc.Advance(3 * time.Hour) // wall clock is irrelevant: the log time is used
	at := t0.Add(7 * time.Second)
	tr.Apply("a", evAt(at, "SPELL_CAST_SUCCESS", mkUnit(otherGUID, "Ola-PvP-US", fFriend), parser.Unit{}, info(otherGUID, 1, 1, 7.04, 8, 1453)))
	r, ok := h.Track(otherGUID, 0)
	if !ok || len(r.Fixes) != 1 {
		t.Fatalf("%+v", r)
	}
	f := r.Fixes[0]
	if f.T != at.UnixMilli() || f.MapID != 1453 || f.X != 7 || f.Y != 8 || f.Kind != "exact" {
		t.Fatalf("%+v", f)
	}
	if r.Reaction != "friendly" {
		t.Fatalf("reaction %q", r.Reaction)
	}
	if m := h.Search("ola", 5); len(m) != 1 || m[0].Raw != "Ola-PvP-US" || m[0].Fixes != 1 {
		t.Fatalf("%+v", m)
	}
}

func TestApproxSightingGoesToHistoryTrack(t *testing.T) {
	tr, _, h := newT(t, Config{})
	tr.Apply("a", evAt(t0, "SPELL_CAST_SUCCESS", mkUnit(meGUID, "Me-X", fMe), parser.Unit{}, info(meGUID, 9, 9, 100, 200, 1453)))
	at := t0.Add(10 * time.Second)
	tr.Apply("a", evAt(at, "SPELL_AURA_APPLIED", mkUnit(otherGUID, "O-X", fFriend), mkUnit(foeGUID, "F-X", fHostPl), nil))
	for _, g := range []string{otherGUID, foeGUID} {
		r, _ := h.Track(g, 0)
		if len(r.Fixes) != 1 || r.Fixes[0].Kind != "approx" || r.Fixes[0].X != 100 || r.Fixes[0].Y != 200 || r.Fixes[0].MapID != 1453 || r.Fixes[0].T != at.UnixMilli() {
			t.Fatalf("%s: %+v", g, r.Fixes)
		}
		if e, _ := h.Get(g); e.HasPos {
			t.Fatalf("approx sighting must not become the last exact position: %+v", e)
		}
	}
	if r, _ := h.Track(meGUID, 0); len(r.Fixes) != 1 || r.Fixes[0].Kind != "exact" {
		t.Fatalf("me: %+v", r.Fixes)
	}
}

func TestNoApproxTrackWhenDisabled(t *testing.T) {
	tr, _, h := newT(t, Config{DisableApprox: true})
	tr.Apply("a", evAt(t0, "SPELL_CAST_SUCCESS", mkUnit(meGUID, "Me-X", fMe), parser.Unit{}, info(meGUID, 9, 9, 100, 200, 1453)))
	tr.Apply("a", evAt(t0.Add(time.Second), "SPELL_AURA_APPLIED", mkUnit(otherGUID, "O-X", fFriend), parser.Unit{}, nil))
	if r, _ := h.Track(otherGUID, 0); len(r.Fixes) != 0 {
		t.Fatalf("%+v", r.Fixes)
	}
}

func TestRealmSuffixAccessor(t *testing.T) {
	if got := New(Config{RealmSuffix: "-PvP-US"}, nil).RealmSuffix(); got != "-PvP-US" {
		t.Fatalf("%q", got)
	}
	if got := New(Config{}, nil).RealmSuffix(); got != "" {
		t.Fatalf("%q", got)
	}
}
