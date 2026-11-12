package sqlstore

import (
	"fmt"
	"os"
	"path/filepath"
)

// AcquireLock takes the exclusive "server" lock of the database at dbPath (a "<db>.lock" file next
// to it). radar-db holds it while serving; `radar-db restore` needs it, so a restore can never run
// under a live server. The OS releases it when the process dies. The returned func releases it.
func AcquireLock(dbPath string) (release func() error, err error) {
	if err := mkdirPrivate(filepath.Dir(dbPath)); err != nil {
		return nil, fmt.Errorf("sqlstore: create data directory: %w", err)
	}
	return lockFile(dbPath + ".lock")
}

// mkdirPrivate creates dir (one level, mode 0700) when it is missing.
func mkdirPrivate(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}
