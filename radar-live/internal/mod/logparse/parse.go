package logparse

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"radarlive/internal/domain"
)

// MaxLineBytes is the longest line ParseLine accepts (1 MiB).
const MaxLineBytes = 1 << 20

// Field layout constants of the combat log.
const (
	firstUnitField   = 1
	unitFieldCount   = 4
	secondUnitField  = firstUnitField + unitFieldCount
	firstParamField  = secondUnitField + unitFieldCount
	minUnitRecordLen = firstParamField
	advancedLen      = 19
	hexBase          = 16
	hexBits          = 32

	prefixSwing  = 0
	prefixEnv    = 1
	prefixSpell  = 3
	prefixNoInfo = -1

	advOwner  = 1
	advHP     = 2
	advMaxHP  = 3
	advX      = 14
	advY      = 15
	advMap    = 16
	advFacing = 17
	advLevel  = 18

	mapChangeFields = 7
	nilText         = "nil"
	emptyGUID       = "0000000000000000"
	timestampGap    = "  "
)

// Errors returned by ParseLine; both can be tested with errors.Is(err, ErrBadLine).
var (
	// ErrBadLine marks a line that cannot be parsed.
	ErrBadLine = errors.New("bad log line")
	// ErrLineTooLong marks a line over MaxLineBytes; it also matches ErrBadLine.
	ErrLineTooLong = fmt.Errorf("%w: longer than %d bytes", ErrBadLine, MaxLineBytes)
)

// Options configure ParseLine.
type Options struct {
	// FallbackZone applies to timestamps without a UTC offset (nil means UTC).
	FallbackZone *time.Location
	// Source is copied to Event.Source.
	Source string
}

// ParseLine parses one combat-log line into an Event. It never panics; unparseable lines return an
// error wrapping ErrBadLine. Records that are valid but too short to name units yield an Event
// carrying only the time and record name.
func ParseLine(line string, opts Options) (domain.Event, error) {
	if len(line) > MaxLineBytes {
		return domain.Event{}, ErrLineTooLong
	}
	line = strings.TrimRight(line, "\r\n")
	gap := strings.Index(line, timestampGap)
	if gap < 0 {
		return domain.Event{}, fmt.Errorf("%w: no timestamp separator", ErrBadLine)
	}
	ts, err := parseTimestamp(line[:gap], opts.FallbackZone)
	if err != nil {
		return domain.Event{}, err
	}
	fields, err := splitCSV(line[gap+len(timestampGap):])
	if err != nil {
		return domain.Event{}, err
	}
	if fields[0] == "" {
		return domain.Event{}, fmt.Errorf("%w: empty record", ErrBadLine)
	}
	ev := domain.Event{Source: opts.Source, TimeMs: ts.UnixMilli(), Name: fields[0]}
	if parseSpecial(&ev, fields) {
		return ev, nil
	}
	if len(fields) < minUnitRecordLen {
		return ev, nil
	}
	parseUnitRecord(&ev, fields)
	return ev, nil
}

// parseSpecial fills the non-combat records and reports whether ev.Name was one of them.
func parseSpecial(ev *domain.Event, f []string) bool {
	switch ev.Name {
	case "COMBAT_LOG_VERSION":
		for k := 1; k+1 < len(f); k++ {
			if f[k] == "ADVANCED_LOG_ENABLED" {
				enabled := f[k+1] == "1"
				ev.AdvancedEnabled = &enabled
			}
		}
	case "ZONE_CHANGE":
		if len(f) > 2 {
			ev.Zone = f[2]
		}
	case "MAP_CHANGE":
		ev.Chart = parseMapChange(f)
	default:
		return false
	}
	return true
}

// parseMapChange reads MAP_CHANGE,id,"Name",a,b,c,d: the x range is (a,b) and the y range is (c,d).
func parseMapChange(f []string) *domain.Chart {
	if len(f) < mapChangeFields {
		return nil
	}
	id, idErr := strconv.Atoi(f[1])
	var v [4]float64
	for i := range v {
		n, err := strconv.ParseFloat(f[3+i], 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil
		}
		v[i] = n
	}
	if idErr != nil || id < 0 {
		return nil
	}
	return &domain.Chart{MapID: id, Name: f[2],
		X0: math.Min(v[0], v[1]), X1: math.Max(v[0], v[1]), Y0: math.Min(v[2], v[3]), Y1: math.Max(v[2], v[3])}
}

