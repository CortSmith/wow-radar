package ruleset

import "testing"

func TestAll(t *testing.T) {
	all := All()
	want := []struct {
		id   ID
		name string
	}{{Normal, "Normal"}, {PvP, "PvP"}, {RP, "RP"}, {RPPvP, "RP-PvP"}}
	if len(all) != len(want) {
		t.Fatalf("%v", all)
	}
	for i, w := range want {
		if all[i].ID != w.id || all[i].Name != w.name || NameOf(w.id) != w.name {
			t.Errorf("%d: %+v", i, all[i])
		}
	}
	all[0].Name = "mutated" // a copy
	if All()[0].Name != "Normal" {
		t.Fatal("All must return a copy")
	}
	if NameOf("zzz") != "" {
		t.Fatal("unknown name")
	}
	if string(Default) != "pvp" {
		t.Fatal("default")
	}
}

func TestParse(t *testing.T) {
	ok := map[string]ID{
		"normal": Normal, "Normal": Normal, " NORMAL ": Normal,
		"pvp": PvP, "PvP": PvP, "rp": RP, "RP": RP,
		"rppvp": RPPvP, "RPPvP": RPPvP, "rp-pvp": RPPvP, "RP-PvP": RPPvP, "rp_pvp": RPPvP, "rp pvp": RPPvP,
	}
	for in, want := range ok {
		if got, found := Parse(in); !found || got != want {
			t.Errorf("Parse(%q) = %q,%v", in, got, found)
		}
	}
	for _, bad := range []string{"", "auto", "pve", "rp-", "rp pvp x", "normalpvp"} {
		if got, found := Parse(bad); found || got != "" {
			t.Errorf("Parse(%q) = %q,%v", bad, got, found)
		}
	}
}

func TestDetect(t *testing.T) {
	if id, ok := Detect("PvP"); !ok || id != PvP {
		t.Fatal("pvp")
	}
	if _, ok := Detect("Stormrage"); ok {
		t.Fatal("unknown realm")
	}
}

func TestDetectFromRawName(t *testing.T) {
	cases := map[string]ID{
		"Mc-Cheeki-PvP-US": PvP,
		"Name-PvP-US":      PvP,
		"Name-RPPvP-EU":    RPPvP,
		"Name-RP-PvP-US":   RPPvP,
		"Name-RP-US":       RP,
		"Name-Normal-US":   Normal,
		"Name-Normal":      Normal,
		"Name-pvp":         PvP,
		"Mc-Normal-PvP-US": PvP,
	}
	for in, want := range cases {
		if got, ok := DetectFromRawName(in); !ok || got != want {
			t.Errorf("%q -> %q,%v want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "Solo", "Name-Realm-US", "PvP", "Name-Realm-PvPx-US"} {
		if got, ok := DetectFromRawName(in); ok {
			t.Errorf("%q should not detect, got %q", in, got)
		}
	}
}
