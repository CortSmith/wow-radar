package streamapi

import (
	"net/http"

	"radarlive/internal/core"
)

// handleSources lists the live connections. Either an ingest or a view key may read it.
func (m *Module) handleSources(w http.ResponseWriter, r *http.Request) {
	if !m.mayList(r) {
		core.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid key")
		return
	}
	core.WriteJSON(w, http.StatusOK, map[string]any{"sources": m.registry().list(m.now())})
}

// mayList reports whether r passes the view or the ingest authenticators.
func (m *Module) mayList(r *http.Request) bool {
	for _, role := range []core.Role{core.RoleView, core.RoleIngest} {
		allowed := false
		probe := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { allowed = true })
		m.host.Auth.Require(role, probe).ServeHTTP(discardWriter{}, r)
		if allowed {
			return true
		}
	}
	return false
}

// discardWriter swallows the 401 the Auth socket writes while probing a role.
type discardWriter struct{}

func (discardWriter) Header() http.Header         { return http.Header{} }
func (discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (discardWriter) WriteHeader(int)             {}
