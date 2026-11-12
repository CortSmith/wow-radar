package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestArmorySeedCursorRoundTrip(t *testing.T) {
	for _, c := range []string{"", "c|ann|testrealm", "c|ann|", "g|testrealm|Synthetic|Guild", "g||"} {
		kind, a, b, err := ParseArmorySeedCursor(c)
		if err != nil {
			t.Fatalf("%q: %v", c, err)
		}
		if got := FormatArmorySeedCursor(kind, a, b); got != c {
			t.Fatalf("round trip %q -> %q", c, got)
		}
	}
}

func TestArmorySeedCursorRejects(t *testing.T) {
	for _, c := range []string{"x", "c", "c|a", "g|a", "z|a|b", "c|a|b\x00", strings.Repeat("a", ArmoryCursorMax+1),
		"c|'; DROP TABLE x;--|a\n", "C|a|b"} {
		if _, _, _, err := ParseArmorySeedCursor(c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err %v, want ErrInvalid", c, err)
		}
	}
}

func TestArmorySeedLimit(t *testing.T) {
	for _, n := range []int{0, -1, 501} {
		if err := ValidateArmorySeedLimit(n); !errors.Is(err, ErrInvalid) {
			t.Errorf("limit %d: %v", n, err)
		}
	}
	for _, n := range []int{1, 200, 500} {
		if err := ValidateArmorySeedLimit(n); err != nil {
			t.Errorf("limit %d: %v", n, err)
		}
	}
}

func TestArmorySeedValidate(t *testing.T) {
	good := []ArmorySeed{
		{Kind: ArmorySeedCharacter, Realm: "Testrealm", Name: "Ann"},
		{Kind: ArmorySeedCharacter, Name: "Ann"},
		{Kind: ArmorySeedGuild, Realm: "Testrealm", Guild: "Synthetic Guild"},
	}
	for _, s := range good {
		if err := s.Validate(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	bad := []ArmorySeed{
		{Kind: "x", Name: "Ann"},
		{Kind: ArmorySeedCharacter},
		{Kind: ArmorySeedCharacter, Name: "A1"},
		{Kind: ArmorySeedCharacter, Realm: "Bad Realm!", Name: "Ann"},
		{Kind: ArmorySeedGuild, Realm: "Testrealm"},
		{Kind: ArmorySeedGuild, Guild: "G"},
	}
	for _, s := range bad {
		if err := s.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", s, err)
		}
	}
}

func TestArmoryRealmFromRaw(t *testing.T) {
	cases := map[string]string{"Ann-Testrealm": "Testrealm", "Ann": "", "Ann-": "", "Ann-Bad!": "", "Ann-Two Words": "Two Words"}
	for raw, want := range cases {
		if got := ArmoryRealmFromRaw(raw); got != want {
			t.Errorf("%q: %q, want %q", raw, got, want)
		}
	}
}

func TestBuildArmorySeedPage(t *testing.T) {
	chars := []ArmorySeed{{Kind: "character", Name: "Ann"}, {Kind: "character", Name: "Bob", Realm: "R"}}
	guilds := []ArmorySeed{{Kind: "guild", Realm: "R", Guild: "G1"}, {Kind: "guild", Realm: "R", Guild: "G2"}}
	after := func(src []ArmorySeed, key func(ArmorySeed) (string, string)) ArmorySeedSource {
		return func(a, b string, n int) ([]ArmorySeed, error) {
			var out []ArmorySeed
			for _, s := range src {
				k1, k2 := key(s)
				if (k1 > a || (k1 == a && k2 > b)) && len(out) < n {
					out = append(out, s)
				}
			}
			return out, nil
		}
	}
	cs := after(chars, func(s ArmorySeed) (string, string) { return ArmoryNameKey(s.Name), ArmoryRealmKey(s.Realm) })
	gs := after(guilds, func(s ArmorySeed) (string, string) { return ArmoryRealmKey(s.Realm), s.Guild })
	for limit := 1; limit <= 5; limit++ {
		var all []ArmorySeed
		cursor := ""
		for i := 0; i < 20; i++ {
			p, err := BuildArmorySeedPage(cursor, limit, cs, gs)
			if err != nil {
				t.Fatal(err)
			}
			if p.Items == nil || len(p.Items) > limit {
				t.Fatalf("limit %d: bad items %v", limit, p.Items)
			}
			all = append(all, p.Items...)
			if p.Next == "" {
				break
			}
			cursor = p.Next
		}
		if len(all) != 4 || all[0].Name != "Ann" || all[3].Guild != "G2" {
			t.Fatalf("limit %d: %+v", limit, all)
		}
	}
	if _, err := BuildArmorySeedPage("bogus", 10, cs, gs); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := BuildArmorySeedPage("", 0, cs, gs); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
