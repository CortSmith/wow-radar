package main

import (
	"context"
	"fmt"
	"io"
	"slices"

	"radarlive/internal/core"
	"radarlive/internal/mod/guard/abuse"
	"radarlive/internal/mod/guard/audit"
	"radarlive/internal/mod/guard/headers"
	"radarlive/internal/mod/guard/limits"
	"radarlive/internal/mod/guard/ratelimit"
	"radarlive/internal/mod/keys"
	"radarlive/internal/mod/memstore"
	"radarlive/internal/mod/readapi"
	"radarlive/internal/mod/sqlstore"
	"radarlive/internal/mod/static"
	"radarlive/internal/mod/writeapi"
)

// Store names accepted by --store.
const (
	storeSQLite   = "sqlite"
	storeMemstore = "memstore"
)

// modules is the ONLY list of radar-db's modules. Delete a line (and its import) to remove a
// feature; the binary still builds. The chosen store module comes first so it is the default store.
// Names in omit are left out (tests use this to prove each feature is removable).
func modules(store string, omit ...string) ([]core.Module, error) {
	var storeModule core.Module
	switch store {
	case storeSQLite:
		storeModule = sqlstore.New()
	case storeMemstore:
		storeModule = memstore.NewModule()
	default:
		return nil, fmt.Errorf("unknown store %q (use %s or %s)", store, storeSQLite, storeMemstore)
	}
	all := []core.Module{
		storeModule,
		headers.New(),
		ratelimit.New(),
		abuse.New(),
		limits.New(),
		keys.New(),
		readapi.NewModule(),
		writeapi.NewModule(),
		static.NewModule(),
		audit.New(), // after writeapi: it only adds /internal/metrics to the internal listener writeapi declares
	}
	return slices.DeleteFunc(all, func(m core.Module) bool { return slices.Contains(omit, m.Name()) }), nil
}

// The helpers below exist so that modules.go stays the only file that imports module packages.

// generateKey returns a new random API key (see the keys module).
func generateKey() (string, error) { return keys.GenerateKey() }

// setVersion publishes the build version through the read API's status route.
func setVersion(v string) { readapi.Version = v }

// backupDB writes a consistent copy of the SQLite database at dbPath to target.
func backupDB(ctx context.Context, dbPath, target string) error {
	store, err := sqlstore.Open(dbPath, sqlstore.Options{})
	if err != nil {
		return err
	}
	defer store.Close()
	return store.Backup(ctx, target)
}

// verifyDB checks that path is a usable SQLite database of this build (read-only, quick_check).
func verifyDB(path string) error { return sqlstore.Verify(path) }

// lockDB takes the exclusive server lock of the database at dbPath (what a running radar-db holds).
func lockDB(dbPath string) (func() error, error) { return sqlstore.AcquireLock(dbPath) }

// guard runs the `guard list|unban|ban` admin commands on the abuse module's ban file, found like
// the server finds it: RADAR_ABUSE_BAN_FILE, else beside the database (RADAR_DB_PATH or the default).
func guard(args []string, lookupEnv func(string) (string, bool), stdout io.Writer) error {
	explicit, _ := lookupEnv("RADAR_ABUSE_BAN_FILE")
	dbPath, _ := lookupEnv(dbPathEnvName)
	if dbPath == "" {
		dbPath = defaultDBPath
	}
	return abuse.Admin(args, abuse.ResolveBanFile(explicit, dbPath), nil, stdout)
}
