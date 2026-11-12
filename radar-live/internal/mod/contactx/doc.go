// Package contactx turns the events of one log source into per-player contact deltas.
//
// An Extractor tracks exact fixes (a unit's own advanced-log position), approximate fixes (units named
// within 120 s of the log owner's last fix borrow that position), level, max HP, HP samples, past names
// (at most 10), event and damage counters, and chart rectangles from MAP_CHANGE. Flush returns the
// changes since the previous Flush as domain.PingContact values (additive deltas plus new trail points)
// and resets them, so flushing twice never double counts. Memory is bounded: the contact count is capped
// (least recently seen are evicted), pending fixes are capped and idle contacts are pruned.
//
// The module registers a core.Parser that accepts domain.Event values (produced by logparse), keeps one
// Extractor per Event.Source, and publishes pings to the Sinks socket on a timer. It imports only core
// and domain.
package contactx
