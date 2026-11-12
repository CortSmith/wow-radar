package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"radarlive/internal/domain"
)

// Defaults of the safety limits.
const (
	DefaultMaxDBMB   = 2048
	DefaultMinFreeMB = 200
	// spaceCheckEvery throttles the size and free-space probes on the write path.
	spaceCheckEvery = time.Second
	mib             = 1 << 20
)

// Errors that make Open refuse a database file. The file is never modified, moved or deleted.
var (
	// ErrNotDatabase marks a file that is not a SQLite database.
	ErrNotDatabase = errors.New("not a SQLite database")
	// ErrCorrupt marks a database that failed an integrity check.
	ErrCorrupt = errors.New("database is corrupt")
	// ErrNewerSchema marks a database written by a newer radar-live.
	ErrNewerSchema = errors.New("database schema is newer than this build")
	// ErrLocked marks a database that another radar-db process holds.
	ErrLocked = errors.New("database is locked by another radar-db process")
)

// restoreHelp is the operator instruction attached to every refusal and to the degraded log line.
func restoreHelp(path string) string {
	return "the database file " + path + " was left untouched (nothing was moved or deleted). To recover: stop radar-db, " +
		"run `radar-db restore <backup-file>` with a backup made by `radar-db backup` (see the Backups section of " +
		"deploy/README-xubuntu.md), then start radar-db again. Keep the damaged file for inspection"
}

// notify reports a state change to the optional OnEvent hook.
func (s *Store) notify(event, reason string) {
	if s.onEvent != nil {
		s.onEvent(event, reason)
	}
}

// spaceKind classifies a spaceProblem text: free disk space below the minimum, or the size cap.
func spaceKind(reason string) string {
	if strings.HasPrefix(reason, "free disk") {
		return "disk_low"
	}
	return "size_cap"
}

// Health reports the integrity state, the time of the last check and whether writes are paused for lack of space.
func (s *Store) Health() domain.Health {
	h := domain.Health{Integrity: domain.IntegrityOK, LastCheck: s.lastCheck.Load(), Full: s.full.Load()}
	if s.degraded.Load() {
		h.Integrity = domain.IntegrityDegraded
	}
	return h
}

// writeGate refuses a write when the store is degraded or out of space.
func (s *Store) writeGate(ctx context.Context) error {
	if s.degraded.Load() {
		return fmt.Errorf("sqlstore: %w", domain.ErrDegraded)
	}
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	now := s.now()
	if s.checkedOnce && now.Sub(s.lastSpace) < spaceCheckEvery && !now.Before(s.lastSpace) {
		return s.fullErr()
	}
	s.checkedOnce, s.lastSpace = true, now
	reason := s.spaceProblem(ctx)
	switch was := s.full.Load(); {
	case reason != "" && !was:
		s.full.Store(true)
		s.fullReason = reason
		s.notify("disk_full", spaceKind(reason))
		s.log.Error("write path paused: "+reason+"; pings get 507, reads continue, writes resume when space returns",
			slog.String("path", s.path))
	case reason != "":
		s.fullReason = reason
	case was:
		s.full.Store(false)
		s.log.Info("storage recovered; accepting pings again", slog.String("path", s.path))
	}
	return s.fullErr()
}

func (s *Store) fullErr() error {
	if s.full.Load() {
		return fmt.Errorf("sqlstore: %s: %w", s.fullReason, domain.ErrStorageFull)
	}
	return nil
}

// spaceProblem returns why writes must pause, or "". Called with gateMu held.
func (s *Store) spaceProblem(ctx context.Context) string {
	if s.diskFree != nil {
		free, err := s.diskFree(filepath.Dir(s.path))
		if err != nil {
			s.log.Debug("free-space probe failed", slog.Any("err", err))
		} else if int64(min(free, 1<<62)) < s.minFreeBytes {
			return fmt.Sprintf("free disk space %d MiB is below the minimum %d MiB", free/mib, s.minFreeBytes/mib)
		}
	}
	limit := s.maxDBBytes.Load()
	over, size := s.overCap(ctx, limit)
	if over && !s.full.Load() {
		s.reclaim(ctx, limit)
		over, size = s.overCap(ctx, limit)
	}
	if over {
		return fmt.Sprintf("database size %d MiB exceeds the maximum %d MiB", size/mib, limit/mib)
	}
	return ""
}

