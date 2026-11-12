package memstore

import (
	"context"

	"radarlive/internal/domain"
)

// ApplyPing merges one batch into its ruleset. A batch id seen before is a no-op reported as
// Duplicate. Changed counts contacts whose data or trail changed plus charts added or recalibrated.
func (s *Store) ApplyPing(ctx context.Context, ping domain.Ping) (domain.ApplyResult, error) {
	if err := prepare(ctx, ping.RS); err != nil {
		return domain.ApplyResult{}, err
	}
	if err := ping.Validate(); err != nil {
		return domain.ApplyResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	part := s.partitionFor(ping.RS, true)
	result := domain.ApplyResult{Received: len(ping.Contacts)}
	if !part.batches.add(ping.BatchID) {
		result.Duplicate = true
		return result, nil
	}
	for _, in := range ping.Contacts {
		if part.applyContact(in) {
			result.Changed++
		}
	}
	result.Changed += part.applyCharts(ping.Charts)
	part.recordDeaths(ping.Deaths)
	if result.Changed > 0 {
		part.lastWrite = s.nowMs()
	}
	return result, nil
}

// applyContact merges one observation and its fixes; it reports whether anything changed.
func (p *partition) applyContact(in domain.PingContact) bool {
	rec, existed := p.contacts[in.GUID]
	if !existed {
		rec = &record{}
		p.contacts[in.GUID] = rec
	}
	var stored *domain.Contact
	if existed {
		stored = &rec.contact
	}
	merged := domain.MergeContact(stored, in)
	changed := !existed || !merged.Equal(rec.contact)
	rec.contact = merged
	return p.appendFixes(rec, in.Fixes) || changed
}

// appendFixes extends the trail of rec and reports whether it changed.
func (p *partition) appendFixes(rec *record, fixes []domain.Fix) bool {
	before := len(rec.trail)
	var lastBefore domain.Fix
	if before > 0 {
		lastBefore = rec.trail[before-1]
	}
	rec.trail = domain.AppendFixes(rec.trail, fixes)
	after := len(rec.trail)
	return after != before || (after > 0 && rec.trail[after-1] != lastBefore)
}

// applyCharts stores new or recalibrated charts and returns how many changed.
func (p *partition) applyCharts(charts []domain.Chart) int {
	changed := 0
	for _, c := range charts {
		if old, ok := p.charts[c.MapID]; !ok || old != c {
			p.charts[c.MapID] = c
			changed++
		}
	}
	return changed
}

// recordDeaths remembers death ids so the same death is never counted twice.
func (p *partition) recordDeaths(deaths []domain.Death) {
	for _, d := range deaths {
		p.deaths.add(d.ID)
	}
}
