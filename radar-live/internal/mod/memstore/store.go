// Package memstore is the in-memory core.Store. It is the reference behaviour that sqlstore must
// match (see internal/mod/storetest) and is also handy for development and tests.
package memstore

import (
	"context"
	"fmt"
	"sync"
	"time"

	"radarlive/internal/domain"
)

// Memory bounds per ruleset: how many batch ids and death ids are remembered for deduplication.
// The oldest are forgotten first, so a replay older than this window is applied again.
const (
	batchMemoryMax = 10_000
	deathMemoryMax = 50_000
)

// record is one contact with its trail.
type record struct {
	contact domain.Contact
	trail   []domain.Fix
}

// partition holds everything for one ruleset.
type partition struct {
	contacts  map[string]*record
	charts    map[int]domain.Chart
	batches   *boundedSet
	deaths    *boundedSet
	lastWrite int64
}

func newPartition() *partition {
	return &partition{
		contacts: map[string]*record{},
		charts:   map[int]domain.Chart{},
		batches:  newBoundedSet(batchMemoryMax),
		deaths:   newBoundedSet(deathMemoryMax),
	}
}

// hasData reports whether the partition holds anything worth listing in Status.
func (p *partition) hasData() bool {
	return len(p.contacts) > 0 || len(p.charts) > 0 || p.deaths.len() > 0
}

// Store implements core.Store in memory. It is safe for concurrent use.
type Store struct {
	now        func() time.Time
	mu         sync.RWMutex
	partitions map[domain.Ruleset]*partition
}

// New creates an empty store. now supplies the clock; nil means time.Now.
func New(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{now: now, partitions: map[domain.Ruleset]*partition{}}
}

// Close implements core.Store; there is nothing to release.
func (s *Store) Close() error { return nil }

// nowMs is the store clock in epoch milliseconds.
func (s *Store) nowMs() int64 { return s.now().UnixMilli() }

// prepare checks the context and ruleset shared by every call.
func prepare(ctx context.Context, rs domain.Ruleset) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("memstore: %w", err)
	}
	return rs.Validate()
}

// partitionFor returns the partition of rs, creating it when create is set (callers hold the write lock).
func (s *Store) partitionFor(rs domain.Ruleset, create bool) *partition {
	p := s.partitions[rs]
	if p == nil && create {
		p = newPartition()
		s.partitions[rs] = p
	}
	return p
}
