package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// QueryFunc is a named read handler; params are URL-style query values.
type QueryFunc func(ctx context.Context, params map[string][]string) (any, error)

// Queries is the socket of named read handlers used by the sweep.
type Queries struct {
	mu    sync.RWMutex
	funcs map[string]QueryFunc
	order []string
}

// NewQueries creates an empty socket.
func NewQueries() *Queries { return &Queries{funcs: map[string]QueryFunc{}} }

// Add registers fn under name; names must be unique.
func (q *Queries) Add(name string, fn QueryFunc) error {
	if name == "" {
		return fmt.Errorf("query: %w", errEmptyName)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, dup := q.funcs[name]; dup {
		return fmt.Errorf("query %q registered twice", name)
	}
	q.funcs[name] = fn
	q.order = append(q.order, name)
	return nil
}

// Get returns the handler registered under name.
func (q *Queries) Get(name string) (QueryFunc, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	fn, ok := q.funcs[name]
	return fn, ok
}

// Names lists registered queries in registration order.
func (q *Queries) Names() []string {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return append([]string(nil), q.order...)
}

// Stores is the socket of named Store implementations; the first registered is the default.
type Stores struct {
	mu     sync.RWMutex
	stores map[string]Store
	order  []string
}

// NewStores creates an empty socket.
func NewStores() *Stores { return &Stores{stores: map[string]Store{}} }

// Add registers a store under a unique name.
func (s *Stores) Add(name string, store Store) error {
	if name == "" {
		return fmt.Errorf("store: %w", errEmptyName)
	}
	if store == nil {
		return fmt.Errorf("store %q: nil store", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.stores[name]; dup {
		return fmt.Errorf("store %q registered twice", name)
	}
	s.stores[name] = store
	s.order = append(s.order, name)
	return nil
}

// Get returns the store registered under name.
func (s *Stores) Get(name string) (Store, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	store, ok := s.stores[name]
	return store, ok
}

// Default returns the first registered store.
func (s *Stores) Default() (Store, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.order) == 0 {
		return nil, errors.New("no store registered")
	}
	return s.stores[s.order[0]], nil
}

// Names lists registered stores in registration order.
func (s *Stores) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.order...)
}

// CloseAll closes every store, newest first, and joins any errors.
func (s *Stores) CloseAll() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var errs []error
	for i := len(s.order) - 1; i >= 0; i-- {
		if err := s.stores[s.order[i]].Close(); err != nil {
			errs = append(errs, fmt.Errorf("close store %q: %w", s.order[i], err))
		}
	}
	return errors.Join(errs...)
}

// Hook is a lifecycle callback.
type Hook func(ctx context.Context) error

// Hooks is the socket for start and stop callbacks.
type Hooks struct {
	mu    sync.Mutex
	start []Hook
	stop  []Hook
}

// NewHooks creates an empty socket.
func NewHooks() *Hooks { return &Hooks{} }

// OnStart registers a callback run before modules start.
func (h *Hooks) OnStart(fn Hook) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.start = append(h.start, fn)
}

// OnStop registers a callback run after modules stop.
func (h *Hooks) OnStop(fn Hook) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stop = append(h.stop, fn)
}

// RunStart runs start hooks in order and stops at the first error.
func (h *Hooks) RunStart(ctx context.Context) error {
	h.mu.Lock()
	hooks := append([]Hook(nil), h.start...)
	h.mu.Unlock()
	for _, fn := range hooks {
		if err := fn(ctx); err != nil {
			return fmt.Errorf("start hook: %w", err)
		}
	}
	return nil
}

// RunStop runs every stop hook, newest first, and joins any errors.
func (h *Hooks) RunStop(ctx context.Context) error {
	h.mu.Lock()
	hooks := append([]Hook(nil), h.stop...)
	h.mu.Unlock()
	var errs []error
	for i := len(hooks) - 1; i >= 0; i-- {
		if err := hooks[i](ctx); err != nil {
			errs = append(errs, fmt.Errorf("stop hook: %w", err))
		}
	}
	return errors.Join(errs...)
}
