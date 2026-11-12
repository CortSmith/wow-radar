package core

import (
	"fmt"
	"net"
	"net/http"
	"sync"
)

// ServerTuner adjusts the http.Server of a listener class before it serves.
type ServerTuner func(class string, srv *http.Server)

// ListenerWrapper wraps the net.Listener of a listener class (connection caps and the like).
type ListenerWrapper func(class string, ln net.Listener) net.Listener

// HandlerWrapper wraps the handler of a listener class; it runs inside the core middleware.
type HandlerWrapper func(class string, next http.Handler) http.Handler

// Server is the socket security and tuning modules use to harden the HTTP servers without the
// core knowing about them. Hooks run when the host starts listening, so they may read loaded config.
type Server struct {
	mu        sync.Mutex
	tuners    []ServerTuner
	listeners []ListenerWrapper
	handlers  []HandlerWrapper
}

// NewServer creates an empty socket.
func NewServer() *Server { return &Server{} }

// Tune registers a ServerTuner.
func (s *Server) Tune(fn ServerTuner) { s.add(func() { s.tuners = append(s.tuners, fn) }) }

// WrapListener registers a ListenerWrapper; later wrappers wrap earlier ones.
func (s *Server) WrapListener(fn ListenerWrapper) {
	s.add(func() { s.listeners = append(s.listeners, fn) })
}

// WrapHandler registers a HandlerWrapper; later wrappers are inner (closer to the routes).
func (s *Server) WrapHandler(fn HandlerWrapper) {
	s.add(func() { s.handlers = append(s.handlers, fn) })
}

func (s *Server) add(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

func (s *Server) tune(class string, srv *http.Server) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, fn := range s.tuners {
		fn(class, srv)
	}
}

func (s *Server) wrapListener(class string, ln net.Listener) net.Listener {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, fn := range s.listeners {
		ln = fn(class, ln)
	}
	return ln
}

func (s *Server) wrapHandler(class string, h http.Handler) http.Handler {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.handlers) - 1; i >= 0; i-- {
		h = s.handlers[i](class, h)
	}
	return h
}

// listenerClass is one extra listener: its route class and the setting holding its address.
type listenerClass struct{ class, key string }

// EnsureListener declares an additional listener for route class, bound to the address in setting
// key (default defaultAddr; an empty address disables the listener and its routes are not served).
// It is idempotent for the same class and key, so several modules can share a class. It must run
// before Config.Load, that is, during Init.
func (h *Host) EnsureListener(class, key, defaultAddr, usage string) error {
	if class == "" || class == ClassPublic {
		return fmt.Errorf("listener class %q is reserved or empty", class)
	}
	if known, ok := h.extra[class]; ok {
		if known != key {
			return fmt.Errorf("listener class %q already uses setting %q, not %q", class, known, key)
		}
		return nil
	}
	if err := h.Config.Declare(Setting{Key: key, Kind: KindString, Default: defaultAddr, Usage: usage}); err != nil {
		return err
	}
	h.extra[class] = key
	h.classes = append(h.classes, listenerClass{class: class, key: key})
	h.Routes.AddClass(class)
	h.mustRouteOn(class, http.MethodGet, healthPath, serveHealth)
	return nil
}

// ClassAddr returns the bound address of a route class's listener; empty when it is not listening.
func (h *Host) ClassAddr(class string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.addrs[class]
}
