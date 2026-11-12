// Package logparse turns lines of the WoW advanced combat log into domain.Events.
//
// Verified layout: f[0]=record, f[1..4]=source guid,name,flags,raidflags, f[5..8]=destination, then
// the event prefix params (SWING_*: 0, SPELL_*/RANGE_*/DAMAGE_*: 3, ENVIRONMENTAL_*: 1), then - when
// advanced logging is on - a 19 field block describing the "info unit" (the source or the
// destination), then the suffix params (damage: amount, base, overkill, ...).
//
//	advanced block: 0 guid, 1 owner, 2 hp, 3 maxhp, 4..13 stats and power, 14 X, 15 Y, 16 map, 17 facing, 18 level
//
// Timestamps are "M/D/YYYY HH:MM:SS.mmm[+-H[:MM]]" and become UTC epoch milliseconds; the line's own
// offset wins over Options.FallbackZone. The package imports only core and domain; its module feeds
// parsed events to the Parsers socket so contactx and bountyx (which never import it) receive them.
package logparse
