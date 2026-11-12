package core

import (
	"log/slog"
	"net/http"
	"slices"
	"sync"
)

// Role is what a request wants to do: read data or push data in.
type Role string

// Request roles.
const (
	RoleView   Role = "view"
	RoleIngest Role = "ingest"
)

// Authenticator decides whether a request may act in a role.
type Authenticator interface {
	Authenticate(r *http.Request) bool
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(r *http.Request) bool

// Authenticate calls f.
func (f AuthenticatorFunc) Authenticate(r *http.Request) bool { return f(r) }

// Auth is the socket holding the authenticators of each role. A role with none registered is open.
type Auth struct {
	log    *slog.Logger
	mu     sync.Mutex
	chains map[Role][]Authenticator
	warned map[Role]bool
	used   map[Role]bool
}

// NewAuth creates an Auth that reports open roles to log.
func NewAuth(log *slog.Logger) *Auth {
	return &Auth{log: log, chains: map[Role][]Authenticator{}, warned: map[Role]bool{}, used: map[Role]bool{}}
}

// Add registers an authenticator for role; a request passes if any authenticator of the role accepts it.
func (a *Auth) Add(role Role, auth Authenticator) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.chains[role] = append(a.chains[role], auth)
}

// Require wraps next so only requests acceptable for role reach it.
func (a *Auth) Require(role Role, next http.Handler) http.Handler {
	a.mu.Lock()
	a.used[role] = true
	a.mu.Unlock()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chain, open := a.chain(role)
		if open || anyAccepts(chain, r) {
			next.ServeHTTP(w, r)
			return
		}
		WriteError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid key")
	})
}

// chain returns the role's authenticators; open is true (with a one-time warning) when there are none.
func (a *Auth) chain(role Role) (chain []Authenticator, open bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	chain = a.chains[role]
	if len(chain) > 0 {
		return chain, false
	}
	if !a.warned[role] {
		a.warned[role] = true
		a.log.Warn("no authenticator registered; API is open", "role", string(role))
	}
	return nil, true
}

func anyAccepts(chain []Authenticator, r *http.Request) bool {
	for _, auth := range chain {
		if auth.Authenticate(r) {
			return true
		}
	}
	return false
}

// RolesInUse lists, sorted, the roles that some handler asked Require to protect.
func (a *Auth) RolesInUse() []Role {
	a.mu.Lock()
	defer a.mu.Unlock()
	roles := make([]Role, 0, len(a.used))
	for r := range a.used {
		roles = append(roles, r)
	}
	slices.Sort(roles)
	return roles
}

// HasAuthenticator reports whether role has at least one authenticator (that is, is not open).
func (a *Auth) HasAuthenticator(role Role) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.chains[role]) > 0
}
