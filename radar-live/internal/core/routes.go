package core

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Route classes name the listeners a route is served on. Everything registered with HandleFunc is
// public; a module that wants a private listener (the write API) registers on ClassInternal after
// Host.EnsureListener declared it.
const (
	ClassPublic   = "public"
	ClassInternal = "internal"
)

// Routes is the HTTP socket: modules register method-aware handlers here, per route class.
type Routes struct {
	muxes map[string]*http.ServeMux
	seen  map[string]bool
}

// NewRoutes creates a route table that has only the public class.
func NewRoutes() *Routes {
	return &Routes{muxes: map[string]*http.ServeMux{ClassPublic: http.NewServeMux()}, seen: map[string]bool{}}
}

// AddClass creates an empty route table for class; it is a no-op when the class exists.
func (r *Routes) AddClass(class string) {
	if _, ok := r.muxes[class]; !ok {
		r.muxes[class] = http.NewServeMux()
	}
}

// HandleFunc registers handler on the public class for method and pattern (for example "GET", "/api/v1/sweep").
// Patterns use net/http syntax, including {name} wildcards. Duplicates and conflicts return an error.
func (r *Routes) HandleFunc(method, pattern string, handler http.HandlerFunc) error {
	return r.HandleFuncOn(ClassPublic, method, pattern, handler)
}

// HandleFuncOn is HandleFunc for a named route class, which must exist (see Host.EnsureListener).
func (r *Routes) HandleFuncOn(class, method, pattern string, handler http.HandlerFunc) error {
	mux, ok := r.muxes[class]
	if !ok {
		return fmt.Errorf("route %s %s: unknown route class %q", method, pattern, class)
	}
	if method == "" || !strings.HasPrefix(pattern, "/") {
		return fmt.Errorf("route %q %q: need a method and a pattern starting with /", method, pattern)
	}
	if handler == nil {
		return fmt.Errorf("route %s %s: nil handler", method, pattern)
	}
	key := method + " " + pattern
	if r.seen[class+" "+key] {
		return fmt.Errorf("route %s registered twice", key)
	}
	if err := register(mux, key, handler); err != nil {
		return err
	}
	r.seen[class+" "+key] = true
	return nil
}

// register adds the pattern to the mux, converting its conflict panic into an error.
func register(mux *http.ServeMux, key string, handler http.Handler) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("route %s: %v", key, p)
		}
	}()
	mux.Handle(key, handler)
	return nil
}

// Handler returns the public handler.
func (r *Routes) Handler() http.Handler { return r.muxes[ClassPublic] }

// HandlerFor returns the handler of a route class, or nil for an unknown class.
func (r *Routes) HandlerFor(class string) http.Handler {
	if mux, ok := r.muxes[class]; ok {
		return mux
	}
	return nil
}

var errEmptyName = errors.New("name must not be empty")
