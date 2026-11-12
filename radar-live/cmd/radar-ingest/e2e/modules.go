// Package e2e wires an in-process radar-ingest to an in-process radar-db (memory store only) so the
// whole path log line -> ping -> relay -> store can be tested. It lists modules, hence modules.go.
package e2e

import (
	"slices"

	"radarlive/internal/core"
	"radarlive/internal/mod/bountyx"
	"radarlive/internal/mod/contactx"
	"radarlive/internal/mod/logparse"
	"radarlive/internal/mod/memstore"
	"radarlive/internal/mod/relay"
	"radarlive/internal/mod/streamapi"
)

// Ingest returns the ingest module list (same order as cmd/radar-ingest) minus the named modules.
func Ingest(skip ...string) []core.Module {
	all := []core.Module{
		relay.NewModule(),
		logparse.New(),
		contactx.New(),
		bountyx.New(),
		streamapi.New(),
	}
	var out []core.Module
	for _, m := range all {
		if !slices.Contains(skip, m.Name()) {
			out = append(out, m)
		}
	}
	return out
}

// DB returns the modules of the test database host: just the memory store.
func DB() []core.Module { return []core.Module{memstore.NewModule()} }
