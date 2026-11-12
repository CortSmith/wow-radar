package core

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

// Host settings declared by the core itself.
const (
	// DefaultEnvPrefix starts the environment variable names (RADAR_LISTEN and so on).
	DefaultEnvPrefix = "RADAR"
	// SettingListen is the HTTP listen address setting.
	SettingListen = "listen"
	// SettingMaxBody is the request body cap in bytes.
	SettingMaxBody = "max-body"
	// SettingShutdownTimeout is the graceful shutdown allowance.
	SettingShutdownTimeout = "shutdown-timeout"

	defaultListenAddr      = ":8787"
	defaultMaxBodyBytes    = 8 << 20
	defaultShutdownTimeout = 10 * time.Second
	readHeaderTimeout      = 10 * time.Second
	healthPath             = "/healthz"
)

// Host wires modules to sockets and runs the HTTP server. Modules receive it in Init and
// register what they provide; the host only iterates the registries.
type Host struct {
	Routes  *Routes
	Parsers *Parsers
	Stores  *Stores
	Queries *Queries
	Sinks   *Sinks
	Hooks   *Hooks
	Config  *Config
	Auth    *Auth
	Bus     *Bus
	Server  *Server
	Log     *slog.Logger
	Audit   *Audit
	Metrics *Metrics

	modules []Module
	extra   map[string]string // route class -> listen setting key
	classes []listenerClass
	names   map[string]bool

	mu    sync.Mutex
	addrs map[string]string
	ready chan struct{}
}

// Option customises NewHost.
type Option func(*hostOptions)

type hostOptions struct {
	log    *slog.Logger
	prefix string
}

// WithLogger sets the host logger (default: text logs on stderr).
func WithLogger(log *slog.Logger) Option { return func(o *hostOptions) { o.log = log } }

// WithEnvPrefix sets the environment variable prefix (default RADAR).
func WithEnvPrefix(prefix string) Option { return func(o *hostOptions) { o.prefix = prefix } }

// NewHost creates a host with empty sockets, its own settings declared and /healthz registered.
func NewHost(opts ...Option) *Host {
	o := hostOptions{log: slog.New(slog.NewTextHandler(os.Stderr, nil)), prefix: DefaultEnvPrefix}
	for _, opt := range opts {
		opt(&o)
	}
	h := &Host{
		Routes: NewRoutes(), Parsers: NewParsers(), Stores: NewStores(), Queries: NewQueries(),
		Sinks: NewSinks(), Hooks: NewHooks(), Config: NewConfig(o.prefix), Auth: NewAuth(o.log),
		Bus: NewBus(o.log), Server: NewServer(), Audit: NewAudit(), Metrics: NewMetrics(), Log: o.log, names: map[string]bool{}, ready: make(chan struct{}),
		extra: map[string]string{}, addrs: map[string]string{},
	}
	h.declareSettings()
	h.mustRoute(http.MethodGet, healthPath, serveHealth)
	return h
}

func (h *Host) declareSettings() {
	for _, s := range []Setting{
		{Key: SettingListen, Kind: KindString, Default: defaultListenAddr, Usage: "HTTP listen address"},
		{Key: SettingMaxBody, Kind: KindInt, Default: fmt.Sprint(defaultMaxBodyBytes), Usage: "request body cap in bytes"},
		{Key: SettingRequireKeys, Kind: KindString, Default: "auto", Usage: "refuse to start with open auth: true, false, or auto (true unless every listener is loopback)"},
		{Key: SettingShutdownTimeout, Kind: KindDuration, Default: defaultShutdownTimeout.String(), Usage: "graceful shutdown allowance"},
	} {
		if err := h.Config.Declare(s); err != nil {
			panic(err) // the host's own settings are static; a failure is a programming error
		}
	}
}

// mustRoute registers a built-in route; failure is a programming error.
func (h *Host) mustRoute(method, pattern string, fn http.HandlerFunc) {
	if err := h.Routes.HandleFunc(method, pattern, fn); err != nil {
		panic(err)
	}
}

func serveHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// Register adds modules and runs their Init in order. A duplicate or empty name, or a failing Init,
// aborts with the module's name in the error.
func (h *Host) Register(modules ...Module) error {
	for _, m := range modules {
		name := m.Name()
		if name == "" {
			return fmt.Errorf("register module: empty name")
		}
		if h.names[name] {
			return fmt.Errorf("register module %q: duplicate name", name)
		}
		if err := m.Init(h); err != nil {
			return fmt.Errorf("init module %q: %w", name, err)
		}
		h.names[name] = true
		h.modules = append(h.modules, m)
	}
	return nil
}

// ModuleNames lists registered modules in registration order.
func (h *Host) ModuleNames() []string {
	names := make([]string, len(h.modules))
	for i, m := range h.modules {
		names[i] = m.Name()
	}
	return names
}

// Ready is closed once the HTTP listener is accepting connections.
func (h *Host) Ready() <-chan struct{} { return h.ready }

// Addr returns the bound listen address (useful with port 0); empty before Ready.
func (h *Host) Addr() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.addrs[ClassPublic]
}

// mustRouteOn registers a built-in route on a class; failure is a programming error.
func (h *Host) mustRouteOn(class, method, pattern string, fn http.HandlerFunc) {
	if err := h.Routes.HandleFuncOn(class, method, pattern, fn); err != nil {
		panic(err)
	}
}
