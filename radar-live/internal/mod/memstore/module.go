package memstore

import "radarlive/internal/core"

// ModuleName is the name under which the module and its store register.
const ModuleName = "memstore"

// Module plugs a Store into the core's Stores socket.
type Module struct{}

// NewModule returns the memstore module.
func NewModule() *Module { return &Module{} }

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init registers a wall-clock Store with the host.
func (*Module) Init(host *core.Host) error { return host.Stores.Add(ModuleName, New(nil)) }
