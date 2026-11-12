package logparse

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"radarlive/internal/domain"
)

const stamp = "11/5/2026 20:15:30.250-5  "

var stampMs = time.Date(2026, 11, 6, 1, 15, 30, 250e6, time.UTC).UnixMilli()

// adv builds a 19 field advanced block.
func adv(guid, owner string, hp, maxHP int, x, y float64, mapID int, level int) string {
	return fmt.Sprintf("%s,%s,%d,%d,0,0,0,0,0,0,0,0,0,0,%g,%g,%d,1.5,%d", guid, owner, hp, maxHP, x, y, mapID, level)
}

const (
	meGUID  = "Player-1-0A"
	foeGUID = "Player-1-0B"
	nilGUID = "0000000000000000"
)

func mustParse(t *testing.T, line string) domain.Event {
	t.Helper()
	ev, err := ParseLine(line, Options{})
	if err != nil {
		t.Fatalf("ParseLine(%q): %v", line, err)
	}
	return ev
}

func TestParseSpellDamageWithAdvancedBlock(t *testing.T) {
	line := stamp + "SPELL_DAMAGE," + meGUID + `,"Ann-Bee-PvP-US",0x511,0x0,` + foeGUID + `,"Cat-PvP-US",0x548,0x0,` +
		`100,"Fire Bolt",4,` + adv(foeGUID, nilGUID, 800, 1000, 12.5, -3.25, 1500, 60) + ",250,250,0,4,0,0,0,nil,nil,nil"
	ev := mustParse(t, line)
	if ev.Name != "SPELL_DAMAGE" || ev.TimeMs != stampMs {
		t.Fatalf("name/time: %+v", ev)
	}
	if ev.Src.GUID != meGUID || ev.Src.Raw != "Ann-Bee-PvP-US" || ev.Src.Flags != 0x511 || ev.Dst.Flags != 0x548 {
		t.Fatalf("units: %+v %+v", ev.Src, ev.Dst)
	}
	in := ev.Info
	if in == nil || in.GUID != foeGUID || in.Owner != "" || !in.HasHP || in.HP != 800 || in.MaxHP != 1000 ||
		!in.HasPos || in.X != 12.5 || in.Y != -3.25 || in.MapID != 1500 || in.Level != 60 || in.Facing != 1.5 {
		t.Fatalf("info: %+v", in)
	}
	if !ev.IsDamage || !ev.HasAmount || ev.Amount != 250 {
		t.Fatalf("amount: %+v", ev)
	}
	if ev.Ruleset != domain.RulesetPvP {
		t.Fatalf("ruleset from owner raw name: %q", ev.Ruleset)
	}
}

func TestParsePrefixLayouts(t *testing.T) {
	block := adv(meGUID, nilGUID, 5, 10, 1, 2, 7, 3)
	units := meGUID + `,"A",0x511,0x0,` + foeGUID + `,"B",0x548,0x0,`
	cases := []struct {
		name, body string
		amount     float64
	}{
		{"SWING_DAMAGE", units + block + ",77,77,0,1,0,0,0,nil,nil,nil", 77},
		{"SWING_DAMAGE_LANDED", units + block + ",70,70,0,1,0,0,0,nil,nil,nil", 70},
		{"ENVIRONMENTAL_DAMAGE", units + `Falling,` + block + ",33,33,0,1,0,0,0", 33},
		{"RANGE_DAMAGE", units + `5,"Shot",1,` + block + ",44,44,0,1,0,0,0", 44},
		{"SPELL_PERIODIC_DAMAGE", units + `5,"Burn",4,` + block + ",12,12,0,4,0,0,0", 12},
		{"SPELL_HEAL", units + `5,"Heal",2,` + block + ",90,90,0,0", 90},
		{"DAMAGE_SHIELD", units + `5,"Thorns",8,` + block + ",9,9,0,8,0,0,0", 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := mustParse(t, stamp+c.name+","+c.body)
			if ev.Info == nil || ev.Info.GUID != meGUID || ev.Info.Level != 3 {
				t.Fatalf("info: %+v", ev.Info)
			}
			landed := strings.HasSuffix(c.name, "_LANDED") // duplicates carry no credited amount
			if ev.HasAmount == landed || (!landed && ev.Amount != c.amount) {
				t.Fatalf("amount=%v has=%v", ev.Amount, ev.HasAmount)
			}
			wantDamage := !landed && c.name != "SPELL_HEAL"
			if ev.IsDamage != wantDamage || ev.IsHeal != (c.name == "SPELL_HEAL") {
				t.Fatalf("flags damage=%v heal=%v", ev.IsDamage, ev.IsHeal)
			}
		})
	}
}

