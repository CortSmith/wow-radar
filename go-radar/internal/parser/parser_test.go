package parser

import (
	"math"
	"testing"
	"time"
)

const (
	swing = `11/10/2026 15:11:47.075-6  SWING_DAMAGE,Player-1-0000AAAA,"Alpha-Realm-PvP-US",0x518,0x80000000,Creature-0-6835-0-11-1397-0000F38640,"Boar",0xa28,0x80000000,Player-1-0000AAAA,0000000000000000,56,147,90,0,171,0,0,0,3,35,105,0,-5370.61,-224.12,1426,1.8470,12,6,11,-1,1,0,0,0,nil,1,nil`
	spell = `11/10/2026 15:11:43.678-6  SPELL_DAMAGE,Creature-0-6835-0-11-1397-0001738601,"Seer",0xa48,0x80000000,Player-1-0000AAAA,"Alpha-Realm-PvP-US",0x518,0x80000000,9532,"Lightning Bolt",0x8,Player-1-0000AAAA,0000000000000000,83,147,90,0,171,0,0,0,3,105,105,0,-5367.97,-236.31,1426,1.7056,0,32,32,-1,8,0,0,0,nil,nil,nil,ST`
	cast  = `11/10/2026 18:14:19.603-6  SPELL_CAST_SUCCESS,Player-1-0000BBBB,"Beta-Realm-PvP-US",0x511,0x80000000,0000000000000000,nil,0x80000000,0x80000000,2963,"Bolt",0x1,Player-1-0000BBBB,0000000000000000,67,67,20,0,47,0,0,0,0,184,184,0,-8812.84,652.15,1453,0.6938,0`
	start = `11/10/2026 18:14:19.604-6  SPELL_CAST_START,Player-1-0000CCCC,"Gamma-Realm-PvP-US",0x518,0x80000000,0000000000000000,nil,0x80000000,0x80000000,3839,"Bolt",0x1`
)

