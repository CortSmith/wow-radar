package memstore

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sync"

	"radarlive/internal/domain"
)

// armoryKey identifies a character: case-folded realm and name.
type armoryKey struct{ realm, name string }

func keyOf(realm, name string) armoryKey {
	return armoryKey{domain.ArmoryRealmKey(realm), domain.ArmoryNameKey(name)}
}

// armoryRecord is one character with its daily snapshots (keyed by UTC day).
type armoryRecord struct {
	char      domain.ArmoryCharacter
	snapshots map[int64]domain.ArmorySnapshot
}

// armoryData is the armory half of the store; its zero value is ready to use.
type armoryData struct {
	mu      sync.Mutex
	chars   map[armoryKey]*armoryRecord
	batches *boundedSet
	runs    map[string]domain.ArmoryRun
}

func (a *armoryData) init() {
	if a.chars == nil {
		a.chars = map[armoryKey]*armoryRecord{}
		a.batches = newBoundedSet(batchMemoryMax)
		a.runs = map[string]domain.ArmoryRun{}
	}
}

// cloneChar copies the JSON blobs so callers never alias stored memory.
func cloneChar(c domain.ArmoryCharacter) domain.ArmoryCharacter {
	c.Gear, c.Talents = bytes.Clone(c.Gear), bytes.Clone(c.Talents)
	c.Stats, c.PvP = bytes.Clone(c.Stats), bytes.Clone(c.PvP)
	if c.APIUpdatedAt != nil {
		v := *c.APIUpdatedAt
		c.APIUpdatedAt = &v
	}
	return c
}

// ApplyArmoryBatch implements core.ArmoryStore. The whole batch is validated before anything is
// stored. A character older than the stored fetch is Skipped.
func (s *Store) ApplyArmoryBatch(ctx context.Context, batch domain.ArmoryBatch) (domain.ArmoryApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.ArmoryApplyResult{}, fmt.Errorf("memstore: %w", err)
	}
	if err := batch.Validate(); err != nil {
		return domain.ArmoryApplyResult{}, err
	}
	a := &s.armory
	a.mu.Lock()
	defer a.mu.Unlock()
	a.init()
	if !a.batches.add(batch.BatchID) {
		return domain.ArmoryApplyResult{Duplicate: true}, nil
	}
	var res domain.ArmoryApplyResult
	for _, c := range batch.Characters {
		if a.upsert(c) {
			res.Stored++
		} else {
			res.Skipped++
		}
	}
	return res, nil
}

// upsert applies one character and reports whether it was stored (false: an older fetch).
//
// A non-ok status on an existing character updates only Status and FetchedAt, so a transient
// error or a hidden profile does not erase the last good details. Only ok fetches make snapshots.
func (a *armoryData) upsert(c domain.ArmoryCharacter) bool {
	key := keyOf(c.Realm, c.Name)
	rec := a.chars[key]
	if rec == nil {
		rec = &armoryRecord{snapshots: map[int64]domain.ArmorySnapshot{}}
		a.chars[key] = rec
	} else if c.FetchedAt < rec.char.FetchedAt {
		return false
	} else if c.Status != domain.ArmoryStatusOK {
		rec.char.Status, rec.char.FetchedAt = c.Status, c.FetchedAt
		return true
	}
	rec.char = cloneChar(c)
	if c.Status == domain.ArmoryStatusOK {
		rec.snapshots[domain.ArmoryDay(c.FetchedAt)] = domain.ArmorySnapshot{
			Day: domain.ArmoryDay(c.FetchedAt), Level: c.Level, GearHash: domain.GearHash(c.Gear), PvP: bytes.Clone(c.PvP),
		}
	}
	return true
}

// GetArmoryCharacter implements core.ArmoryStore.
func (s *Store) GetArmoryCharacter(ctx context.Context, realm, name string) (domain.ArmoryCharacter, error) {
	if err := ctx.Err(); err != nil {
		return domain.ArmoryCharacter{}, fmt.Errorf("memstore: %w", err)
	}
	a := &s.armory
	a.mu.Lock()
	defer a.mu.Unlock()
	rec := a.chars[keyOf(realm, name)]
	if rec == nil {
		return domain.ArmoryCharacter{}, fmt.Errorf("%w: armory character", domain.ErrNotFound)
	}
	return cloneChar(rec.char), nil
}

// ListArmoryCharacters implements core.ArmoryStore.
func (s *Store) ListArmoryCharacters(ctx context.Context, realm string, names []string) ([]domain.ArmoryCharacter, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("memstore: %w", err)
	}
	if len(names) > domain.ArmoryBatchMax {
		return nil, fmt.Errorf("%w: more than %d names", domain.ErrInvalid, domain.ArmoryBatchMax)
	}
	a := &s.armory
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]domain.ArmoryCharacter, 0, len(names))
	for _, n := range names {
		if rec := a.chars[keyOf(realm, n)]; rec != nil {
			out = append(out, cloneChar(rec.char))
		}
	}
	return out, nil
}

// ArmorySnapshots implements core.ArmoryStore; an unknown character has no snapshots.
func (s *Store) ArmorySnapshots(ctx context.Context, realm, name string) ([]domain.ArmorySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("memstore: %w", err)
	}
	a := &s.armory
	a.mu.Lock()
	defer a.mu.Unlock()
	rec := a.chars[keyOf(realm, name)]
	if rec == nil {
		return nil, nil
	}
	out := make([]domain.ArmorySnapshot, 0, len(rec.snapshots))
	for _, snap := range rec.snapshots {
		snap.PvP = bytes.Clone(snap.PvP)
		out = append(out, snap)
	}
	slices.SortFunc(out, func(x, y domain.ArmorySnapshot) int { return int(x.Day - y.Day) })
	return out, nil
}

// PruneArmorySnapshots implements core.ArmoryStore.
func (s *Store) PruneArmorySnapshots(ctx context.Context, olderThanDay int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("memstore: %w", err)
	}
	a := &s.armory
	a.mu.Lock()
	defer a.mu.Unlock()
	removed := 0
	for _, rec := range a.chars {
		for day := range rec.snapshots {
			if day < olderThanDay {
				delete(rec.snapshots, day)
				removed++
			}
		}
	}
	return removed, nil
}

// RecordArmoryRun implements core.ArmoryStore.
func (s *Store) RecordArmoryRun(ctx context.Context, run domain.ArmoryRun) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("memstore: %w", err)
	}
	if err := run.Validate(); err != nil {
		return err
	}
	a := &s.armory
	a.mu.Lock()
	defer a.mu.Unlock()
	a.init()
	a.runs[run.ID] = run
	return nil
}

// LastArmoryRun implements core.ArmoryStore.
func (s *Store) LastArmoryRun(ctx context.Context) (domain.ArmoryRun, error) {
	if err := ctx.Err(); err != nil {
		return domain.ArmoryRun{}, fmt.Errorf("memstore: %w", err)
	}
	a := &s.armory
	a.mu.Lock()
	defer a.mu.Unlock()
	var last domain.ArmoryRun
	found := false
	for _, r := range a.runs {
		if !found || r.StartedAt > last.StartedAt {
			last, found = r, true
		}
	}
	if !found {
		return domain.ArmoryRun{}, fmt.Errorf("%w: armory run", domain.ErrNotFound)
	}
	return last, nil
}
