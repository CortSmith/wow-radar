package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuditWithoutObserverIsHarmlessAndBounded(t *testing.T) {
	a := NewAudit()
	for i := 0; i < auditBacklog*3; i++ {
		a.Emit(AuditEvent{Event: "x"})
	}
	var got int
	a.Observe(func(AuditEvent) { got++ })
	if got != auditBacklog {
		t.Fatalf("replayed %d events, want the backlog cap %d", got, auditBacklog)
	}
	a.Emit(AuditEvent{Event: "y"})
	if got != auditBacklog+1 {
		t.Fatalf("live event not delivered, got %d", got)
	}
}

func TestAuditCountDefaultsToOneAndObserverPanicIsContained(t *testing.T) {
	a := NewAudit()
	var seen []AuditEvent
	a.Observe(func(AuditEvent) { panic("boom") })
	a.Observe(func(ev AuditEvent) { seen = append(seen, ev) })
	a.Emit(AuditEvent{Event: "ban", Count: -3})
	if len(seen) != 1 || seen[0].Count != 1 {
		t.Fatalf("seen = %+v", seen)
	}
}

func TestAuditEmitRequestUsesResolverOrPeer(t *testing.T) {
	a := NewAudit()
	var got []AuditEvent
	a.Observe(func(ev AuditEvent) { got = append(got, ev) })
	r := httptest.NewRequest("GET", "/x?key=SECRET", nil)
	r.RemoteAddr = "[::ffff:192.0.2.9]:5555"
	a.EmitRequest(r, AuditEvent{Event: "e"})
	a.SetClientResolver(func(*http.Request) string { return "198.51.100.1" })
	a.EmitRequest(r, AuditEvent{Event: "e"})
	a.EmitRequest(r, AuditEvent{Event: "e", IP: "203.0.113.5"})
	if got[0].IP != "192.0.2.9" || got[1].IP != "198.51.100.1" || got[2].IP != "203.0.113.5" {
		t.Fatalf("ips = %q %q %q", got[0].IP, got[1].IP, got[2].IP)
	}
}

func TestAuditObservesRequestStatusIncludingPanics(t *testing.T) {
	a := NewAudit()
	type seen struct {
		class  string
		status int
	}
	var got []seen
	a.ObserveRequests(func(c string, s int) { got = append(got, seen{c, s}) })
	h := a.observeRequests("public", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/429":
			w.WriteHeader(http.StatusTooManyRequests)
		case "/panic":
			panic("x")
		}
	}))
	for _, p := range []string{"/ok", "/429"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	func() {
		defer func() { _ = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/panic", nil))
	}()
	want := []seen{{"public", 200}, {"public", 429}, {"public", 500}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMetricsRegistryValidatesAndSums(t *testing.T) {
	m := NewMetrics()
	if err := m.Add(Metric{Name: "Bad-Name", Kind: Gauge, Value: func() float64 { return 1 }}); err == nil {
		t.Fatal("bad name accepted")
	}
	if err := m.Add(Metric{Name: "radar_x", Kind: Gauge}); err == nil {
		t.Fatal("nil value accepted")
	}
	m.MustAdd(Metric{Name: "radar_x", Kind: Counter, Labels: map[string]string{"a": "1"}, Value: func() float64 { return 2 }})
	m.MustAdd(Metric{Name: "radar_x", Kind: Counter, Labels: map[string]string{"a": "2"}, Value: func() float64 { return 3 }})
	if v, ok := m.Value("radar_x"); !ok || v != 5 {
		t.Fatalf("Value = %v %v", v, ok)
	}
	if _, ok := m.Value("missing"); ok {
		t.Fatal("missing metric found")
	}
}
