package main

import (
	"radarlive/internal/core"
	"radarlive/internal/mod/bountyx"
	"radarlive/internal/mod/contactx"
	"radarlive/internal/mod/guard/abuse"
	"radarlive/internal/mod/guard/audit"
	"radarlive/internal/mod/guard/limits"
	"radarlive/internal/mod/guard/ratelimit"
	"radarlive/internal/mod/keys"
	"radarlive/internal/mod/logparse"
	"radarlive/internal/mod/relay"
	"radarlive/internal/mod/streamapi"
)

// modules lists every module of the ingest binary. relay comes first so it stops last: the
// parsing modules publish their final pings while stopping and the relay must still be there
// to drain them. Removing any single line (except that ordering) leaves a working binary.
func modules() []core.Module {
	return []core.Module{
		relay.NewModule(),
		ratelimit.New(),
		abuse.New(),
		limits.New(),
		keys.New(),
		logparse.New(),
		contactx.New(),
		bountyx.New(),
		streamapi.New(),
		audit.New(), // metrics listener is off unless RADAR_INTERNAL_LISTEN is set (127.0.0.1:8790 suggested)
	}
}
