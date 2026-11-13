// Package armorysync crawls the armory and stores the characters through a Sink in batches. The armory has
// no "list all characters" endpoint, so the crawl is seeded from radar-db (a SeedSource): character seeds
// are fetched directly, guild seeds are expanded into their roster members through the Source. It is
// resumable (a checkpoint after every unit of work), bounded (wall-clock and request budgets, a worker
// pool) and idempotent (deterministic batch ids). It knows nothing about HTTP; cmd/radar-armory wires the
// real client and DB API.
//
// The Source type is declared here, not imported from armoryclient, because modules may not import each
// other; the wiring layer adapts armoryclient's client to Source.
package armorysync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"radarlive/internal/domain"
)

// CharRef identifies one character on the armory.
type CharRef struct{ Realm, Name string }

// Source reads the armory. Hidden and not-found characters are returned with the matching Status, not as
// errors. A guild without a roster is an empty list, not an error.
type Source interface {
	GuildRoster(ctx context.Context, realm, guild string) ([]CharRef, error)
	Character(ctx context.Context, realm, name string) (domain.ArmoryCharacter, error)
}

// SeedSource supplies the crawl seeds page by page (radar-db's GET /internal/v1/armory/seeds in
// production). The cursor is opaque: "" starts, an empty Next ends the feed.
type SeedSource interface {
	Seeds(ctx context.Context, cursor string, limit int) (domain.ArmorySeedPage, error)
}

// Sink stores batches and run checkpoints (the DB API in production).
type Sink interface {
	ApplyBatch(ctx context.Context, b domain.ArmoryBatch) (domain.ArmoryApplyResult, error)
	RecordRun(ctx context.Context, r domain.ArmoryRun) error
	LastRun(ctx context.Context) (domain.ArmoryRun, bool, error)
}

// Clock supplies the current time.
type Clock interface{ Now() time.Time }

// Skipper is an optional delta hook: a true answer skips the detail fetch of a character.
type Skipper interface {
	Skip(ctx context.Context, ref CharRef) bool
}

// Final run statuses. Interrupted is a Summary status only: it is persisted as an unfinished "running"
// run so the same local day can resume it (the domain has no interrupted status).
const (
	StatusOK          = "ok"
	StatusPartial     = "partial"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
)

// ErrFatal wraps every error that ends a run as failed (auth, config); it maps to exit code 2.
var ErrFatal = errors.New("armorysync: fatal")

// ErrAuth is the armory refusing the key; Config.AuthErr may name the client's own sentinel as well.
var ErrAuth = errors.New("armorysync: the armory rejected the API key")

// authEnd ends the run on an auth refusal, keeping the client's reason (which step refused) when known.
func authEnd(cause error) *end {
	if cause == nil || errors.Is(cause, ErrAuth) {
		return &end{status: StatusFailed, err: ErrAuth}
	}
	return &end{status: StatusFailed, err: fmt.Errorf("%w: %w", ErrAuth, cause)}
}

// Summary is the outcome of one Run. The counters are cumulative for a resumed run; Duration covers
// this invocation only. Note holds a short, secret-free reason when the run ended early.
type Summary struct {
	RunID      string
	Status     string
	Enumerated int
	Fetched    int
	Stored     int
	Skipped    int
	Failed     int
	Duration   time.Duration
	Cursor     string
	Note       string
}

// ExitCode maps the status to a process exit code: 0 ok, 1 partial or interrupted, 2 failed or unknown.
func (s Summary) ExitCode() int {
	switch s.Status {
	case StatusOK:
		return 0
	case StatusPartial, StatusInterrupted:
		return 1
	}
	return 2
}

// Config holds the knobs; zero values take the defaults noted.
type Config struct {
	MaxDuration time.Duration  // wall-clock budget per Run (default 6h)
	MaxRequests int            // request budget per Run: character and roster fetches (0 = unlimited)
	Concurrency int            // parallel detail fetches (default 3)
	BatchSize   int            // characters per batch, at most 100 (default 100)
	Attempts    int            // tries per sink call (default 4)
	Backoff     time.Duration  // first retry delay, doubled each time (default 2s)
	Location    *time.Location // defines "the same day" for resuming (default time.Local)
	AuthErr     error          // extra sentinel that means "key rejected"
	InvalidErr  error          // sentinel for a seed the source refuses as malformed: skipped and counted, not failed
	Skipper     Skipper        // optional delta hook

	DefaultRealm    string // realm (slug form) for seeds without one; unset: such seeds are skipped and counted
	SeedPageSize    int    // seeds per page (default 200, at most 500)
	MaxGuildMembers int    // roster members crawled per guild (default 1000)

	Sleep func(ctx context.Context, d time.Duration) error
}

func (c Config) withDefaults() Config {
	if c.MaxDuration <= 0 {
		c.MaxDuration = 6 * time.Hour
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 3
	}
	if c.BatchSize <= 0 || c.BatchSize > domain.ArmoryBatchMax {
		c.BatchSize = domain.ArmoryBatchMax
	}
	if c.Attempts <= 0 {
		c.Attempts = 4
	}
	if c.Backoff <= 0 {
		c.Backoff = 2 * time.Second
	}
	if c.SeedPageSize <= 0 {
		c.SeedPageSize = domain.ArmorySeedLimitDefault
	}
	c.SeedPageSize = min(c.SeedPageSize, domain.ArmorySeedLimitMax)
	if c.MaxGuildMembers <= 0 {
		c.MaxGuildMembers = 1000
	}
	if c.Location == nil {
		c.Location = time.Local
	}
	if c.Sleep == nil {
		c.Sleep = sleepCtx
	}
	return c
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
