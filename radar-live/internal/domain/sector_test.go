package domain

import (
	"errors"
	"testing"
	"time"
)

func intp(v int) *int { return &v }

func TestSectorValidate(t *testing.T) {
	tests := []struct {
		name string
		s    Sector
		ok   bool
	}{
		{"all", Sector{RS: RulesetPvP, Scope: ScopeAll}, true},
		{"zone map zero is valid", Sector{RS: RulesetPvP, Scope: ScopeZone, MapID: intp(0)}, true},
		{"zone needs map", Sector{RS: RulesetPvP, Scope: ScopeZone}, false},
		{"player needs guid", Sector{RS: RulesetPvP, Scope: ScopePlayer}, false},
		{"player ok", Sector{RS: RulesetPvP, Scope: ScopePlayer, GUID: "Player-1"}, true},
		{"bad scope", Sector{RS: RulesetPvP, Scope: "galaxy"}, false},
		{"bad ruleset", Sector{RS: "x", Scope: ScopeAll}, false},
		{"bad group", Sector{RS: RulesetPvP, Scope: ScopeAll, Group: "red"}, false},
		{"negative since", Sector{RS: RulesetPvP, Scope: ScopeAll, Since: -1}, false},
		{"negative window", Sector{RS: RulesetPvP, Scope: ScopeAll, ActiveWithinMs: -1}, false},
	}
	for _, tt := range tests {
		err := tt.s.Validate()
		if (err == nil) != tt.ok {
			t.Errorf("%s: Validate() = %v", tt.name, err)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: not ErrInvalid", tt.name)
		}
	}
}

func TestSectorActiveWindow(t *testing.T) {
	if (Sector{}).ActiveWindow() != DefaultActiveWindow {
		t.Fatal("default window")
	}
	if (Sector{ActiveWithinMs: 1000}).ActiveWindow() != time.Second {
		t.Fatal("override window")
	}
}

func TestSectorIncludes(t *testing.T) {
	now := time.UnixMilli(10_000_000)
	fresh := Contact{GUID: "Player-1", Name: "Alpha", Reaction: ReactionHostile, MapID: 5, PosTime: 1, LastSeen: now.UnixMilli() - 1000}
	old := fresh
	old.GUID, old.LastSeen = "Player-2", now.UnixMilli()-DefaultActiveWindow.Milliseconds()-1
	noPos := fresh
	noPos.GUID, noPos.PosTime = "Player-3", 0
	tests := []struct {
		name string
		s    Sector
		c    Contact
		want bool
	}{
		{"all includes fresh", Sector{Scope: ScopeAll}, fresh, true},
		{"all drops inactive", Sector{Scope: ScopeAll}, old, false},
		{"player ignores active window", Sector{Scope: ScopePlayer, GUID: "Player-2"}, old, true},
		{"player other guid", Sector{Scope: ScopePlayer, GUID: "Player-9"}, fresh, false},
		{"zone match", Sector{Scope: ScopeZone, MapID: intp(5)}, fresh, true},
		{"zone other map", Sector{Scope: ScopeZone, MapID: intp(6)}, fresh, false},
		{"zone needs position", Sector{Scope: ScopeZone, MapID: intp(5)}, noPos, false},
		{"group filter", Sector{Scope: ScopeAll, Group: GroupFriendly}, fresh, false},
		{"query filter", Sector{Scope: ScopeAll, Query: "alp"}, fresh, true},
		{"query miss", Sector{Scope: ScopeAll, Query: "zzz"}, fresh, false},
		{"since excludes unchanged", Sector{Scope: ScopeAll, Since: fresh.LastSeen}, fresh, false},
		{"since includes newer", Sector{Scope: ScopeAll, Since: fresh.LastSeen - 1}, fresh, true},
		{"window override widens", Sector{Scope: ScopeAll, ActiveWithinMs: 3_600_000}, old, true},
	}
	for _, tt := range tests {
		if got := tt.s.Includes(tt.c, now); got != tt.want {
			t.Errorf("%s: Includes = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSearchQuery(t *testing.T) {
	good := SearchQuery{RS: RulesetPvP, Group: GroupHostile, Limit: 10, Offset: 0}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []SearchQuery{
		{RS: "x"}, {RS: RulesetPvP, Group: "red"}, {RS: RulesetPvP, Limit: -1}, {RS: RulesetPvP, Offset: -1},
	} {
		if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v should be invalid, got %v", bad, err)
		}
	}
	c := Contact{Name: "Alpha", Reaction: ReactionHostile, MapID: 5, PosTime: 1}
	if !(SearchQuery{Q: "alp", MapID: intp(5), Group: GroupHostile}).Includes(c) {
		t.Error("should include")
	}
	if (SearchQuery{MapID: intp(6)}).Includes(c) || (SearchQuery{Group: GroupFriendly}).Includes(c) || (SearchQuery{Q: "zz"}).Includes(c) {
		t.Error("should exclude")
	}
	if (SearchQuery{MapID: intp(5)}).Includes(Contact{MapID: 5}) {
		t.Error("map filter needs a known position")
	}
}

func TestClampLimit(t *testing.T) {
	tests := []struct{ in, def, max, want int }{{0, 50, 500, 50}, {-3, 50, 500, 50}, {10, 50, 500, 10}, {9999, 50, 500, 500}}
	for _, tt := range tests {
		if got := ClampLimit(tt.in, tt.def, tt.max); got != tt.want {
			t.Errorf("ClampLimit(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
