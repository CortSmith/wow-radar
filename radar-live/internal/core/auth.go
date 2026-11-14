package core

import (
	"context"
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

// KeyLimits are optional per-key rate limits (nil = server default). It mirrors keyring.Limits so
// core does not depend on the keyring package.
type KeyLimits struct {
	LineRate, LineBurst, ByteRate, ByteBurst, MaxStreams *int64
}

// KeyInfo describes the key that authenticated a request. ID is the hex of the first 16 bytes of the
// key's SHA-256; FromKeyring is false for keys configured through settings.
type KeyInfo struct {
	ID          string
	FromKeyring bool
	Limits      KeyLimits
}

// Identifier is an optional Authenticator extension: Identify accepts the request like Authenticate
// and also says which key matched. Auth.Require stores the result in the request context.
type Identifier interface {
	Identify(r *http.Request) (KeyInfo, bool)
}

// Rejecter is an optional Authenticator extension consulted after every authenticator of the role
// refused a request: when ok is true Require answers with status, code and message instead of the
// default 401 (for example 403 for a key that is known but disabled).
type Rejecter interface {
	Reject(r *http.Request) (status int, code, message string, ok bool)
}

type keyInfoCtx struct{}

// KeyInfoFromContext returns the key info Auth.Require stored for an authenticated request.
func KeyInfoFromContext(ctx context.Context) (KeyInfo, bool) {
	info, ok := ctx.Value(keyInfoCtx{}).(KeyInfo)
	return info, ok
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
	lookup KeyLookup
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
		if open {
			next.ServeHTTP(w, r)
			return
		}
		if ok, info, identified := accepts(chain, r); ok {
			if identified {
				r = r.WithContext(context.WithValue(r.Context(), keyInfoCtx{}, info))
			}
			next.ServeHTTP(w, r)
			return
		}
		for _, auth := range chain {
			if rej, isRej := auth.(Rejecter); isRej {
				if status, code, msg, ok := rej.Reject(r); ok {
					WriteError(w, status, code, msg)
					return
				}
			}
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

// accepts reports whether some authenticator accepts r and, when it can identify the key, which one.
func accepts(chain []Authenticator, r *http.Request) (ok bool, info KeyInfo, identified bool) {
	for _, auth := range chain {
		if id, isID := auth.(Identifier); isID {
			if info, ok := id.Identify(r); ok {
				return true, info, true
			}
			continue
		}
		if auth.Authenticate(r) {
			return true, KeyInfo{}, false
		}
	}
	return false, KeyInfo{}, false
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

// KeyLookup reports the current state of a keyring key by its id: its latest KeyInfo and whether it
// is active (not disabled, not expired). A key that is not in the keyring is reported inactive.
type KeyLookup func(id string) (info KeyInfo, active bool)

// SetKeyLookup registers the keyring's lookup, used to end open streams of a key that was blocked
// and to apply changed limits to open streams. The last registration wins.
func (a *Auth) SetKeyLookup(f KeyLookup) {
	a.mu.Lock()
	a.lookup = f
	a.mu.Unlock()
}

// LookupKey returns the current state of a keyring key; ok is false when no keyring is registered.
func (a *Auth) LookupKey(id string) (info KeyInfo, active, ok bool) {
	a.mu.Lock()
	f := a.lookup
	a.mu.Unlock()
	if f == nil {
		return KeyInfo{}, false, false
	}
	info, active = f(id)
	return info, active, true
}