// overCap reports whether the data (live pages) or the WAL exceed limit.
func (s *Store) overCap(ctx context.Context, limit int64) (bool, int64) {
	size, err := s.logicalSize(ctx)
	if err != nil {
		s.log.Debug("size probe failed", slog.Any("err", err))
		return false, 0
	}
	if size > limit {
		return true, size
	}
	if wal := fileSize(s.path + "-wal"); wal > limit {
		_, _ = s.writer.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
		if wal = fileSize(s.path + "-wal"); wal > limit {
			return true, wal
		}
	}
	return false, size
}

// logicalSize is the database size in bytes without free (reusable) pages.
func (s *Store) logicalSize(ctx context.Context) (int64, error) {
	var pages, free, pageSize int64
	if err := s.writer.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return 0, err
	}
	if err := s.writer.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free); err != nil {
		return 0, err
	}
	if err := s.writer.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, err
	}
	return (pages - free) * pageSize, nil
}

func fileSize(path string) int64 {
	if st, err := os.Stat(path); err == nil {
		return st.Size()
	}
	return 0
}

// reclaim makes room when the size cap is hit. Pruning order (documented in schema.md): 1. trail
// fixes, whole trails of the least recently seen contacts first, oldest fixes first, half of all
// fixes; 2. only if that is not enough, the oldest half of the batch ids and death ids. It runs
// once per transition into the full state, so a permanently oversized database is not eaten away.
func (s *Store) reclaim(ctx context.Context, limit int64) {
	if n := s.fixCount.Load(); n > 0 {
		if err := s.pruneFixesBy(ctx, n-n/2); err != nil {
			s.log.Warn("reclaim: pruning fixes failed", slog.Any("err", err))
		}
		if over, _ := s.overCap(ctx, limit); !over {
			s.log.Warn("database reached its size cap; oldest trail fixes pruned", slog.Int64("maxMB", limit/mib))
			return
		}
	}
	if err := s.pruneIDsKeep(ctx, true); err != nil {
		s.log.Warn("reclaim: pruning ids failed", slog.Any("err", err))
		return
	}
	s.log.Warn("database reached its size cap; trail fixes and old batch/death ids pruned", slog.Int64("maxMB", limit/mib))
}

// CheckIntegrity runs quick_check (full=false) or integrity_check (full=true). A failure marks the
// store degraded: writes then return domain.ErrDegraded until the process is restarted on a restored file.
func (s *Store) CheckIntegrity(ctx context.Context, full bool) error {
	mode := "quick_check"
	if full {
		mode = "integrity_check"
	}
	res, err := s.runCheck(ctx, mode)
	s.lastCheck.Store(s.nowMs())
	if err == nil && res == "ok" {
		return nil
	}
	detail := res
	if err != nil {
		detail = err.Error()
	}
	if !s.degraded.Swap(true) {
		s.notify("integrity_degraded", mode)
		s.log.Error("DATABASE INTEGRITY CHECK FAILED: writes are refused (503) and reads may be wrong. "+restoreHelp(s.path),
			slog.String("check", mode), slog.String("detail", truncate(detail, 300)))
	}
	return fmt.Errorf("sqlstore: %s failed: %s: %w", mode, truncate(detail, 300), ErrCorrupt)
}

// sqliteCheck runs the pragma on a reader connection (readers never block the writer in WAL mode).
func (s *Store) sqliteCheck(ctx context.Context, mode string) (string, error) {
	return pragmaCheck(ctx, s.readers, mode)
}

// monitor runs the full integrity check on a timer until ctx ends.
func (s *Store) monitor(ctx context.Context, every time.Duration) {
	defer close(s.monitorDone)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.CheckIntegrity(ctx, true); err != nil {
				if ctx.Err() == nil {
					s.log.Debug("scheduled integrity check failed", slog.Any("err", err))
				}
			} else {
				s.log.Info("scheduled integrity check ok")
			}
		}
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