// parseUnitRecord fills units, the advanced block, damage data and the ruleset of a combat record.
func parseUnitRecord(ev *domain.Event, f []string) {
	ev.Src = unitAt(f, firstUnitField)
	ev.Dst = unitAt(f, secondUnitField)
	if ev.Name == "UNIT_DIED" && len(f) > firstParamField && f[firstParamField] == "1" {
		ev.Unconscious = true
	}
	prefix := prefixLen(ev.Name)
	start := firstParamField + max(prefix, 0)
	if prefix >= 0 {
		ev.Info = parseInfo(f, start)
	} else {
		ev.Info = scanInfo(f)
	}
	ev.IsDamage = isDamage(ev.Name)
	ev.IsHeal = ev.Name == "SPELL_HEAL" || ev.Name == "SPELL_PERIODIC_HEAL"
	if prefix >= 0 && (ev.IsDamage || ev.IsHeal) {
		setAmount(ev, f, start)
	}
	ev.Ruleset = detectRuleset(ev)
}

func setAmount(ev *domain.Event, f []string, start int) {
	if ev.Info != nil {
		start += advancedLen
	}
	if start >= len(f) {
		return
	}
	if v, err := strconv.ParseFloat(f[start], 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
		ev.Amount, ev.HasAmount = v, true
	}
}

// scanInfo looks for an advanced block in a record whose prefix length is unknown.
func scanInfo(f []string) *domain.EventInfo {
	for k := firstParamField; k+advancedLen <= len(f); k++ {
		if info := parseInfo(f, k); info != nil && info.HasPos {
			return info
		}
	}
	return nil
}

// detectRuleset reads the realm token from the log owner's raw name, if the owner is on the line.
func detectRuleset(ev *domain.Event) domain.Ruleset {
	for _, u := range []domain.EventUnit{ev.Src, ev.Dst} {
		if u.IsMine() {
			if rs, ok := DetectRealmToken(u.Raw); ok {
				return rs
			}
		}
	}
	return ""
}

func isDamage(name string) bool {
	switch name {
	case "DAMAGE_SHIELD", "DAMAGE_SPLIT":
		return true
	}
	return strings.HasSuffix(name, "_DAMAGE") && !strings.HasSuffix(name, "_LANDED")
}

// prefixLen is the number of prefix params before the advanced block, or prefixNoInfo if unknown.
func prefixLen(name string) int {
	switch {
	case strings.HasPrefix(name, "SWING_"):
		return prefixSwing
	case strings.HasPrefix(name, "ENVIRONMENTAL_"):
		return prefixEnv
	case strings.HasPrefix(name, "SPELL_"), strings.HasPrefix(name, "RANGE_"),
		name == "DAMAGE_SHIELD", name == "DAMAGE_SPLIT", name == "DAMAGE_SHIELD_MISSED":
		return prefixSpell
	}
	return prefixNoInfo
}

func unitAt(f []string, i int) domain.EventUnit {
	u := domain.EventUnit{GUID: f[i], Raw: f[i+1], Flags: parseHex(f[i+2]), RaidFlags: parseHex(f[i+3])}
	if isEmptyGUID(u.GUID) {
		u.GUID = ""
	}
	if u.Raw == nilText {
		u.Raw = ""
	}
	return u
}

func isEmptyGUID(g string) bool { return g == "" || g == emptyGUID || g == nilText }

func parseHex(s string) uint32 {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	v, err := strconv.ParseUint(s, hexBase, hexBits)
	if err != nil {
		return 0
	}
	return uint32(v)
}

// parseInfo reads the advanced block that starts at f[s], or nil when it does not start there.
func parseInfo(f []string, s int) *domain.EventInfo {
	if s+advancedLen > len(f) || domain.KindOf(f[s]) == domain.KindOther {
		return nil
	}
	in := &domain.EventInfo{GUID: f[s]}
	if !isEmptyGUID(f[s+advOwner]) {
		in.Owner = f[s+advOwner]
	}
	hp, e1 := strconv.ParseFloat(f[s+advHP], 64)
	maxHP, e2 := strconv.ParseFloat(f[s+advMaxHP], 64)
	if e1 == nil && e2 == nil && isFinite(hp) && isFinite(maxHP) {
		in.HP, in.MaxHP, in.HasHP = hp, maxHP, true
	}
	x, e3 := strconv.ParseFloat(f[s+advX], 64)
	y, e4 := strconv.ParseFloat(f[s+advY], 64)
	mapID, e5 := strconv.Atoi(f[s+advMap])
	if e3 == nil && e4 == nil && e5 == nil && isFinite(x) && isFinite(y) {
		in.X, in.Y, in.MapID, in.HasPos = x, y, mapID, true
	}
	in.Facing, _ = strconv.ParseFloat(f[s+advFacing], 64)
	in.Level, _ = strconv.Atoi(f[s+advLevel])
	return in
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
