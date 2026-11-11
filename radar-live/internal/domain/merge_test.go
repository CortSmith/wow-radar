package domain

import (
	"reflect"
	"strings"
	"testing"
)

func incoming(name string, lastSeen int64) PingContact {
	return PingContact{Contact: Contact{
		GUID: "Player-1", Name: name, Raw: name + "-Realm", Reaction: ReactionHostile,
		FirstSeen: lastSeen, LastSeen: lastSeen,
	}}
}

func withDeltas(p PingContact, d Deltas) PingContact { p.D = d; return p }

func TestMergeNewContact(t *testing.T) {
	in := incoming("Alpha", 1000)
	in.Level, in.MaxHP = 60, 4000.04
	in.X, in.Y, in.MapID, in.Zone, in.PosTime = 10.04, 20, 3, "Valley", 900
	in.D = Deltas{Events: 2, DmgTotal: 5.25, KB: 1}
	got := MergeContact(nil, in)
	if got.Name != "Alpha" || got.Level != 60 || got.MaxHP != 4000 || got.Events != 2 || got.KB != 1 || got.DmgTotal != 5.3 {
		t.Fatalf("unexpected merge result %+v", got)
	}
	if got.X != 10 || got.MapID != 3 || got.Zone != "Valley" || got.PosTime != 900 || got.FirstSeen != 1000 {
		t.Fatalf("position/times wrong %+v", got)
	}
}

func TestMergeFirstSeenDefaultsToLastSeen(t *testing.T) {
	in := incoming("Alpha", 1000)
	in.FirstSeen = 0
	if got := MergeContact(nil, in); got.FirstSeen != 1000 {
		t.Fatalf("FirstSeen = %d", got.FirstSeen)
	}
}

func TestMergeCountersAddAndExtremesHold(t *testing.T) {
	stored := MergeContact(nil, withDeltas(incoming("Alpha", 2000), Deltas{Events: 5, HPSum: 1.1, HPSamples: 1}))
	stored.Level, stored.MaxHP = 70, 9000
	in := withDeltas(incoming("Alpha", 3000), Deltas{Events: 3, HPSum: 2.2, HPSamples: 2})
	in.FirstSeen, in.Level, in.MaxHP = 500, 60, 8000
	got := MergeContact(&stored, in)
	if got.Events != 8 || got.HPSum != 3.3 || got.HPSamples != 3 {
		t.Fatalf("counters %+v", got)
	}
	if got.FirstSeen != 500 || got.LastSeen != 3000 || got.Level != 70 || got.MaxHP != 9000 {
		t.Fatalf("extremes %+v", got)
	}
}

func TestMergeIdentityNewestWins(t *testing.T) {
	stored := MergeContact(nil, incoming("Alpha", 2000))
	renamed := MergeContact(&stored, incoming("Beta", 3000))
	if renamed.Name != "Beta" || !reflect.DeepEqual(renamed.PastNames, []string{"Alpha"}) {
		t.Fatalf("newer name should win: %+v", renamed)
	}
	late := MergeContact(&stored, incoming("Gamma", 1000))
	if late.Name != "Alpha" || !reflect.DeepEqual(late.PastNames, []string{"Gamma"}) || late.LastSeen != 2000 {
		t.Fatalf("older sighting must not win: %+v", late)
	}
}

func TestMergeUnknownReactionNeverOverwrites(t *testing.T) {
	stored := MergeContact(nil, incoming("Alpha", 2000))
	in := incoming("Alpha", 3000)
	in.Reaction = ReactionUnknown
	if got := MergeContact(&stored, in); got.Reaction != ReactionHostile {
		t.Fatalf("reaction = %q", got.Reaction)
	}
	in.Reaction = ReactionFriendly
	if got := MergeContact(&stored, in); got.Reaction != ReactionFriendly {
		t.Fatalf("reaction = %q", got.Reaction)
	}
}

func TestMergePastNamesDedupeAndCap(t *testing.T) {
	in := incoming("Current", 1000)
	for i := 0; i < 15; i++ {
		in.PastNames = append(in.PastNames, "Old"+strings.Repeat("x", i), "Current", "")
	}
	got := MergeContact(nil, in)
	if len(got.PastNames) != PastNamesKeptMax {
		t.Fatalf("len(PastNames) = %d", len(got.PastNames))
	}
	for _, n := range got.PastNames {
		if n == "Current" || n == "" {
			t.Fatalf("bad past name %q", n)
		}
	}
	if got.PastNames[len(got.PastNames)-1] != "Oldxxxxxxxxxxxxxx" {
		t.Fatalf("should keep the most recent names, got %v", got.PastNames)
	}
}

func TestMergePositionOnlyMovesForward(t *testing.T) {
	first := incoming("Alpha", 2000)
	first.X, first.Y, first.MapID, first.Zone, first.PosTime = 1, 1, 1, "A", 2000
	stored := MergeContact(nil, first)

	old := incoming("Alpha", 2500)
	old.X, old.Y, old.MapID, old.Zone, old.PosTime = 9, 9, 2, "B", 1500
	if got := MergeContact(&stored, old); got.MapID != 1 || got.PosTime != 2000 {
		t.Fatalf("stale position applied: %+v", got)
	}
	newer := incoming("Alpha", 3000)
	newer.X, newer.Y, newer.MapID, newer.PosTime = 5, 5, 2, 2800
	got := MergeContact(&stored, newer)
	if got.MapID != 2 || got.PosTime != 2800 || got.Zone != "A" {
		t.Fatalf("newer position should apply and keep the old zone name when none sent: %+v", got)
	}
	noPos := MergeContact(&stored, incoming("Alpha", 4000))
	if noPos.MapID != 1 || noPos.PosTime != 2000 {
		t.Fatalf("contact without position must keep the stored one: %+v", noPos)
	}
}

func TestMergeDoesNotMutateInputs(t *testing.T) {
	stored := MergeContact(nil, incoming("Alpha", 2000))
	snapshot := stored.Clone()
	MergeContact(&stored, incoming("Beta", 3000))
	if !stored.Equal(snapshot) {
		t.Fatal("stored contact was mutated")
	}
}

func TestMergeTruncatesLongNames(t *testing.T) {
	in := incoming(strings.Repeat("é", NameMaxChars+10), 1000)
	if got := MergeContact(nil, in); len([]rune(got.Name)) != NameMaxChars {
		t.Fatalf("name length %d", len([]rune(got.Name)))
	}
}
