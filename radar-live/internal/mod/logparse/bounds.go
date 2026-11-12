package logparse

import (
	"fmt"
	"math"
	"time"

	"radarlive/internal/domain"
	"radarlive/internal/validate"
)

// ErrOutOfBounds marks an event whose values are outside what the game can produce (or whose time is
// outside the accepted window). It also matches ErrBadLine, so callers count it as a rejected line.
var ErrOutOfBounds = fmt.Errorf("%w: value out of bounds", ErrBadLine)

// Bounds on parsed values. Coordinates, map ids, levels and hit points share the ping limits.
const (
	// DefaultWindow is how far from the clock an event time may be, in both directions.
	DefaultWindow = 24 * time.Hour
	// MaxRecordNameLen bounds the record name ("SPELL_DAMAGE").
	MaxRecordNameLen = 64
	// MaxGUIDLen bounds every GUID text.
	MaxGUIDLen = 64
	// MaxAmount bounds one damage or heal amount.
	MaxAmount = domain.MaxHPLimit
)

// Bounds configure CheckEvent.
type Bounds struct {
	// Window is the accepted distance of an event time from now (default DefaultWindow).
	Window time.Duration
	// AllowReplay accepts events older than the window (importing old logs); they must still be after
	// domain.MinTimestampMs, and the future stays limited to Window.
	AllowReplay bool
}

func (b Bounds) window() time.Duration {
	if b.Window <= 0 {
		return DefaultWindow
	}
	return b.Window
}

// CheckEvent reports whether ev is sane: time inside the window, finite and bounded numbers, short
// identifiers. Nothing is repaired; a failing event must be dropped and counted as rejected.
func CheckEvent(ev domain.Event, now time.Time, b Bounds) error {
	if err := checkTime(ev.TimeMs, now, b); err != nil {
		return err
	}
	if len(ev.Name) > MaxRecordNameLen {
		return fmt.Errorf("%w: record name too long", ErrOutOfBounds)
	}
	for _, u := range []domain.EventUnit{ev.Src, ev.Dst} {
		if len(u.GUID) > MaxGUIDLen {
			return fmt.Errorf("%w: guid too long", ErrOutOfBounds)
		}
	}
	if ev.HasAmount && !within(ev.Amount, 0, MaxAmount) {
		return fmt.Errorf("%w: amount", ErrOutOfBounds)
	}
	if in := ev.Info; in != nil {
		return checkInfo(in)
	}
	return nil
}

func checkTime(ms int64, now time.Time, b Bounds) error {
	lo, hi := now.Add(-b.window()).UnixMilli(), now.Add(b.window()).UnixMilli()
	if b.AllowReplay {
		lo = domain.MinTimestampMs
	}
	if ms < lo || ms > hi {
		return fmt.Errorf("%w: time outside the accepted window", ErrOutOfBounds)
	}
	return nil
}

func checkInfo(in *domain.EventInfo) error {
	if len(in.GUID) > MaxGUIDLen || len(in.Owner) > MaxGUIDLen {
		return fmt.Errorf("%w: guid too long", ErrOutOfBounds)
	}
	if in.HasHP && !(within(in.HP, 0, domain.MaxHPLimit) && within(in.MaxHP, 0, domain.MaxHPLimit)) {
		return fmt.Errorf("%w: hit points", ErrOutOfBounds)
	}
	if in.HasPos && !(within(in.X, -domain.CoordMax, domain.CoordMax) && within(in.Y, -domain.CoordMax, domain.CoordMax) &&
		in.MapID >= 0 && in.MapID <= domain.MaxMapID) {
		return fmt.Errorf("%w: position", ErrOutOfBounds)
	}
	if in.Level < 0 || in.Level > domain.PingLevelMax {
		return fmt.Errorf("%w: level", ErrOutOfBounds)
	}
	return nil
}

func within(v, lo, hi float64) bool { return !math.IsNaN(v) && v >= lo && v <= hi }

// SanitizeEvent repairs the free text of ev in place: control characters, newlines, bidi and
// zero-width characters are removed and names are cut to the ping caps, so a poisoned log cannot
// carry them into a ping.
func SanitizeEvent(ev *domain.Event) {
	ev.Src.Raw, _ = validate.CleanText(ev.Src.Raw, domain.ZoneMaxRunes)
	ev.Dst.Raw, _ = validate.CleanText(ev.Dst.Raw, domain.ZoneMaxRunes)
	ev.Zone, _ = validate.CleanText(ev.Zone, domain.ZoneMaxRunes)
	if ev.Chart != nil {
		c := *ev.Chart
		c.Name, _ = validate.CleanText(c.Name, domain.ZoneMaxRunes)
		ev.Chart = &c
	}
}
