// Package core is the host every radar-live feature plugs into. It owns the HTTP server, the
// configuration, the lifecycle and a handful of sockets (routes, parsers, stores, queries, sinks,
// hooks, auth, bus). It knows nothing about players or combat logs beyond the shared domain types,
// so any module can be deleted without touching it.
package core

import "context"

// Module is a removable feature. Init wires it into the host's sockets; it must not start goroutines.
type Module interface {
	// Name is unique within a host and appears in errors and logs.
	Name() string
	// Init registers the module's routes, settings, stores and so on.
	Init(host *Host) error
}

// Starter is implemented by modules that run background work.
type Starter interface {
	Start(ctx context.Context) error
}

// Stopper is implemented by modules that must release resources on shutdown.
type Stopper interface {
	Stop(ctx context.Context) error
}
