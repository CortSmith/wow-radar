// Command radar-db is the radar-live database server: it owns the store, serves the read API and
// the web page, and accepts pings from radar-ingest. Subcommands: keygen, backup, restore, guard, version.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"radarlive/internal/core"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	keyStore       = "store"
	defaultStore   = storeSQLite
	exitFailure    = 1
	exitUsage      = 2
	usageText      = "usage: radar-db [flags] | keygen | backup [--db-path file] [--keep N] <target-or-dir> | restore [--db-path file] <backup> | guard list|unban <ip>|ban <ip> <duration> | version"
	storeEnvName   = "RADAR_STORE"
	storeFlagShort = "-store"
	storeFlagLong  = "--store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run executes one command line and returns the process exit code.
func run(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stdout, stderr io.Writer) int {
	var err error
	switch first(args) {
	case "keygen":
		err = keygen(stdout)
	case "version":
		_, err = fmt.Fprintln(stdout, "radar-db", version)
	case "backup":
		err = backup(ctx, args[1:], lookupEnv, stdout)
	case "restore":
		err = restore(args[1:], lookupEnv, stdout)
	case "guard":
		err = guard(args[1:], lookupEnv, stdout)
	default:
		if first(args) != "" && !strings.HasPrefix(args[0], "-") {
			fmt.Fprintln(stderr, usageText)
			return exitUsage
		}
		err = serve(ctx, args, lookupEnv, stderr)
	}
	if err != nil {
		fmt.Fprintln(stderr, "radar-db:", err)
		return exitFailure
	}
	return 0
}

func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func keygen(stdout io.Writer) error {
	key, err := generateKey()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, key)
	return err
}

// serve builds the host from flags, environment and env file, and runs it until ctx ends.
func serve(ctx context.Context, args []string, lookupEnv func(string) (string, bool), logOut io.Writer) error {
	host, err := buildHost(args, lookupEnv, logOut)
	if err != nil {
		return err
	}
	return host.Run(ctx)
}

// buildHost registers the modules for the chosen store and loads the configuration.
func buildHost(args []string, lookupEnv func(string) (string, bool), logOut io.Writer) (*core.Host, error) {
	return buildHostWithout(args, lookupEnv, logOut)
}

// buildHostWithout is buildHost minus the named modules.
func buildHostWithout(args []string, lookupEnv func(string) (string, bool), logOut io.Writer, omit ...string) (*core.Host, error) {
	store := chooseStore(args, lookupEnv)
	list, err := modules(store, omit...)
	if err != nil {
		return nil, err
	}
	host := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(logOut, nil))))
	if err := host.Config.Declare(core.Setting{Key: keyStore, Kind: core.KindString, Default: defaultStore,
		Usage: "storage backend: sqlite or memstore (set by flag or environment)"}); err != nil {
		return nil, err
	}
	if err := host.Register(list...); err != nil {
		return nil, err
	}
	if err := host.Config.Load(args, lookupEnv); err != nil {
		return nil, err
	}
	if got := host.Config.String(keyStore); got != store {
		return nil, fmt.Errorf("store %q must be set by flag or environment, not the env file", got)
	}
	setVersion(version)
	return host, nil
}

// chooseStore reads --store (flag, then RADAR_STORE) before the modules exist; the choice decides which are built.
func chooseStore(args []string, lookupEnv func(string) (string, bool)) string {
	for i, a := range args {
		switch {
		case (a == storeFlagLong || a == storeFlagShort) && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(a, storeFlagLong+"="):
			return strings.TrimPrefix(a, storeFlagLong+"=")
		case strings.HasPrefix(a, storeFlagShort+"="):
			return strings.TrimPrefix(a, storeFlagShort+"=")
		}
	}
	if v, ok := lookupEnv(storeEnvName); ok && v != "" {
		return v
	}
	return defaultStore
}
