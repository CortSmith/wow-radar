package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Verify opens the file read-only and checks that it is a SQLite database this build can use and
// that quick_check passes. It never modifies the file. radar-db backup and restore use it.
func Verify(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("sqlstore: %w", err)
	}
	if st.Size() == 0 {
		return fmt.Errorf("sqlstore: %s is empty: %w", path, ErrNotDatabase)
	}
	return preflight(path)
}

// preflight inspects an existing database file before the real open: a missing or empty file is a
// fresh database; anything else must be SQLite, not newer than this build, and pass quick_check.
func preflight(path string) error {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && st.Size() == 0) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sqlstore: %w", err)
	}
	db, err := sql.Open("sqlite", readOnlyDSN(path, fileSize(path+"-wal") == 0))
	if err != nil {
		return fmt.Errorf("sqlstore: open %s: %w", path, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return classify(path, err)
	}
	if version > len(migrations) {
		return fmt.Errorf("sqlstore: %s has schema version %d but this build supports %d: %w. Upgrade radar-live instead of downgrading the data; %s",
			path, version, len(migrations), ErrNewerSchema, restoreHelp(path))
	}
	res, err := pragmaCheck(ctx, db, "quick_check")
	if err != nil {
		return classify(path, err)
	}
	if res != "ok" {
		return fmt.Errorf("sqlstore: quick_check of %s failed (%s): %w; %s", path, truncate(res, 200), ErrCorrupt, restoreHelp(path))
	}
	return nil
}

// classify turns a SQLite error into one of the refusal errors with restore instructions.
func classify(path string, err error) error {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not a database") || strings.Contains(msg, "(26)"):
		return fmt.Errorf("sqlstore: %s is not a SQLite database: %w; %s", path, ErrNotDatabase, restoreHelp(path))
	case strings.Contains(msg, "malformed") || strings.Contains(msg, "corrupt") || strings.Contains(msg, "(11)"):
		return fmt.Errorf("sqlstore: %s is damaged (%v): %w; %s", path, err, ErrCorrupt, restoreHelp(path))
	}
	return fmt.Errorf("sqlstore: cannot read %s: %w", path, err)
}

// pragmaCheck runs quick_check or integrity_check and returns "ok" or the first problems joined.
func pragmaCheck(ctx context.Context, db *sql.DB, mode string) (string, error) {
	var q string
	switch mode {
	case "quick_check":
		q = `PRAGMA quick_check`
	case "integrity_check":
		q = `PRAGMA integrity_check`
	default:
		return "", fmt.Errorf("sqlstore: unknown check %q", mode)
	}
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", err
		}
		if len(lines) < 5 {
			lines = append(lines, line)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(lines) == 1 && lines[0] == "ok" {
		return "ok", nil
	}
	return strings.Join(lines, "; "), nil
}

// readOnlyDSN builds a file: URI that opens path read-only. With no pending WAL the file is opened
// immutable, which also keeps SQLite from creating -wal/-shm files, so checking leaves no trace.
func readOnlyDSN(path string, immutable bool) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Path: p}
	mode := "mode=ro"
	if immutable {
		mode = "immutable=1"
	}
	return "file://" + u.EscapedPath() + "?" + mode + "&_pragma=busy_timeout(5000)"
}

// permProblem returns a warning when mode is wider than owner-only (0700 directories, 0600 files)
// on a system that has Unix permissions; it is pure so it can be tested on every platform.
func permProblem(goos string, isDir bool, mode fs.FileMode) string {
	if goos == "windows" || mode.Perm()&0o077 == 0 {
		return ""
	}
	want := "0600"
	if isDir {
		want = "0700"
	}
	return "mode " + mode.Perm().String() + " lets other users read it; should be " + want
}

// preparePaths creates the data directory (0700) and an empty database file (0600) when missing,
// so SQLite's sidecar files inherit private modes. Existing paths are never changed.
func preparePaths(path string) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return fmt.Errorf("sqlstore: create data directory: %w", err)
		}
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return fmt.Errorf("sqlstore: create %s: %w", path, err)
		}
		return f.Close()
	}
	return nil
}

// warnPermissions logs a loud warning for the data directory and each database file that is too open.
func (s *Store) warnPermissions() {
	for _, p := range []struct {
		path  string
		isDir bool
	}{{filepath.Dir(s.path), true}, {s.path, false}, {s.path + "-wal", false}, {s.path + "-shm", false}} {
		st, err := os.Stat(p.path)
		if err != nil {
			continue
		}
		if msg := permProblem(runtime.GOOS, p.isDir, st.Mode()); msg != "" {
			s.log.Warn("insecure file permissions, fix with chmod: "+msg+"", slog.String("path", p.path))
		}
	}
}
