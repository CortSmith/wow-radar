package ratelimit

import (
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"radarlive/internal/core"
)

const healthPath = "/healthz"

// limiterConfig is the validated configuration of a Limiter. ip holds the per-client-address
// budget of every class; key holds the per-API-key budget of the classes that carry a key (a zero
// burst means the class has no key budget).
type limiterConfig struct {
	enabled    bool
	headers    bool
	trusted    []netip.Prefix
	maxEntries int
	ttl        time.Duration
	ip, key    [numClasses]budget
}

// Defaults are sized for a weak laptop and a page that polls about once a minute (rates are per minute).
const (
	defaultMaxEntries = 100_000
	defaultTTL        = 10 * time.Minute
)

var defaultRates = [numClasses]struct{ ipRate, ipBurst, keyRate, keyBurst float64 }{
	classPage:     {120, 60, 0, 0},
	classSweep:    {30, 15, 0, 0},
	classSearch:   {60, 30, 0, 0},
	classWrite:    {1200, 400, 600, 200},
	classIngest:   {1200, 400, 600, 200},
	classAuthFail: {10, 10, 0, 0},
}

func perMinute(n, burst float64) budget {
	if burst == 0 {
		return budget{}
	}
	return budget{rate: n / 60, burst: burst}
}

func defaultConfig() limiterConfig {
	c := limiterConfig{enabled: true, maxEntries: defaultMaxEntries, ttl: defaultTTL}
	for i, r := range defaultRates {
		c.ip[i] = perMinute(r.ipRate, r.ipBurst)
		c.key[i] = perMinute(r.keyRate, r.keyBurst)
	}
	return c
}

// Limiter applies the budgets to HTTP requests.
type Limiter struct {
	cfg   limiterConfig
	table *table
	now   func() time.Time
	audit *core.Audit // optional: refusals are reported as rate_limited events
}

func newLimiter(cfg limiterConfig, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{cfg: cfg, table: newTable(cfg.maxEntries, cfg.ttl), now: now}
}

// classify maps a request to its route class and says whether the class is keyed by API key too.
func classify(r *http.Request) (class, bool) {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/internal/"):
		return classWrite, true
	case p == "/v1/stream" || p == "/v1/lines" || p == "/v1/sources":
		return classIngest, true
	case p == "/api/v1/sweep":
		return classSweep, false
	case p == "/api/v1/status":
		return classPage, false
	case strings.HasPrefix(p, "/api/"):
		return classSearch, false
	}
	return classPage, false
}

// Wrap returns next guarded by the limiter. With the limiter disabled it returns next unchanged.
func (l *Limiter) Wrap(next http.Handler) http.Handler {
	if !l.cfg.enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r, l.cfg.trusted)
		if r.URL.Path == healthPath && ip.IsLoopback() {
			next.ServeHTTP(w, r)
			return
		}
		now := l.now()
		ipk := entryKey{kind: kindIP, id: ipKey(ip)}

		// A client that spent its failed-authentication budget is refused before anything else.
		failKey := ipk
		failKey.class = classAuthFail
		if d := l.table.take(failKey, l.cfg.ip[classAuthFail], now, false); !d.allowed {
			l.deny(w, r, d, ip, classNames[classAuthFail], "authfail_budget")
			return
		}

		cls, keyed := classify(r)
		ipk.class = cls
		d := l.table.take(ipk, l.cfg.ip[cls], now, true)
		if !d.allowed {
			l.deny(w, r, d, ip, classNames[cls], "ip_budget")
			return
		}
		if raw, ok := presentedKey(r); ok && keyed && l.cfg.key[cls].burst > 0 {
			kd := l.table.take(entryKey{class: cls, kind: kindKey, id: keyID(raw)}, l.cfg.key[cls], now, true)
			if !kd.allowed {
				l.deny(w, r, kd, ip, classNames[cls], "key_budget")
				return
			}
			if kd.remaining < d.remaining {
				d = kd
			}
		}
		if l.cfg.headers {
			setRateHeaders(w, d)
		}
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
			l.table.take(failKey, l.cfg.ip[classAuthFail], l.now(), true)
		}
	})
}

func (l *Limiter) deny(w http.ResponseWriter, r *http.Request, d decision, ip netip.Addr, class, reason string) {
	ev := core.AuditEvent{Event: "rate_limited", Class: class, Reason: reason}
	if ip.IsValid() {
		ev.IP = ip.String()
		l.audit.Emit(ev)
	} else {
		l.audit.EmitRequest(r, ev)
	}
	secs := int(d.retry / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	if l.cfg.headers {
		setRateHeaders(w, d)
		w.Header().Set("X-RateLimit-Reset", strconv.Itoa(secs))
	}
	core.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests, retry later")
}

func setRateHeaders(w http.ResponseWriter, d decision) {
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(d.burst))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(d.remaining))
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