func TestParseWithoutAdvancedBlock(t *testing.T) {
	line := stamp + "SPELL_AURA_APPLIED," + meGUID + `,"A",0x511,0x0,` + foeGUID + `,"B",0x548,0x0,5,"Buff",1,BUFF`
	ev := mustParse(t, line)
	if ev.Info != nil {
		t.Fatalf("no advanced block expected: %+v", ev.Info)
	}
}

func TestParseUnknownEventScansForBlock(t *testing.T) {
	block := adv(foeGUID, nilGUID, 5, 10, 1, 2, 7, 3)
	line := stamp + "WEIRD_EVENT," + meGUID + `,"A",0x511,0x0,` + foeGUID + `,"B",0x548,0x0,x,y,` + block
	ev := mustParse(t, line)
	if ev.Info == nil || ev.Info.GUID != foeGUID {
		t.Fatalf("scan failed: %+v", ev.Info)
	}
	if ev.IsDamage || ev.HasAmount {
		t.Fatalf("unknown events carry no amount: %+v", ev)
	}
}

func TestParseOwnerGUIDAndNilUnits(t *testing.T) {
	block := adv("Pet-0-1", meGUID, 5, 10, 1, 2, 7, 3)
	line := stamp + `SWING_DAMAGE,Pet-0-1,"Wolf",0x1112,0x0,` + nilGUID + `,nil,0x80000000,0x80000000,` + block + ",5,5,0,1,0,0,0"
	ev := mustParse(t, line)
	if ev.Info.Owner != meGUID {
		t.Fatalf("owner=%q", ev.Info.Owner)
	}
	if ev.Dst.GUID != "" || ev.Dst.Raw != "" {
		t.Fatalf("nil unit not cleared: %+v", ev.Dst)
	}
	if ev.Dst.Flags != 0x80000000 {
		t.Fatalf("flags=%#x", ev.Dst.Flags)
	}
}

func TestParseBadPositionIsIgnored(t *testing.T) {
	block := strings.Replace(adv(meGUID, nilGUID, 5, 10, 1, 2, 7, 3), ",1,2,7,", ",NaN,2,7,", 1)
	ev := mustParse(t, stamp+"SWING_DAMAGE,"+meGUID+`,"A",0x511,0x0,`+foeGUID+`,"B",0x548,0x0,`+block+",5,5")
	if ev.Info.HasPos || math.IsNaN(ev.Info.X) {
		t.Fatalf("NaN position accepted: %+v", ev.Info)
	}
	if !ev.Info.HasHP {
		t.Fatal("hp should still parse")
	}
}

func TestParseUnitDied(t *testing.T) {
	base := stamp + "UNIT_DIED," + nilGUID + ",nil,0x80000000,0x80000000," + foeGUID + `,"B",0x548,0x0`
	if ev := mustParse(t, base+",0"); ev.Unconscious || ev.Name != "UNIT_DIED" || ev.Dst.GUID != foeGUID {
		t.Fatalf("real death: %+v", ev)
	}
	if ev := mustParse(t, base+",1"); !ev.Unconscious {
		t.Fatal("feign death not flagged")
	}
	if ev := mustParse(t, base); ev.Unconscious {
		t.Fatal("missing flag means not unconscious")
	}
}

