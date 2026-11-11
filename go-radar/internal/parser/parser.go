// Package parser turns lines of the WoW advanced combat log into Events.
//
// Verified layout (against a real log, 2026-11):
//
//	f[0]=event f[1..4]=src guid,name,flags,raidflags f[5..8]=dst guid,name,flags,raidflags
//	then the event PREFIX params (SWING_*: 0, SPELL_*/RANGE_*/DAMAGE_*: 3 = id,name,school,
//	ENVIRONMENTAL_*: 1), then - when advanced logging is on - a 19 field block describing the
//	"info unit" (which may be the source or the destination), then the SUFFIX params
//	(damage: amount, base, overkill, school, resisted, blocked, absorbed, critical...).
//
//	adv: 0 guid, 1 owner, 2 hp, 3 maxhp, 4 AP, 5 SP, 6 armor, 7 absorb, 8..13 power data,
//	     14 X, 15 Y, 16 uiMapID, 17 facing, 18 level
package parser

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Unit is a source or destination unit named on a line.
type Unit struct {
	GUID      string // "" when the line has no unit (0000000000000000)
	Name      string // raw log name ("" for nil)
	Flags     uint32
	RaidFlags uint32
}

// Info is the advanced-logging block: the state of one unit at the time of the event.
type Info struct {
	GUID     string
	Owner    string
	HP       float64
	MaxHP    float64
	HasHP    bool
	X, Y     float64 // X is north, Y is west in the client's orientation
	MapID    int
	Facing   float64
	Level    int
	HasPos   bool
	Position int // index in the field slice where the block starts (diagnostics)
}

// MapBounds is the world rectangle of a map from MAP_CHANGE (normalised so X0<X1, Y0<Y1).
type MapBounds struct {
	ID             int
	Name           string
	X0, X1, Y0, Y1 float64
}

// Event is one parsed log line.
type Event struct {
	Time            time.Time // UTC
	Type            string
	Src, Dst        Unit
	Info            *Info // nil when the line carries no advanced block
	Amount          float64
	HasAmount       bool
	IsDamage        bool // a damage event (excluding *_LANDED duplicates)
	IsHeal          bool
	Zone            string     // ZONE_CHANGE
	Map             *MapBounds // MAP_CHANGE
	AdvancedEnabled *bool      // COMBAT_LOG_VERSION
	Unconscious     bool       // UNIT_DIED whose trailing unconsciousFlag is 1 (feign death), not a real death
}

const advLen = 19

// ErrBadLine is returned (wrapped) for lines that cannot be parsed.
var ErrBadLine = errors.New("bad log line")

