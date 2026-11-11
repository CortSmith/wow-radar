package domain

import "testing"

func TestParseReaction(t *testing.T) {
	tests := map[string]Reaction{
		"friendly": ReactionFriendly, "HOSTILE": ReactionHostile, "neutral": ReactionNeutral,
		"unknown": ReactionUnknown, "": ReactionUnknown, "bogus": ReactionUnknown,
	}
	for in, want := range tests {
		if got := ParseReaction(in); got != want {
			t.Errorf("ParseReaction(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGroupOf(t *testing.T) {
	tests := map[Reaction]Group{
		ReactionFriendly: GroupFriendly, ReactionHostile: GroupHostile,
		ReactionNeutral: GroupOther, ReactionUnknown: GroupOther,
	}
	for r, want := range tests {
		if got := GroupOf(r); got != want {
			t.Errorf("GroupOf(%q) = %q, want %q", r, got, want)
		}
	}
}

func TestGroupValidAndContains(t *testing.T) {
	for _, g := range []Group{"", GroupAny, GroupFriendly, GroupHostile, GroupOther} {
		if !g.Valid() {
			t.Errorf("group %q should be valid", g)
		}
	}
	if Group("red").Valid() {
		t.Error("group red should be invalid")
	}
	if !GroupAny.Contains(ReactionHostile) || !Group("").Contains(ReactionFriendly) {
		t.Error("any/empty group must contain everything")
	}
	if GroupFriendly.Contains(ReactionHostile) || !GroupOther.Contains(ReactionNeutral) {
		t.Error("group containment wrong")
	}
}
