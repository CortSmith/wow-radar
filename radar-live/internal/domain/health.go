package domain

import "errors"

// Errors a Store returns from ApplyPing when it deliberately refuses to write; the write API maps
// them to HTTP 507 and 503. Reads keep working in both states.
var (
	// ErrStorageFull marks a refused write because the database reached its size cap or the disk is nearly full.
	// It clears by itself when space returns.
	ErrStorageFull = errors.New("storage full")
	// ErrDegraded marks a refused write because an integrity check failed. It clears only by a restart
	// after the operator restored a good backup.
	ErrDegraded = errors.New("store degraded: integrity check failed")
)

// Health values of Health.Integrity.
const (
	IntegrityOK       = "ok"
	IntegrityDegraded = "degraded"
)

// Health is the data-integrity summary a store may expose (optional interface `Health() Health`).
type Health struct {
	// Integrity is IntegrityOK or IntegrityDegraded.
	Integrity string `json:"integrity"`
	// LastCheck is the epoch milliseconds of the last integrity check (0 = never).
	LastCheck int64 `json:"lastCheck"`
	// Full reports that writes are being refused for lack of space.
	Full bool `json:"full"`
}