// ParseLine parses one log line. loc is used for timestamps without a UTC offset
// (nil means time.Local).
func ParseLine(line string, loc *time.Location) (Event, error) {
	line = strings.TrimRight(line, "\r\n")
	i := strings.Index(line, "  ")
	if i < 0 {
		return Event{}, fmt.Errorf("%w: no timestamp separator", ErrBadLine)
	}
	ts, err := parseTime(line[:i], loc)
	if err != nil {
		return Event{}, err
	}
	f, err := splitCSV(line[i+2:])
	if err != nil {
		return Event{}, err
	}
	if len(f) == 0 || f[0] == "" {
		return Event{}, fmt.Errorf("%w: empty record", ErrBadLine)
	}
	ev := Event{Time: ts, Type: f[0]}
	switch ev.Type {
	case "COMBAT_LOG_VERSION":
		for k := 1; k+1 < len(f); k++ {
			if f[k] == "ADVANCED_LOG_ENABLED" {
				v := f[k+1] == "1"
				ev.AdvancedEnabled = &v
			}
		}
		return ev, nil
	case "ZONE_CHANGE":
		if len(f) > 2 {
			ev.Zone = f[2]
		}
		return ev, nil
	case "MAP_CHANGE":
		if len(f) >= 7 {
			id, e1 := strconv.Atoi(f[1])
			a, e2 := strconv.ParseFloat(f[3], 64)
			b, e3 := strconv.ParseFloat(f[4], 64)
			c, e4 := strconv.ParseFloat(f[5], 64)
			d, e5 := strconv.ParseFloat(f[6], 64)
			if e1 == nil && e2 == nil && e3 == nil && e4 == nil && e5 == nil {
				// Field order is the x-range (a,b) then the y-range (c,d), in the same axes as the
				// advanced-block positions. Confirmed against the in-game coordinate readout.
				ev.Map = &MapBounds{ID: id, Name: f[2],
					X0: math.Min(a, b), X1: math.Max(a, b), Y0: math.Min(c, d), Y1: math.Max(c, d)}
			}
		}
		return ev, nil
	}
	if len(f) < 9 {
		return ev, nil
	}
	ev.Src = unitAt(f, 1)
	ev.Dst = unitAt(f, 5)
	if ev.Type == "UNIT_DIED" && len(f) > 9 && f[9] == "1" {
		ev.Unconscious = true
	}

	prefix := prefixLen(ev.Type)
	start := 9 + prefix
	if prefix >= 0 {
		ev.Info = parseInfo(f, start)
	} else {
		for k := 9; k+advLen <= len(f); k++ {
			if in := parseInfo(f, k); in != nil && in.HasPos {
				ev.Info = in
				break
			}
		}
	}
	ev.IsDamage = isDamage(ev.Type)
	ev.IsHeal = ev.Type == "SPELL_HEAL" || ev.Type == "SPELL_PERIODIC_HEAL"
	if prefix >= 0 && (ev.IsDamage || ev.IsHeal) {
		idx := start
		if ev.Info != nil {
			idx += advLen
		}
		if idx < len(f) {
			if v, err := strconv.ParseFloat(f[idx], 64); err == nil {
				ev.Amount, ev.HasAmount = v, true
			}
		}
	}
	return ev, nil
}

func isDamage(t string) bool {
	switch t {
	case "DAMAGE_SHIELD", "DAMAGE_SPLIT":
		return true
	}
	return strings.HasSuffix(t, "_DAMAGE") && !strings.HasSuffix(t, "_LANDED")
}

// prefixLen is the number of event-prefix params that precede the advanced block, or -1 if unknown.
func prefixLen(t string) int {
	switch {
	case strings.HasPrefix(t, "SWING_"):
		return 0
	case strings.HasPrefix(t, "ENVIRONMENTAL_"):
		return 1
	case strings.HasPrefix(t, "SPELL_"), strings.HasPrefix(t, "RANGE_"),
		t == "DAMAGE_SHIELD", t == "DAMAGE_SPLIT", t == "DAMAGE_SHIELD_MISSED":
		return 3
	}
	return -1
}

func unitAt(f []string, i int) Unit {
	u := Unit{GUID: f[i], Name: f[i+1]}
	if isEmptyGUID(u.GUID) {
		u.GUID = ""
	}
	if u.Name == "nil" {
		u.Name = ""
	}
	u.Flags = parseHex(f[i+2])
	u.RaidFlags = parseHex(f[i+3])
	return u
}

func isEmptyGUID(g string) bool { return g == "" || g == "0000000000000000" || g == "nil" }

func parseHex(s string) uint32 {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0
	}
	return uint32(v)
}

func parseInfo(f []string, s int) *Info {
	if s+advLen > len(f) || KindOf(f[s]) == KindOther {
		return nil
	}
	in := &Info{GUID: f[s], Position: s}
	if !isEmptyGUID(f[s+1]) {
		in.Owner = f[s+1]
	}
	hp, e1 := strconv.ParseFloat(f[s+2], 64)
	mx, e2 := strconv.ParseFloat(f[s+3], 64)
	if e1 == nil && e2 == nil {
		in.HP, in.MaxHP, in.HasHP = hp, mx, true
	}
	x, e3 := strconv.ParseFloat(f[s+14], 64)
	y, e4 := strconv.ParseFloat(f[s+15], 64)
	mp, e5 := strconv.Atoi(f[s+16])
	if e3 == nil && e4 == nil && e5 == nil && !math.IsNaN(x) && !math.IsNaN(y) &&
		!math.IsInf(x, 0) && !math.IsInf(y, 0) {
		in.X, in.Y, in.MapID, in.HasPos = x, y, mp, true
	}
	in.Facing, _ = strconv.ParseFloat(f[s+17], 64)
	in.Level, _ = strconv.Atoi(f[s+18])
	return in
}

