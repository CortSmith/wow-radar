package bountyx

import (
	"radarlive/internal/domain"
)

// death handles a death signal. killer is non-nil for PARTY_KILL and SPELL_INSTAKILL, which name it.
func (a *Analyzer) death(ev domain.Event, killer *domain.EventUnit) {
	victim := ev.Dst
	if !isFriendlyPlayer(victim) {
		return
	}
	if lastMs, ok := a.last[victim.GUID]; ok && abs(ev.TimeMs-lastMs) <= DuplicateWindowMs {
		a.stats.Duplicates++
		return
	}
	if len(a.last) >= maxLastDeaths {
		clear(a.last)
	}
	a.last[victim.GUID] = ev.TimeMs

	hits := a.takeHits(victim.GUID, ev.TimeMs)
	var credited string
	friendlyKill := false
	if killer != nil {
		credited, friendlyKill = a.creditOf(*killer)
	} else {
		credited, friendlyKill = killingBlow(hits, ev.TimeMs)
	}
	d := domain.Death{ID: deathID(victim.GUID, ev.TimeMs, ev.Source), T: ev.TimeMs, Victim: victim.GUID}
	if !friendlyKill {
		d.Killer = credited
		d.Assists = assistsOf(hits, credited)
	}
	a.record(d, ev.TimeMs)
}

// takeHits returns the victim's hits inside the assist window and clears the victim's window.
func (a *Analyzer) takeHits(victim string, deathMs int64) []hit {
	w := a.wins[victim]
	if w == nil {
		return nil
	}
	var hits []hit
	for _, h := range w.hits {
		if age := deathMs - h.tMs; age >= 0 && age <= AssistWindowMs {
			hits = append(hits, h)
		}
	}
	delete(a.wins, victim)
	return hits
}

// killingBlow picks the source of the newest hit when it landed within the killing blow window.
func killingBlow(hits []hit, deathMs int64) (credit string, friend bool) {
	if len(hits) == 0 {
		return "", false
	}
	last := hits[0]
	for _, h := range hits {
		if h.tMs >= last.tMs { // ties keep the later line
			last = h
		}
	}
	if deathMs-last.tMs > KillingBlowWindowMs {
		return "", false
	}
	return last.credit, last.friend
}

// assistsOf lists the distinct hostile players in hits other than the killer, in order of first hit.
func assistsOf(hits []hit, killer string) []string {
	seen := map[string]bool{killer: true, "": true}
	var out []string
	for _, h := range hits {
		if !seen[h.credit] {
			seen[h.credit] = true
			out = append(out, h.credit)
		}
	}
	return out
}

// record stores the death and its credits for the next Flush.
func (a *Analyzer) record(d domain.Death, tMs int64) {
	a.stats.Deaths++
	if d.Killer == "" && len(d.Assists) == 0 {
		a.stats.NPCDeaths++
	}
	if len(a.deaths) >= domain.PingMaxDeaths {
		a.deaths = append(a.deaths[:0], a.deaths[1:]...)
	}
	a.deaths = append(a.deaths, d)
	if d.Killer != "" {
		a.addCredit(d.Killer, tMs, true)
	}
	for _, guid := range d.Assists {
		a.addCredit(guid, tMs, false)
	}
}

// addCredit books one killing blow or assist for guid. A flush carries at most maxCredits distinct
// players: a hostile log with endless made-up killers cannot grow the table, the surplus is counted.
func (a *Analyzer) addCredit(guid string, tMs int64, killingBlow bool) {
	if _, known := a.credits[guid]; !known && len(a.credits) >= maxCredits {
		a.stats.DroppedCredits++
		return
	}
	c := a.credit(guid)
	if killingBlow {
		c.KB++
	} else {
		c.Assists++
	}
	c.LastMs = max(c.LastMs, tMs)
	a.credits[guid] = c
}

// credit returns the pending credit of guid with its names filled in.
func (a *Analyzer) credit(guid string) Credit {
	c := a.credits[guid]
	if raw := a.known[guid].raw; raw != "" {
		c.Raw, c.Name = raw, a.namer.Display(raw)
	}
	return c
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