func TestParseLineTable(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		check func(t *testing.T, e Event)
	}{
		{"swing damage: adv present, amount after adv", swing, func(t *testing.T, e Event) {
			eq(t, e.Type, "SWING_DAMAGE")
			eq(t, e.Src.GUID, "Player-1-0000AAAA")
			eq(t, e.Src.Name, "Alpha-Realm-PvP-US")
			eq(t, e.Src.Flags, uint32(0x518))
			eq(t, e.Dst.GUID, "Creature-0-6835-0-11-1397-0000F38640")
			if e.Info == nil || !e.Info.HasPos {
				t.Fatal("no info")
			}
			eq(t, e.Info.GUID, "Player-1-0000AAAA")
			eq(t, e.Info.HP, 56.0)
			eq(t, e.Info.MaxHP, 147.0)
			eq(t, e.Info.X, -5370.61)
			eq(t, e.Info.Y, -224.12)
			eq(t, e.Info.MapID, 1426)
			eq(t, e.Info.Level, 12)
			if !e.HasAmount {
				t.Fatal("amount")
			}
			eq(t, e.Amount, 6.0)
			eq(t, e.IsDamage, true)
		}},
		{"spell damage with 3 prefix params", spell, func(t *testing.T, e Event) {
			eq(t, e.Info.GUID, "Player-1-0000AAAA")
			eq(t, e.Info.HP, 83.0)
			eq(t, e.Amount, 32.0)
			eq(t, e.IsDamage, true)
		}},
		{"cast success has adv but no amount", cast, func(t *testing.T, e Event) {
			eq(t, e.Info.GUID, "Player-1-0000BBBB")
			eq(t, e.Info.X, -8812.84)
			eq(t, e.HasAmount, false)
			eq(t, e.IsDamage, false)
			eq(t, e.Dst.GUID, "")
			eq(t, e.Dst.Name, "")
		}},
		{"cast start has no adv", start, func(t *testing.T, e Event) {
			if e.Info != nil {
				t.Fatal("unexpected info")
			}
			eq(t, e.Src.Name, "Gamma-Realm-PvP-US")
		}},
		{"utc conversion with offset", cast, func(t *testing.T, e Event) {
			want := time.Date(2026, 11, 11, 0, 14, 19, 603_000_000, time.UTC)
			if !e.Time.Equal(want) {
				t.Fatalf("time %v want %v", e.Time, want)
			}
		}},
		{"zone change", `11/10/2026 15:10:53.403-6  ZONE_CHANGE,0,"Dun Morogh",0`, func(t *testing.T, e Event) {
			eq(t, e.Type, "ZONE_CHANGE")
			eq(t, e.Zone, "Dun Morogh")
		}},
		{"map change normalises min/max", `11/10/2026 15:10:53.403-6  MAP_CHANGE,1426,"Dun Morogh",-3877.083252,-7160.416504,1802.083252,-3122.916504`, func(t *testing.T, e Event) {
			if e.Map == nil {
				t.Fatal("no map")
			}
			eq(t, e.Map.ID, 1426)
			eq(t, e.Map.Name, "Dun Morogh")
			// Field order is x-range first (a,b) then y-range (c,d); verified against the in-game
			// coordinate readout (see the Thousand Needles case below).
			eq(t, e.Map.X0, -7160.416504)
			eq(t, e.Map.X1, -3877.083252)
			eq(t, e.Map.Y0, -3122.916504)
			eq(t, e.Map.Y1, 1802.083252)
		}},
		{"map change x-range is the first pair (Thousand Needles)", `11/10/2026 16:00:51.964-6  MAP_CHANGE,1441,"Thousand Needles",-3966.666504,-6899.999512,-433.333313,-4833.333008`, func(t *testing.T, e Event) {
			if e.Map == nil {
				t.Fatal("no map")
			}
			eq(t, e.Map.X0, -6899.999512)
			eq(t, e.Map.X1, -3966.666504)
			eq(t, e.Map.Y0, -4833.333008)
			eq(t, e.Map.Y1, -433.333313)
			// A real fix at X=-6399.49, Y=-3853.19 was read in game as 77.7 across (from the west/left edge, Y1).
			across := (e.Map.Y1 - -3853.19) / (e.Map.Y1 - e.Map.Y0) * 100
			if across < 77.6 || across > 77.8 {
				t.Fatalf("across = %.2f, want ~77.7", across)
			}
		}},
		{"combat log version", `11/10/2026 15:10:53.402-6  COMBAT_LOG_VERSION,22,ADVANCED_LOG_ENABLED,1,BUILD_VERSION,1.60.1,PROJECT_ID,18`, func(t *testing.T, e Event) {
			if e.AdvancedEnabled == nil || !*e.AdvancedEnabled {
				t.Fatal("advanced flag")
			}
		}},
		{"unit died", `11/10/2026 15:11:28.590-6  UNIT_DIED,0000000000000000,nil,0x80000000,0x80000000,Player-1-0000AAAA,"Alpha-Realm-PvP-US",0x518,0x80000000,0`, func(t *testing.T, e Event) {
			eq(t, e.Src.GUID, "")
			eq(t, e.Dst.GUID, "Player-1-0000AAAA")
		}},
		{"quoted comma and doubled quote", `11/10/2026 15:11:28.590-6  SPELL_AURA_APPLIED,Player-1-0000AAAA,"Al""pha,x-Realm",0x518,0x80000000,Player-1-0000AAAA,"Al""pha,x-Realm",0x518,0x80000000,1,"Buff",0x1,BUFF`, func(t *testing.T, e Event) {
			eq(t, e.Src.Name, `Al"pha,x-Realm`)
		}},
		{"no offset uses given location", `11/10/2026 15:11:28.590  UNIT_DIED,0000000000000000,nil,0x0,0x0,Player-1-0000AAAA,"A",0x518,0x0,0`, func(t *testing.T, e Event) {
			want := time.Date(2026, 11, 10, 15, 11, 28, 590_000_000, time.UTC)
			if !e.Time.Equal(want) {
				t.Fatalf("time %v want %v", e.Time, want)
			}
		}},
		{"positive fractional offset", `11/10/2026 15:11:28.590+5:30  UNIT_DIED,0000000000000000,nil,0x0,0x0,Player-1-0000AAAA,"A",0x518,0x0,0`, func(t *testing.T, e Event) {
			want := time.Date(2026, 11, 10, 9, 41, 28, 590_000_000, time.UTC)
			if !e.Time.Equal(want) {
				t.Fatalf("time %v want %v", e.Time, want)
			}
		}},
		{"integer positions and heal amount", `11/10/2026 18:14:19.603-6  SPELL_HEAL,Player-1-0000BBBB,"B",0x511,0x0,Player-1-0000BBBB,"B",0x511,0x0,1,"H",0x2,Player-1-0000BBBB,0000000000000000,10,20,0,0,0,0,0,0,0,5,5,0,-100,200,1453,1,40,7,7,0,0,nil`, func(t *testing.T, e Event) {
			eq(t, e.Info.X, -100.0)
			eq(t, e.Info.Y, 200.0)
			eq(t, e.IsDamage, false)
			eq(t, e.IsHeal, true)
			eq(t, e.Amount, 7.0)
		}},
		{"damage landed is not counted as damage", `11/10/2026 15:10:58.961-6  SWING_DAMAGE_LANDED,Creature-0-1-0-11-1127-0001F38844,"Boar",0xa28,0x0,Player-1-0000AAAA,"A",0x518,0x0,Player-1-0000AAAA,0000000000000000,239,248,24,0,126,0,0,0,0,124,385,0,-5248.57,-439.78,1426,2.1674,0,9,9,-1,1,0,0,0,nil,nil,nil`, func(t *testing.T, e Event) {
			eq(t, e.IsDamage, false)
			eq(t, e.Info.HP, 239.0)
		}},
		{"environmental damage prefix of one", `11/10/2026 15:10:58.961-6  ENVIRONMENTAL_DAMAGE,0000000000000000,nil,0x80000000,0x80000000,Player-1-0000AAAA,"A",0x518,0x0,Falling,Player-1-0000AAAA,0000000000000000,239,248,24,0,126,0,0,0,0,124,385,0,-5248.57,-439.78,1426,2.1674,0,9,9,-1,1,0,0,0,nil,nil,nil`, func(t *testing.T, e Event) {
			eq(t, e.Amount, 9.0)
			eq(t, e.IsDamage, true)
		}},
		{"non advanced damage", `11/10/2026 15:10:58.961-6  SWING_DAMAGE,Player-1-0000AAAA,"A",0x518,0x0,Creature-0-1-0-11-1-0001,"Boar",0xa28,0x0,12,-1,1,0,0,0,nil,nil,nil`, func(t *testing.T, e Event) {
			if e.Info != nil {
				t.Fatal("info should be nil")
			}
			eq(t, e.Amount, 12.0)
		}},
		{"adv at unexpected offset: fallback scan finds the block", `11/10/2026 15:10:58.961-6  WEIRD_EVENT,Player-1-0000AAAA,"A",0x518,0x0,0000000000000000,nil,0x0,0x0,1,2,3,4,5,Player-1-0000AAAA,0000000000000000,239,248,24,0,126,0,0,0,0,124,385,0,-5248.57,-439.78,1426,2.1674,0`, func(t *testing.T, e Event) {
			if e.Info == nil || e.Info.GUID != "Player-1-0000AAAA" {
				t.Fatalf("fallback failed: %+v", e.Info)
			}
			eq(t, e.Info.X, -5248.57)
		}},
		{"empty", ``, nil},
		{"no separator", `11/10/2026 15:10:58.961-6 EVENT`, nil},
		{"bad timestamp", `garbage  EVENT,a`, nil},
		{"bad date", `13/45/2026 15:10:58.961-6  EVENT,a`, nil},
		{"unterminated quote", `11/10/2026 15:10:58.961-6  EVENT,"abc`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseLine(tc.line, time.UTC)
			if tc.check == nil {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, e)
		})
	}
}

