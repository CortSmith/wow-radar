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

// ArmoryStore is the OPTIONAL armory data layer. A store that holds armory data implements it in
// addition to Store; hosts discover it with a type assertion and answer 404 for armory routes when
// it is absent. It is deliberately not part of Store. Errors wrap domain.ErrInvalid or domain.ErrNotFound.
type ArmoryStore interface {
	// ApplyArmoryBatch upserts a batch (newest FetchedAt wins per character) and keeps one snapshot
	// per character per UTC day. Replaying a BatchID is a no-op reported as Duplicate.
	ApplyArmoryBatch(ctx context.Context, batch domain.ArmoryBatch) (domain.ArmoryApplyResult, error)
	// GetArmoryCharacter returns one character, or domain.ErrNotFound.
	GetArmoryCharacter(ctx context.Context, realm, name string) (domain.ArmoryCharacter, error)
	// ListArmoryCharacters returns the known characters among names (at most domain.ArmoryBatchMax),
	// in request order; unknown names are omitted.
	ListArmoryCharacters(ctx context.Context, realm string, names []string) ([]domain.ArmoryCharacter, error)
	// ArmorySnapshots returns a character's daily history, oldest day first.
	ArmorySnapshots(ctx context.Context, realm, name string) ([]domain.ArmorySnapshot, error)
	// PruneArmorySnapshots deletes snapshots of days before olderThanDay and returns how many.
	PruneArmorySnapshots(ctx context.Context, olderThanDay int64) (int, error)
	// RecordArmoryRun inserts or replaces a run summary by id.
	RecordArmoryRun(ctx context.Context, run domain.ArmoryRun) error
	// LastArmoryRun returns the run with the latest StartedAt, or domain.ErrNotFound.
	LastArmoryRun(ctx context.Context) (domain.ArmoryRun, error)
}

// ArmorySeedStore is the OPTIONAL crawl-seed feed. The armory API cannot list characters, so the
// sync job seeds from what the store already holds: player contacts of every ruleset (de-duplicated
// by realm and name) followed by the distinct guilds of stored armory characters. Order is
// deterministic with keyset pagination: cursor is opaque ("" starts) and the page's Next is empty
// when done. A bad cursor or a limit outside 1..domain.ArmorySeedLimitMax wraps domain.ErrInvalid.
type ArmorySeedStore interface {
	ArmorySeeds(ctx context.Context, cursor string, limit int) (domain.ArmorySeedPage, error)
}
