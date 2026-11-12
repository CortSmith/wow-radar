package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

var errNoSource = errors.New("restore needs the backup file to restore from")

// restore replaces the database with a verified backup. It refuses while a radar-db holds the
// database lock, verifies the backup and the staged copy, and keeps the previous database (and its
// WAL files) next to it as <db>.pre-restore-<time> instead of deleting anything.
func restore(args []string, lookupEnv func(string) (string, bool), stdout io.Writer) error {
	dbPath := defaultDBPath
	if v, ok := lookupEnv(dbPathEnvName); ok && v != "" {
		dbPath = v
	}
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&dbPath, "db-path", dbPath, "database file to replace")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if fs.NArg() != 1 {
		return errNoSource
	}
	src := fs.Arg(0)
	if same(src, dbPath) {
		return errors.New("restore: the backup and the database are the same file")
	}
	if err := verifyDB(src); err != nil { // cheap and read-only: fail before taking any lock
		return fmt.Errorf("restore: the backup is not usable, nothing was changed: %w", err)
	}
	release, err := lockDB(dbPath)
	if err != nil {
		return fmt.Errorf("restore: %s is in use (is radar-db running? stop it first, then retry), nothing was changed: %w", dbPath, err)
	}
	defer func() { _ = release() }()

	staged := dbPath + ".restore-tmp"
	_ = os.Remove(staged) // a leftover of an earlier failed restore; it was only ever a copy
	if err := copyPrivate(src, staged); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("restore: copy backup: %w", err)
	}
	if err := verifyDB(staged); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("restore: the copied backup failed verification, nothing was changed: %w", err)
	}
	aside, err := moveAside(dbPath)
	if err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("restore: %w", err)
	}
	if err := os.Rename(staged, dbPath); err != nil {
		undoAside(dbPath, aside)
		_ = os.Remove(staged)
		return fmt.Errorf("restore: put the backup in place (previous database put back): %w", err)
	}
	fmt.Fprintf(stdout, "restored %s from %s (verified)\n", dbPath, src)
	if aside != "" {
		fmt.Fprintf(stdout, "the previous database was kept as %s; delete it yourself when you no longer need it\n", aside)
	}
	return nil
}

// walSuffixes are the SQLite sidecar files that belong to a database file.
var walSuffixes = []string{"-wal", "-shm"}

// moveAside renames the database and its WAL files to <db>.pre-restore-<time>[...]. A stale WAL left
// beside the restored file would be replayed into it, so the sidecars must move too. It returns the
// new name of the main file ("" when there was none).
func moveAside(dbPath string) (string, error) {
	aside := dbPath + ".pre-restore-" + backupNow().UTC().Format("20060102T150405Z")
	for i := 1; exists(aside); i++ {
		aside = fmt.Sprintf("%s.pre-restore-%s_%d", dbPath, backupNow().UTC().Format("20060102T150405Z"), i)
	}
	moved := ""
	if exists(dbPath) {
		if err := os.Rename(dbPath, aside); err != nil {
			return "", fmt.Errorf("move the current database aside: %w", err)
		}
		moved = aside
	}
	for _, suf := range walSuffixes {
		if exists(dbPath + suf) {
			if err := os.Rename(dbPath+suf, aside+suf); err != nil {
				undoAside(dbPath, moved)
				return "", fmt.Errorf("move %s aside: %w", dbPath+suf, err)
			}
		}
	}
	return moved, nil
}

// undoAside puts a moved database (and sidecars) back after a failed restore.
func undoAside(dbPath, aside string) {
	if aside == "" {
		return
	}
	_ = os.Rename(aside, dbPath)
	for _, suf := range walSuffixes {
		if exists(aside + suf) {
			_ = os.Rename(aside+suf, dbPath+suf)
		}
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

func same(a, b string) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(ia, ib)
	}
	pa, _ := filepath.Abs(a)
	pb, _ := filepath.Abs(b)
	return pa == pb
}

// copyPrivate copies src to a new file dst (mode 0600) and syncs it.
func copyPrivate(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