func TestParseLineTrailingNewlineAndNilLoc(t *testing.T) {
	e, err := ParseLine(cast+"\r\n", nil)
	if err != nil || e.Type != "SPELL_CAST_SUCCESS" {
		t.Fatalf("%v %v", e.Type, err)
	}
}

func TestBadCoordinates(t *testing.T) {
	l := `11/10/2026 18:14:19.603-6  SPELL_CAST_SUCCESS,Player-1-0000BBBB,"B",0x511,0x0,0000000000000000,nil,0x0,0x0,1,"B",0x1,Player-1-0000BBBB,0000000000000000,67,67,20,0,47,0,0,0,0,184,184,0,abc,def,1453,0.6,0`
	e, err := ParseLine(l, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if e.Info == nil || e.Info.HasPos {
		t.Fatal("bad coordinates must not give a position")
	}
	if e.Info.HP != 67 {
		t.Fatal("hp still parsed")
	}
}

func TestReaction(t *testing.T) {
	tests := []struct {
		flags uint32
		want  Reaction
	}{
		{0x511, ReactionFriendly}, {0x518, ReactionFriendly}, {0x528, ReactionNeutral},
		{0x548, ReactionHostile}, {0xa48, ReactionHostile}, {0x1, ReactionFriendly}, {0x0, ReactionUnknown}, {0x500, ReactionUnknown},
	}
	for _, tc := range tests {
		if got := ReactionOf(tc.flags); got != tc.want {
			t.Errorf("%#x: %v want %v", tc.flags, got, tc.want)
		}
	}
	if !IsMine(0x511) || IsMine(0x518) {
		t.Fatal("IsMine")
	}
	for r, s := range map[Reaction]string{ReactionFriendly: "friendly", ReactionHostile: "hostile", ReactionNeutral: "neutral", ReactionUnknown: "unknown"} {
		eq(t, r.String(), s)
	}
}

func TestGUIDKind(t *testing.T) {
	tests := map[string]GUIDKind{
		"Player-1-ABC": KindPlayer, "Creature-0-1": KindNPC, "Pet-0-1": KindPet, "Vehicle-0-1": KindNPC,
		"GameObject-0-1": KindOther, "": KindOther,
	}
	for g, want := range tests {
		if got := KindOf(g); got != want {
			t.Errorf("%s: %v want %v", g, got, want)
		}
	}
}

func TestDisplayNamer(t *testing.T) {
	n := NewNamer("")
	for _, raw := range []string{"A-B-PvP-US", "C-D-PvP-US", "E-F-PvP-US", "G-H-PvP-US", "I-J-PvP-US"} {
		n.Observe(raw)
	}
	eq(t, n.Suffix(), "")
	eq(t, n.Display("A-B-PvP-US"), "A B PvP US")
	n.Observe("K-L-PvP-US")
	eq(t, n.Suffix(), "-PvP-US")
	eq(t, n.Display("A-B-PvP-US"), "A B")
	eq(t, n.Display("Boar"), "Boar")
	n.Observe("M-N-Other")
	eq(t, n.Suffix(), "-PvP-US") // locked in once detected
	fixed := NewNamer("-PvP-EU")
	fixed.Observe("A-B-PvP-US")
	eq(t, fixed.Display("X-Y-PvP-EU"), "X Y")
	eq(t, fixed.Suffix(), "-PvP-EU")
	eq(t, NewNamer("PvP-EU").Suffix(), "-PvP-EU")
}

func TestCommonSuffix(t *testing.T) {
	eq(t, commonSuffix(nil), "")
	eq(t, commonSuffix([]string{"A-B-C", "D-E-C"}), "-C")
	eq(t, commonSuffix([]string{"A", "A"}), "")
	eq(t, commonSuffix([]string{"A-B", "C-D"}), "")
}

func eq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if f, ok := any(got).(float64); ok {
		if math.Abs(f-any(want).(float64)) > 1e-9 {
			t.Fatalf("got %v want %v", got, want)
		}
		return
	}
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestUnitDiedUnconsciousFlag(t *testing.T) {
	const died = `11/10/2026 15:11:28.590-6  UNIT_DIED,0000000000000000,nil,0x80000000,0x80000000,Player-1-0000AAAA,"Alpha-Realm-PvP-US",0x518,0x80000000,`
	for flag, want := range map[string]bool{"0": false, "1": true} {
		e, err := ParseLine(died+flag, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, e.Type, "UNIT_DIED")
		eq(t, e.Src.GUID, "")
		eq(t, e.Dst.GUID, "Player-1-0000AAAA")
		eq(t, e.Dst.Flags, uint32(0x518))
		eq(t, e.Unconscious, want)
	}
	// a UNIT_DIED without the trailing field is not unconscious
	e, _ := ParseLine(`11/10/2026 15:11:28.590-6  UNIT_DIED,0000000000000000,nil,0x80000000,0x80000000,Player-1-0000AAAA,"Alpha-Realm-PvP-US",0x518,0x80000000`, time.UTC)
	eq(t, e.Unconscious, false)
	// other events never set it
	e, _ = ParseLine(swing, time.UTC)
	eq(t, e.Unconscious, false)
}
