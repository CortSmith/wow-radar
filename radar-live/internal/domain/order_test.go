package domain

import (
	"errors"
	"testing"
)

func TestRulesetValidate(t *testing.T) {
	if err := RulesetPvP.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := Ruleset("x").Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestSortNewestFirst(t *testing.T) {
	cs := []Contact{
		{GUID: "Player-b", LastSeen: 5},
		{GUID: "Player-c", LastSeen: 9},
		{GUID: "Player-a", LastSeen: 5},
	}
	SortNewestFirst(cs)
	want := []string{"Player-c", "Player-a", "Player-b"}
	for i, c := range cs {
		if c.GUID != want[i] {
			t.Fatalf("order %v", cs)
		}
	}
}

func TestAppendFixesWithNothingToAdd(t *testing.T) {
	trail := []Fix{fix(1000, 1, 0, 0)}
	got := AppendFixes(trail, nil)
	if len(got) != 1 || got[0].T != 1000 {
		t.Fatalf("got %v", got)
	}
}
