// Command radar-ingest is the live-log side of radar-live: it accepts combat-log lines over HTTP,
// extracts contacts and bounty credits and forwards them to radar-db. All behaviour lives in modules
// (see modules.go); this file only wires configuration, signals and the host together.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"radarlive/internal/core"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.LookupEnv); err != nil {
		fmt.Fprintln(os.Stderr, "radar-ingest:", err)
		os.Exit(1)
	}
}

// run builds the host, loads the configuration and serves until ctx ends.
func run(ctx context.Context, args []string, lookup func(string) (string, bool)) error {
	host := core.NewHost()
	if err := host.Register(modules()...); err != nil {
		return err
	}
	if err := host.Config.Load(args, withDefaults(args, lookup)); err != nil {
		return err
	}
	return host.Run(ctx)
}
