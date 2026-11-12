package core

import (
	"context"

	"radarlive/internal/domain"
)

// Store is the data layer consumed by the read and write APIs. memstore is the reference
// implementation; sqlstore must behave identically (see internal/mod/storetest).
//
// Errors wrap domain.ErrInvalid for bad input and domain.ErrNotFound for missing rows.
type Store interface {
	// ApplyPing merges one batch. Replaying a BatchID is a no-op reported as Duplicate.
	ApplyPing(ctx context.Context, ping domain.Ping) (domain.ApplyResult, error)
	// Sweep answers one poll for a sector.
	Sweep(ctx context.Context, sector domain.Sector) (domain.SweepResult, error)
	// Search finds contacts by text, chart and group, newest first.
	Search(ctx context.Context, query domain.SearchQuery) (domain.SearchResult, error)
	// Trail returns one contact's fixes, optionally for a single chart (nil mapID means all).
	Trail(ctx context.Context, rs domain.Ruleset, guid string, mapID *int) ([]domain.Fix, error)
	// Bounty returns the ranking, capped at limit (non-positive means the default).
	Bounty(ctx context.Context, rs domain.Ruleset, limit int) ([]domain.BountyRow, error)
	// BountyPlayer returns one contact's row with its rank, or domain.ErrNotFound.
	BountyPlayer(ctx context.Context, rs domain.Ruleset, guid string) (domain.BountyRow, error)
	// Charts lists the map rectangles seen in a ruleset, ordered by map id.
	Charts(ctx context.Context, rs domain.Ruleset) ([]domain.Chart, error)
	// Status summarises every ruleset that holds data.
	Status(ctx context.Context) ([]domain.RulesetStatus, error)
	// Close releases the store's resources.
	Close() error
}