// parseTime parses "M/D/YYYY HH:MM:SS.mmm[+-H[:MM]]" into UTC.
func parseTime(s string, loc *time.Location) (time.Time, error) {
	bad := func() (time.Time, error) { return time.Time{}, fmt.Errorf("%w: timestamp %q", ErrBadLine, s) }
	sp := strings.IndexByte(s, ' ')
	if sp < 0 {
		return bad()
	}
	date := strings.Split(s[:sp], "/")
	if len(date) != 3 {
		return bad()
	}
	clock := s[sp+1:]
	off, hasOff := 0, false
	if k := strings.LastIndexAny(clock, "+-"); k > 0 {
		o := clock[k:]
		clock = clock[:k]
		sign := 1
		if o[0] == '-' {
			sign = -1
		}
		parts := strings.SplitN(o[1:], ":", 2)
		h, err := strconv.Atoi(parts[0])
		if err != nil {
			return bad()
		}
		m := 0
		if len(parts) == 2 {
			if m, err = strconv.Atoi(parts[1]); err != nil {
				return bad()
			}
		}
		off, hasOff = sign*(h*3600+m*60), true
	}
	secs, frac, _ := strings.Cut(clock, ".")
	hms := strings.Split(secs, ":")
	if len(hms) != 3 {
		return bad()
	}
	nums := make([]int, 0, 6)
	for _, p := range append(append([]string{}, date...), hms...) {
		n, err := strconv.Atoi(p)
		if err != nil {
			return bad()
		}
		nums = append(nums, n)
	}
	mo, d, y, hh, mm, ss := nums[0], nums[1], nums[2], nums[3], nums[4], nums[5]
	if mo < 1 || mo > 12 || d < 1 || d > 31 || hh > 23 || mm > 59 || ss > 60 || hh < 0 || mm < 0 || ss < 0 {
		return bad()
	}
	ns := 0
	if frac != "" {
		if len(frac) > 9 {
			frac = frac[:9]
		}
		n, err := strconv.Atoi(frac)
		if err != nil {
			return bad()
		}
		for k := len(frac); k < 9; k++ {
			n *= 10
		}
		ns = n
	}
	if hasOff {
		t := time.Date(y, time.Month(mo), d, hh, mm, ss, ns, time.UTC)
		return t.Add(-time.Duration(off) * time.Second), nil
	}
	if loc == nil {
		loc = time.Local
	}
	return time.Date(y, time.Month(mo), d, hh, mm, ss, ns, loc).UTC(), nil
}

// splitCSV splits a record on commas honouring double quotes ("" escapes a quote).
func splitCSV(s string) ([]string, error) {
	out := make([]string, 0, 40)
	for pos := 0; ; {
		if pos < len(s) && s[pos] == '"' {
			var b strings.Builder
			pos++
			closed := false
			for pos < len(s) {
				c := s[pos]
				if c == '"' {
					if pos+1 < len(s) && s[pos+1] == '"' {
						b.WriteByte('"')
						pos += 2
						continue
					}
					pos++
					closed = true
					break
				}
				b.WriteByte(c)
				pos++
			}
			if !closed {
				return nil, fmt.Errorf("%w: unterminated quote", ErrBadLine)
			}
			out = append(out, b.String())
			if pos >= len(s) {
				return out, nil
			}
			if s[pos] != ',' {
				return nil, fmt.Errorf("%w: garbage after quote", ErrBadLine)
			}
			pos++
			continue
		}
		next := strings.IndexByte(s[pos:], ',')
		if next < 0 {
			return append(out, s[pos:]), nil
		}
		out = append(out, s[pos:pos+next])
		pos += next + 1
	}
}
