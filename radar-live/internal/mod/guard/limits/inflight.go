package limits

import (
	"net/http"
	"sync/atomic"

	"radarlive/internal/core"
)

// inflight answers 503 with Retry-After once max requests are being served. Requests for which
// skip returns true (long-lived streams) are neither counted nor refused.
func inflight(max int, skip func(*http.Request) bool, current *atomic.Int64, audit *core.Audit, class string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skip(r) {
			next.ServeHTTP(w, r)
			return
		}
		if current.Add(1) > int64(max) {
			current.Add(-1)
			audit.EmitRequest(r, core.AuditEvent{Event: "overloaded", Class: class, Reason: "inflight_cap"})
			w.Header().Set("Retry-After", "1")
			core.WriteError(w, http.StatusServiceUnavailable, "overloaded", "server is busy, retry shortly")
			return
		}
		defer current.Add(-1)
		next.ServeHTTP(w, r)
	})
}
