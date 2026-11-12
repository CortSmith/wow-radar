package domain

import "testing"

func TestKindOf(t *testing.T) {
	cases := map[string]GUIDKind{
		"Player-1-ABC": KindPlayer, "Creature-0-1": KindNPC, "Vehicle-0-1": KindNPC,
		"Pet-0-1": KindPet, "": KindOther, "Item-1": KindOther,
	}
	for guid, want := range cases {
		if got := KindOf(guid); got != want {
			t.Errorf("KindOf(%q)=%v want %v", guid, got, want)
		}
	}
}

func TestReactionFromFlags(t *testing.T) {
	cases := []struct {
		flags uint32
		want  Reaction
	}{
		{0x511, ReactionFriendly}, {0x10, ReactionFriendly}, {0x1, ReactionFriendly},
		{0x548, ReactionHostile}, {0x40, ReactionHostile}, {0x20, ReactionNeutral},
		{0x0, ReactionUnknown}, {0x400, ReactionUnknown},
	}
	for _, c := range cases {
		if got := ReactionFromFlags(c.flags); got != c.want {
			t.Errorf("flags %#x: got %q want %q", c.flags, got, c.want)
		}
	}
}

func TestEventUnitHelpers(t *testing.T) {
	me := EventUnit{GUID: "Player-1-A", Flags: 0x511}
	if !me.IsMine() || me.Kind() != KindPlayer || me.Reaction() != ReactionFriendly {
		t.Fatalf("me helpers wrong: %+v", me)
	}
	npc := EventUnit{GUID: "Creature-0-1", Flags: 0x1}
	if npc.IsMine() {
		t.Fatal("only players can be me")
	}
	if (EventUnit{}).IsPlayer() {
		t.Fatal("empty unit is not a player")
	}
}

func TestNamerDisplayWithConfiguredSuffix(t *testing.T) {
	n := NewNamer("PvP-US")
	if n.Suffix() != "-PvP-US" {
		t.Fatalf("suffix=%q", n.Suffix())
	}
	if got := n.Display("Aaa-Bbb-PvP-US"); got != "Aaa Bbb" {
		t.Fatalf("display=%q", got)
	}
	n.Observe("X-Other-Realm") // locked: ignored
	if n.Suffix() != "-PvP-US" {
		t.Fatal("locked suffix changed")
	}
}

func TestNamerDetectsSuffix(t *testing.T) {
	n := NewNamer("")
	for _, raw := range []string{"A-PvP-US", "B-PvP-US", "C-PvP-US", "D-PvP-US", "E-PvP-US", "E-PvP-US"} {
		n.Observe(raw)
	}
	if n.Suffix() != "" {
		t.Fatalf("too early: %q", n.Suffix())
	}
	n.Observe("")
	n.Observe("F-PvP-US")
	if n.Suffix() != "-PvP-US" {
		t.Fatalf("suffix=%q", n.Suffix())
	}
	if got := n.Display("Zed-PvP-US"); got != "Zed" {
		t.Fatalf("display=%q", got)
	}
}

func TestNamerNoCommonSuffix(t *testing.T) {
	n := NewNamer("")
	for _, raw := range []string{"A-X-US", "B-Y-US", "C-Z-US", "D-Q-US", "E-R-US", "F"} {
		n.Observe(raw)
	}
	if n.Suffix() != "" {
		t.Fatalf("suffix=%q", n.Suffix())
	}
	if got := n.Display("Aa-Bb"); got != "Aa Bb" {
		t.Fatalf("display=%q", got)
	}
}

func TestNamerSharedTwoTokenSuffixKeepsOneNameToken(t *testing.T) {
	n := NewNamer("")
	for _, raw := range []string{"A-US", "B-US", "C-US", "D-US", "E-US", "F-US"} {
		n.Observe(raw)
	}
	if n.Suffix() != "-US" {
		t.Fatalf("suffix=%q", n.Suffix())
	}
}

func TestNamerDetectsMajoritySuffixDespiteMinority(t *testing.T) {
	n := NewNamer("")
	for _, raw := range []string{"A-PvP-US", "Zed-PvE-EU", "B-PvP-US", "C-PvP-US", "D-PvP-US", "E-PvP-US", "Mary-Ann"} {
		n.Observe(raw)
	}
	if n.Suffix() != "" {
		t.Fatalf("too early: %q", n.Suffix())
	}
	n.Observe("F-PvP-US")
	if n.Suffix() != "-PvP-US" {
		t.Fatalf("suffix=%q", n.Suffix())
	}
	if got := n.Display("Zed-PvE-EU"); got != "Zed PvE EU" {
		t.Fatalf("minority display=%q", got)
	}
}
