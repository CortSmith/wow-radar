package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const armoryNow = int64(1_800_000_000_000)

func goodChar() ArmoryCharacter {
	return ArmoryCharacter{
		Realm: "Testrealm", Name: "Aelwyn", Class: "Mage", Race: "Human", Faction: "Alliance",
		Level: 60, Guild: "Synthetic Guild", Health: 4200, PowerType: "mana", Power: 5100,
		Gear: json.RawMessage(`{"head":{"id":1}}`), FetchedAt: armoryNow, Status: ArmoryStatusOK,
	}
}

func TestArmoryCharacterValidateAccepts(t *testing.T) {
	c := goodChar()
	ts := armoryNow - 1000
	c.APIUpdatedAt = &ts
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestArmoryCharacterValidateRejects(t *testing.T) {
	big := json.RawMessage(`{"a":"` + strings.Repeat("x", ArmoryBlobMaxBytes) + `"}`)
	neg := int64(-5)
	cases := map[string]func(*ArmoryCharacter){
		"empty name":      func(c *ArmoryCharacter) { c.Name = "" },
		"digit name":      func(c *ArmoryCharacter) { c.Name = "Ael1" },
		"long name":       func(c *ArmoryCharacter) { c.Name = strings.Repeat("a", ArmoryNameMaxRunes+1) },
		"control name":    func(c *ArmoryCharacter) { c.Name = "Ae\x00l" },
		"empty realm":     func(c *ArmoryCharacter) { c.Realm = "" },
		"bad realm":       func(c *ArmoryCharacter) { c.Realm = "Real;m" },
		"bad status":      func(c *ArmoryCharacter) { c.Status = "weird" },
		"empty status":    func(c *ArmoryCharacter) { c.Status = "" },
		"level high":      func(c *ArmoryCharacter) { c.Level = PingLevelMax + 1 },
		"level negative":  func(c *ArmoryCharacter) { c.Level = -1 },
		"health negative": func(c *ArmoryCharacter) { c.Health = -1 },
		"power huge":      func(c *ArmoryCharacter) { c.Power = ArmoryValueMax + 1 },
		"no fetched":      func(c *ArmoryCharacter) { c.FetchedAt = 0 },
		"api time neg":    func(c *ArmoryCharacter) { c.APIUpdatedAt = &neg },
		"blob too big":    func(c *ArmoryCharacter) { c.Gear = big },
		"blob array":      func(c *ArmoryCharacter) { c.Talents = json.RawMessage(`[1]`) },
		"blob garbage":    func(c *ArmoryCharacter) { c.Stats = json.RawMessage(`{nope`) },
		"blob scalar":     func(c *ArmoryCharacter) { c.PvP = json.RawMessage(`7`) },
		"guild unsafe":    func(c *ArmoryCharacter) { c.Guild = "a‮b" },
	}
	for name, mutate := range cases {
		c := goodChar()
		mutate(&c)
		err := c.Validate()
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestArmoryCharacterSanitiseClampsAndCleans(t *testing.T) {
	c := goodChar()
	c.Class = "  Mage\n"
	c.Level = 999
	c.Health = -4
	c.Power = ArmoryValueMax * 3
	c.Guild = "G\x00uild"
	c.Gear = json.RawMessage("null")
	got := c.Sanitise()
	if got.Class != "Mage" || got.Level != PingLevelMax || got.Health != 0 || got.Power != ArmoryValueMax {
		t.Fatalf("sanitised %+v", got)
	}
	if got.Guild != "Guild" || got.Gear != nil {
		t.Fatalf("guild %q gear %q", got.Guild, got.Gear)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestArmoryNameKeys(t *testing.T) {
	if ArmoryNameKey("AeLwYn") != "aelwyn" {
		t.Fatal("name key must be lower case")
	}
	if ArmoryRealmKey(" Testrealm ") != "testrealm" {
		t.Fatal("realm key must be trimmed lower case")
	}
}

func TestGearHashIgnoresWhitespaceAndEmpty(t *testing.T) {
	a := GearHash(json.RawMessage(`{"a": 1, "b": 2}`))
	b := GearHash(json.RawMessage(`{"a":1,"b":2}`))
	if a == "" || a != b {
		t.Fatalf("hashes %q %q", a, b)
	}
	if GearHash(nil) != "" || GearHash(json.RawMessage(`{"a":2}`)) == a {
		t.Fatal("empty gear has no hash; different gear differs")
	}
}

func TestArmoryBatchValidate(t *testing.T) {
	ok := ArmoryBatch{BatchID: "b-1", RunID: "run-1", Characters: []ArmoryCharacter{goodChar()}, Cursor: "realm:1"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	many := make([]ArmoryCharacter, ArmoryBatchMax+1)
	for i := range many {
		many[i] = goodChar()
	}
	bad := map[string]ArmoryBatch{
		"no id":       {Characters: ok.Characters},
		"bad id":      {BatchID: "a b", Characters: ok.Characters},
		"bad run":     {BatchID: "b", RunID: "a b", Characters: ok.Characters},
		"too many":    {BatchID: "b", Characters: many},
		"long cursor": {BatchID: "b", Cursor: strings.Repeat("c", ArmoryCursorMax+1)},
		"bad char":    {BatchID: "b", Characters: []ArmoryCharacter{{Name: "x"}}},
	}
	for name, b := range bad {
		if err := b.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestArmoryBatchSanitiseDoesNotAliasInput(t *testing.T) {
	b := ArmoryBatch{BatchID: "b", Characters: []ArmoryCharacter{goodChar()}}
	b.Characters[0].Level = 500
	out := b.Sanitise()
	if out.Characters[0].Level != PingLevelMax || b.Characters[0].Level != 500 {
		t.Fatal("sanitise must copy")
	}
}

func TestArmoryRunValidate(t *testing.T) {
	r := ArmoryRun{ID: "run-1", StartedAt: armoryNow, Status: ArmoryRunOK}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ArmoryRun){
		"bad id":     func(r *ArmoryRun) { r.ID = "" },
		"bad status": func(r *ArmoryRun) { r.Status = "?" },
		"negative":   func(r *ArmoryRun) { r.Failed = -1 },
		"cursor":     func(r *ArmoryRun) { r.Cursor = strings.Repeat("c", ArmoryCursorMax+1) },
		"no start":   func(r *ArmoryRun) { r.StartedAt = 0 },
	} {
		c := r
		mutate(&c)
		if err := c.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestArmoryDay(t *testing.T) {
	if ArmoryDay(86_400_000*5+1) != 5 || ArmoryDay(86_400_000*5-1) != 4 {
		t.Fatal("day boundary wrong")
	}
}
