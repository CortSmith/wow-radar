package core

import (
	"fmt"
	"net/http"
	"net/netip"
	"sync"
)

// AuditEvent is one security-relevant occurrence. It deliberately has no free-form fields: modules
// name the event, the client address, the route class and a short reason token, so a header, query
// string, body or key can never travel through it. The audit module (if present) logs and counts them.
type AuditEvent struct {
	// Event is a short snake_case name such as auth_failure, ban, rate_limited.
	Event string
	// IP is the client address as the emitter resolved it (empty when there is none).
	IP string
	// Class is the route or listener class the event happened on.
	Class string
	// Reason is a short constant token naming why (never request data).
	Reason string
	// Count is how many occurrences this stands for (0 means 1).
	Count int
}

// auditBacklog is how many events are kept for an observer that registers late (startup warnings
// are emitted while modules start, possibly before the audit module did).
const auditBacklog = 128

// Audit is the socket modules report security events and request outcomes to. Emitting is cheap and
// never fails; with no observer registered (no audit module in the binary) events are only kept in a
// small ring, so removing the audit module breaks nothing.
type Audit struct {
	mu       sync.RWMutex
	observer []func(AuditEvent)
	requests []func(class string, status int)
	resolve  func(*http.Request) string
	backlog  []AuditEvent
}

// NewAudit creates an empty socket.
func NewAudit() *Audit { return &Audit{} }

// Emit reports ev. Observers run synchronously and must be quick; a panicking observer is contained.
func (a *Audit) Emit(ev AuditEvent) {
	if a == nil {
		return
	}
	if ev.Count <= 0 {
		ev.Count = 1
	}
	a.mu.Lock()
	if len(a.observer) == 0 {
		if len(a.backlog) >= auditBacklog {
			a.backlog = append(a.backlog[:0], a.backlog[1:]...)
		}
		a.backlog = append(a.backlog, ev)
		a.mu.Unlock()
		return
	}
	obs := a.observer
	a.mu.Unlock()
	for _, fn := range obs {
		deliver(fn, ev)
	}
}

func deliver(fn func(AuditEvent), ev AuditEvent) {
	defer func() { _ = recover() }()
	fn(ev)
}

// EmitRequest is Emit for an event caused by request r: ev.IP is filled in with the client address
// as the observer's resolver sees it (the TCP peer when there is no resolver).
func (a *Audit) EmitRequest(r *http.Request, ev AuditEvent) {
	if a == nil {
		return
	}
	if ev.IP == "" {
		ev.IP = a.clientIP(r)
	}
	a.Emit(ev)
}

func (a *Audit) clientIP(r *http.Request) string {
	a.mu.RLock()
	fn := a.resolve
	a.mu.RUnlock()
	if fn != nil {
		return fn(r)
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap().String()
	}
	return ""
}

// Observe registers fn for every event and replays the events that were emitted before the first
// observer existed.
func (a *Audit) Observe(fn func(AuditEvent)) {
	a.mu.Lock()
	a.observer = append(append([]func(AuditEvent){}, a.observer...), fn)
	backlog := a.backlog
	a.backlog = nil
	a.mu.Unlock()
	for _, ev := range backlog {
		deliver(fn, ev)
	}
}

// ObserveRequests registers fn for every finished request (listener class and final status).
func (a *Audit) ObserveRequests(fn func(class string, status int)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(append([]func(string, int){}, a.requests...), fn)
}

// SetClientResolver sets how EmitRequest finds the client address (trusted-proxy aware).
func (a *Audit) SetClientResolver(fn func(*http.Request) string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resolve = fn
}

// observeRequests wraps next so every finished request of class is reported to the request observers.
func (a *Audit) observeRequests(class string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		obs := a.requests
		a.mu.RUnlock()
		if len(obs) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			p := recover()
			status := sw.status
			if p != nil {
				status = http.StatusInternalServerError
			}
			for _, fn := range obs {
				fn(class, status)
			}
			if p != nil {
				panic(p)
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// MetricKind is the Prometheus type of a metric.
type MetricKind string

// Metric kinds.
const (
	Counter MetricKind = "counter"
	Gauge   MetricKind = "gauge"
)

// Metric is one time series a module exposes. Value is called at scrape time and must be quick.
// Series sharing a Name must share Help and Kind and differ in Labels.
type Metric struct {
	Name   string
	Help   string
	Kind   MetricKind
	Labels map[string]string
	Value  func() float64
}

// Metrics is the socket modules register their counters and gauges on; the audit module serves it.
type Metrics struct {
	mu   sync.RWMutex
	list []Metric
}

// NewMetrics creates an empty registry.
func NewMetrics() *Metrics { return &Metrics{} }

// Add registers m. The name must be a lower-case Prometheus name; a nil Value is refused.
func (r *Metrics) Add(m Metric) error {
	if !validMetricName(m.Name) {
		return fmt.Errorf("metric name %q is not a lower-case Prometheus name", m.Name)
	}
	if m.Value == nil || (m.Kind != Counter && m.Kind != Gauge) {
		return fmt.Errorf("metric %q needs a kind and a value function", m.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, m)
	return nil
}

// MustAdd is Add for static, known-good metrics; it panics on a programming error.
func (r *Metrics) MustAdd(m Metric) {
	if err := r.Add(m); err != nil {
		panic(err)
	}
}

// All returns the registered series in registration order.
func (r *Metrics) All() []Metric {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Metric(nil), r.list...)
}

// Value returns the sum of the series called name, and whether there is one.
func (r *Metrics) Value(name string) (float64, bool) {
	var sum float64
	found := false
	for _, m := range r.All() {
		if m.Name == name {
			sum += m.Value()
			found = true
		}
	}
	return sum, found
}

func validMetricName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
