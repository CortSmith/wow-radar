// Package bountyx turns friendly player deaths into bounty credits (killing blows and assists).
//
// Victims are friendly "Player-" units. A death is signalled by UNIT_DIED (ignored when the unconscious
// flag marks a feign death), PARTY_KILL or SPELL_INSTAKILL; signals for one victim within 3 s are one
// death. The killing blow is the PARTY_KILL source, or the source of the last damage at most 5 s before
// the death. Assists (worth no points) go to the other hostile players that hit the victim in the
// previous 15 s. Pets, guardians and totems credit their owner (from the advanced block owner GUID or
// SPELL_SUMMON). A death without hostile player credit is counted as an NPC death. Memory is bounded
// (64 hits per victim, 256 victims).
//
// Flush returns the deaths (with stable ids so the store can deduplicate) and the kb/assist deltas keyed
// by player GUID. The module registers a core.Parser for domain.Event values, keeps one Analyzer per
// Event.Source and publishes pings to the Sinks socket on a timer. It imports only core and domain.
package bountyx