func TestParseSpecialEvents(t *testing.T) {
	ev := mustParse(t, stamp+"COMBAT_LOG_VERSION,22,ADVANCED_LOG_ENABLED,1,BUILD_VERSION,1.0.0,PROJECT_ID,5")
	if ev.AdvancedEnabled == nil || !*ev.AdvancedEnabled {
		t.Fatalf("advanced: %v", ev.AdvancedEnabled)
	}
	ev = mustParse(t, stamp+"COMBAT_LOG_VERSION,22,ADVANCED_LOG_ENABLED,0")
	if ev.AdvancedEnabled == nil || *ev.AdvancedEnabled {
		t.Fatal("advanced should be false")
	}
	ev = mustParse(t, stamp+`ZONE_CHANGE,1500,"Testville",0`)
	if ev.Zone != "Testville" {
		t.Fatalf("zone=%q", ev.Zone)
	}
	ev = mustParse(t, stamp+`ZONE_CHANGE,1500`)
	if ev.Zone != "" {
		t.Fatalf("zone=%q", ev.Zone)
	}
	ev = mustParse(t, stamp+`MAP_CHANGE,1500,"Testville",100.5,-50,-20,300`)
	want := domain.Chart{MapID: 1500, Name: "Testville", X0: -50, X1: 100.5, Y0: -20, Y1: 300}
	if ev.Chart == nil || *ev.Chart != want {
		t.Fatalf("chart=%+v want %+v", ev.Chart, want)
	}
	ev = mustParse(t, stamp+`MAP_CHANGE,1500,"Testville",x,1,2,3`)
	if ev.Chart != nil {
		t.Fatal("garbled map change must not produce a chart")
	}
	ev = mustParse(t, stamp+`MAP_CHANGE,1500`)
	if ev.Chart != nil {
		t.Fatal("short map change must not produce a chart")
	}
}

func TestParseShortRecordReturnsNameOnly(t *testing.T) {
	ev := mustParse(t, stamp+"ENCOUNTER_START,1,2")
	if ev.Name != "ENCOUNTER_START" || ev.Src.GUID != "" || ev.Info != nil {
		t.Fatalf("%+v", ev)
	}
}

func TestParseLineErrors(t *testing.T) {
	cases := map[string]string{
		"no separator":  "garbage without spaces",
		"bad timestamp": "xx/yy/zz 1:2:3  SWING_DAMAGE,a",
		"empty record":  stamp,
		"empty name":    stamp + ",a,b",
		"bad quote":     stamp + `SWING_DAMAGE,"x`,
		"empty line":    "",
	}
	for name, line := range cases {
		if _, err := ParseLine(line, Options{}); !errors.Is(err, ErrBadLine) {
			t.Errorf("%s: want ErrBadLine, got %v", name, err)
		}
	}
	long := stamp + "SWING_DAMAGE," + strings.Repeat("x", MaxLineBytes)
	if _, err := ParseLine(long, Options{}); !errors.Is(err, ErrLineTooLong) || !errors.Is(err, ErrBadLine) {
		t.Fatalf("long line: %v", err)
	}
}

func TestParseTrimsLineEnding(t *testing.T) {
	ev := mustParse(t, stamp+"ZONE_CHANGE,1,\"Z\",0\r\n")
	if ev.Zone != "Z" {
		t.Fatalf("zone=%q", ev.Zone)
	}
}

func TestParseFallbackZoneAndSource(t *testing.T) {
	est := time.FixedZone("EST", -5*3600)
	ev, err := ParseLine("11/5/2026 20:15:30.250  ZONE_CHANGE,1,\"Z\",0", Options{FallbackZone: est, Source: "src1"})
	if err != nil {
		t.Fatal(err)
	}
	if ev.TimeMs != stampMs || ev.Source != "src1" {
		t.Fatalf("%+v", ev)
	}
}

func TestParseNeverPanicsOnGarbage(t *testing.T) {
	fields := []string{"SPELL_DAMAGE", "", "nil", "0x", "1", "\"", "-", "Player-1", ","}
	for i := range 2000 {
		var parts []string
		for j := range i % 40 {
			parts = append(parts, fields[(i*7+j*3)%len(fields)])
		}
		_, _ = ParseLine(stamp+strings.Join(parts, ","), Options{})
	}
}
