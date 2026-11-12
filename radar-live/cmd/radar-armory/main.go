// Command radar-armory is the one-shot armory sync against the Battle.net WoW Classic Profile API
// (OAuth2 client credentials; endpoint and field details are PROVISIONAL). The crawl is seeded from
// radar-db (GET /internal/v1/armory/seeds: known contacts, then guilds from stored armory data, whose
// rosters are expanded); the fetched characters are stored through radar-db's internal listener. It runs
// once per invocation (a systemd timer starts it nightly) and exits with 0 (ok), 1 (partial or
// interrupted) or 2 (fatal: configuration, rejected credentials). The client id and secret are read once
// from WOW_ARMORY_CLIENT_ID and WOW_ARMORY_API_KEY (or their _FILE variants) and never printed.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

// Exit codes.
const (
	exitOK      = 0
	exitPartial = 1
	exitFatal   = 2
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr, testHooks{})
	stop()
	os.Exit(code)
}

// run performs one sync and returns the process exit code. Messages never contain a setting value.
// Audit events are not written: the audit module belongs to the long-running hosts, and reusing it here
// would pull a host into a one-shot tool; the one-line summary on stdout is journald's record.
func run(ctx context.Context, args []string, lookup func(string) (string, bool), stdout, stderr io.Writer, h testHooks) int {
	cfg, err := parseConfig(args, lookup)
	if err != nil {
		fmt.Fprintln(stderr, "radar-armory:", err)
		return exitFatal
	}
	if cfg.showVersion {
		fmt.Fprintln(stdout, "radar-armory", version)
		return exitOK
	}
	syncer, sink, err := buildSyncer(cfg, h)
	if err != nil {
		fmt.Fprintln(stderr, "radar-armory:", err)
		return exitFatal
	}
	out, err := runSync(ctx, syncer)
	code := out.exit
	if sink.authFailed() && code < exitFatal {
		code = exitFatal // radar-db refused our key: a configuration problem, not a partial run
		out.note = "radar-db rejected the DB key"
	}
	fmt.Fprintf(stdout, "radar-armory: status=%s run=%s enumerated=%d fetched=%d stored=%d skipped=%d failed=%d duration=%s exit=%d\n",
		out.status, out.runID, out.enumerated, out.fetched, out.stored, out.skipped, out.failed,
		out.duration.Round(time.Millisecond), code)
	if out.note != "" {
		fmt.Fprintln(stderr, "radar-armory: note:", out.note)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "radar-armory:", err)
	}
	return code
}
