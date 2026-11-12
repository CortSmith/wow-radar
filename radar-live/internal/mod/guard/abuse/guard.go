package abuse

import (
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/netx"
)

// Guard is the HTTP side of the module: it refuses banned addresses before any auth work happens,
// counts failed key attempts from the response status, and keeps internal routes loopback-only.
type Guard struct {
	table         *Table
	trusted       []netip.Prefix
	internalAllow []netip.Prefix
	audit         *core.Audit // optional: auth failures, bans and internal denials are reported here
}

// NewGuard creates a guard over table. trusted are the reverse proxies whose forwarded client
// address is believed (the same rules as the rate limiter); internalAllow are extra peers allowed
// on the internal listener besides loopback.
func NewGuard(table *Table, trusted, internalAllow []netip.Prefix) *Guard {
	return &Guard{table: table, trusted: trusted, internalAllow: internalAllow}
}

// deny writes the one refusal every banned or forbidden client sees: the same status, body and
// headers whatever it sent, so nothing about keys or roles can be learned from it.
func deny(w http.ResponseWriter, status int, retryAfter time.Duration) {
	if retryAfter > 0 {
		secs := int((retryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
	}
	core.WriteError(w, status, "denied", "request refused")
}

// addrText is the log text of an address; empty for the zero Addr.
func addrText(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// Wrap guards next for the given listener class.
func (g *Guard) Wrap(class string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Internal routes: the TCP peer itself must be loopback (or explicitly allowed); forwarded
		// headers are never consulted here.
		if class == core.ClassInternal {
			peer := netx.PeerAddr(r)
			if !peer.IsValid() || !(peer.IsLoopback() || netx.IsTrusted(peer, g.internalAllow)) {
				g.audit.Emit(core.AuditEvent{Event: "internal_denied", IP: addrText(peer), Class: class, Reason: "non_loopback"})
				deny(w, http.StatusForbidden, 0)
				return
			}
		}
		ip := netx.ClientIP(r, g.trusted)
		if until, banned := g.table.Banned(ip); banned {
			deny(w, http.StatusTooManyRequests, until.Sub(g.table.now()))
			return
		}
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
			if _, presented := netx.PresentedKey(r); presented {
				g.audit.Emit(core.AuditEvent{Event: "auth_failure", IP: addrText(ip), Class: class, Reason: "bad_key"})
				if g.table.Fail(ip) {
					g.audit.Emit(core.AuditEvent{Event: "ban", IP: addrText(netx.Aggregate(ip)), Class: class, Reason: "lockout"})
				}
			}
		}
	})
}

// statusRecorder remembers the response status while keeping streaming and deadlines working.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush supports streaming responses.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
