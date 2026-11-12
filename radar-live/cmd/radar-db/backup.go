package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

const (
	dbPathEnvName = "RADAR_DB_PATH"
	defaultDBPath = "radar-live.db"
	backupExt     = ".partial"
	maxSameSecond = 99
)

// Test hooks.
var (
	backupNow = time.Now
	// verifyBackup checks a freshly written copy (quick_check, schema version) before it is published.
	verifyBackup = func(path string) error { return verifyDB(path) }
)

// rotatedName matches the files `backup --keep` creates and is the only pattern it ever deletes.
var rotatedName = regexp.MustCompile(`^radar-\d{8}T\d{6}Z(_\d+)?\.db$`)

// backup copies the SQLite database with VACUUM INTO, verifies the copy with quick_check, and
// publishes it with mode 0600 without ever overwriting a file. With --keep N the target is a
// directory that receives timestamped files, and all but the newest N are removed afterwards.
func backup(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stdout io.Writer) error {
	dbPath := defaultDBPath
	if v, ok := lookupEnv(dbPathEnvName); ok && v != "" {
		dbPath = v
	}
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&dbPath, "db-path", dbPath, "database file to back up")
	keep := fs.Int("keep", 0, "rotate: write a timestamped file into the target directory and keep the newest N")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if fs.NArg() != 1 {
		return errNoTarget
	}
	if *keep < 0 {
		return errors.New("backup: --keep must be 1 or more")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("backup: database: %w", err)
	}
	if *keep == 0 {
		target := fs.Arg(0)
		if err := writeVerified(ctx, dbPath, target); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
		_, err := fmt.Fprintf(stdout, "backup written to %s (verified)\n", target)
		return err
	}
	return rotate(ctx, dbPath, fs.Arg(0), *keep, stdout)
}

// rotate writes a new timestamped backup into dir and prunes the older ones.
func rotate(ctx context.Context, dbPath, dir string, keep int, stdout io.Writer) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	stamp := backupNow().UTC().Format("20060102T150405Z")
	target, err := freshName(dir, stamp)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err := writeVerified(ctx, dbPath, target); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	fmt.Fprintf(stdout, "backup written to %s (verified)\n", target)
	removed, err := pruneBackups(dir, keep)
	for _, name := range removed {
		fmt.Fprintf(stdout, "removed old backup %s\n", name)
	}
	if err != nil {
		return fmt.Errorf("backup: written, but pruning old backups failed: %w", err)
	}
	return nil
}

// freshName returns a path in dir for stamp that does not exist yet.
func freshName(dir, stamp string) (string, error) {
	for i := 0; i <= maxSameSecond; i++ {
		name := "radar-" + stamp + ".db"
		if i > 0 {
			name = fmt.Sprintf("radar-%s_%d.db", stamp, i)
		}
		if _, err := os.Lstat(filepath.Join(dir, name)); errors.Is(err, fs.ErrNotExist) {
			return filepath.Join(dir, name), nil
		}
	}
	return "", errors.New("too many backups in the same second")
}

// pruneBackups deletes the oldest rotated backups beyond keep; other files are never touched.
func pruneBackups(dir string, keep int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && rotatedName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // timestamps sort chronologically; "_N" suffixes sort after the plain name
	var removed []string
	for len(names)-len(removed) > keep {
		name := names[len(removed)]
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return removed, err
		}
		removed = append(removed, name)
	}
	return removed, nil
}

// writeVerified writes the backup to a private temporary file next to target, verifies it, and
// publishes it with a hard link so an existing target is never replaced.
func writeVerified(ctx context.Context, dbPath, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("%s already exists; backups never overwrite", target)
	}
	tmp := target + backupExt
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // empty and private before data lands
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	_ = f.Close()
	defer os.Remove(tmp)
	if err := backupDB(ctx, dbPath, tmp); err != nil {
		return err
	}
	if err := verifyBackup(tmp); err != nil {
		return fmt.Errorf("the new backup failed verification and was discarded: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := os.Link(tmp, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s appeared meanwhile; backups never overwrite", target)
		}
		if _, statErr := os.Lstat(target); statErr == nil {
			return fmt.Errorf("%s appeared meanwhile; backups never overwrite", target)
		}
		return os.Rename(tmp, target) // filesystem without hard links; existence was checked just above
	}
	return nil
}

var errNoTarget = errors.New("backup needs a target file (or a directory with --keep N)")
