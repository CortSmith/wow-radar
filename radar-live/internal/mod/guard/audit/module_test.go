package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
)

// syncBuf is a goroutine-safe buffer for the log output.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// emitter is a stand-in feature module: it reports events through the core socket only.
type emitter struct{ host *core.Host }

func (*emitter) Name() string { return "emitter" }

func (e *emitter) Init(h *core.Host) error {
	e.host = h
	if err := h.EnsureListener(core.ClassInternal, "internal-listen", "", "internal"); err != nil {
		return err
	}
	h.Metrics.MustAdd(core.Metric{Name: "radar_relay_dropped_total", Help: "d", Kind: core.Counter, Value: func() float64 { return 0 }})
	return h.Routes.HandleFunc(http.MethodPost, "/emit", func(w http.ResponseWriter, r *http.Request) {
		h.Audit.EmitRequest(r, core.AuditEvent{Event: "auth_failure", Class: "public", Reason: "bad_key"})
		w.WriteHeader(http.StatusUnauthorized)
	})
}

type running struct {
	h      *core.Host
	pub    string
	intl   string
	cancel context.CancelFunc
	done   chan error
}

func start(t *testing.T, env map[string]string, mods ...core.Module) *running {
	t.Helper()
	h := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := h.Register(mods...); err != nil {
		t.Fatal(err)
	}
	args := []string{"--listen", "127.0.0.1:0", "--internal-listen", "127.0.0.1:0", "--require-keys", "false"}
	if err := h.Config.Load(args, func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{h: h, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- h.Run(ctx) }()
	select {
	case <-h.Ready():
	case err := <-r.done:
		t.Fatalf("host exited: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	r.pub, r.intl = "http://"+h.Addr(), "http://"+h.ClassAddr(core.ClassInternal)
	t.Cleanup(func() { r.stop(t) })
	return r
}

func (r *running) stop(t *testing.T) {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Error("host did not stop")
	}
	r.cancel = func() {}
	r.done = make(chan error, 1)
	r.done <- nil
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func jsonLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func TestEventsBecomeSampledJSONLines(t *testing.T) {
	buf := &syncBuf{}
	r := start(t, nil, &emitter{}, New(WithWriter(buf)))
	for i := 0; i < 10; i++ {
		resp, err := http.Post(r.pub+"/emit", "text/plain", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	r.h.Audit.Emit(core.AuditEvent{Event: "ban", IP: "198.51.100.4", Class: "public", Reason: "lockout"})
	r.h.Audit.Emit(core.AuditEvent{Event: "integrity_degraded", Reason: "quick_check"})
	lines := jsonLines(t, buf.String())
	var auth []map[string]any
	for _, l := range lines {
		if l["event"] == "auth_failure" {
			auth = append(auth, l)
		}
	}
	if len(auth) != 1 {
		t.Fatalf("10 failures from one address gave %d auth_failure lines:\n%s", len(auth), buf.String())
	}
	if auth[0]["ip"] != "127.0.0.1" || auth[0]["class"] != "public" || auth[0]["reason"] != "bad_key" || auth[0]["count"] != float64(1) || auth[0]["ts"] == nil {
		t.Fatalf("line = %v", auth[0])
	}
	if !strings.Contains(buf.String(), `"event":"ban","ip":"198.51.100.4"`) || !strings.Contains(buf.String(), `"event":"integrity_degraded"`) {
		t.Fatalf("missing lines:\n%s", buf.String())
	}
	r.stop(t)
	var total float64
	for _, l := range jsonLines(t, buf.String()) {
		if l["event"] == "auth_failure" {
			total += l["count"].(float64)
		}
	}
	if total != 10 {
		t.Fatalf("counts add up to %v, want 10 (summary line on stop)", total)
	}
}

func TestMetricsOnInternalListenerOnly(t *testing.T) {
	r := start(t, nil, &emitter{}, New(WithWriter(&syncBuf{})))
	for i := 0; i < 3; i++ {
		resp, _ := http.Post(r.pub+"/emit", "text/plain", nil)
		resp.Body.Close()
	}
	if code, _ := get(t, r.pub+"/internal/metrics"); code != http.StatusNotFound {
		t.Fatalf("public listener serves /internal/metrics: %d", code)
	}
	waitFor(t, func() bool {
		_, b := get(t, r.intl+"/internal/metrics")
		return strings.Contains(b, `class="public",code="4xx"} 4`)
	})
	code, body := get(t, r.intl+"/internal/metrics")
	if code != http.StatusOK {
		t.Fatalf("internal metrics: %d", code)
	}
	for _, want := range []string{
		"# TYPE radar_auth_failures_total counter", "radar_auth_failures_total 3",
		`radar_security_events_total{event="auth_failure"} 3`,
		`radar_http_requests_total{class="public",code="4xx"} 4`, // 3 emits + the 404 on the public listener
		"radar_bans_total 0", "radar_http_429_total 0", "radar_audit_log_lines_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	if resp, err := http.Post(r.intl+"/internal/metrics", "text/plain", nil); err == nil {
		if resp.Body.Close(); resp.StatusCode == http.StatusOK {
			t.Fatal("POST accepted on metrics")
		}
	}
}

func TestMetricsRenderFormat(t *testing.T) {
	out := renderMetrics([]core.Metric{
		{Name: "b_total", Help: "B help\nline", Kind: core.Counter, Labels: map[string]string{"z": `a"b\c`, "a": "x\ny"}, Value: func() float64 { return 2 }},
		{Name: "a_gauge", Help: "A", Kind: core.Gauge, Value: func() float64 { return 1.5 }},
		{Name: "b_total", Help: "B help\nline", Kind: core.Counter, Labels: map[string]string{"z": "2", "a": "1"}, Value: func() float64 { return 3 }},
	})
	want := "# HELP a_gauge A\n# TYPE a_gauge gauge\na_gauge 1.5\n" +
		"# HELP b_total B help\\nline\n# TYPE b_total counter\n" +
		"b_total{a=\"x\\ny\",z=\"a\\\"b\\\\c\"} 2\nb_total{a=\"1\",z=\"2\"} 3\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestLogNeverHoldsRequestSecrets(t *testing.T) {
	const key = "test-key-0000000000000000"
	buf := &syncBuf{}
	r := start(t, nil, &emitter{}, New(WithWriter(buf)))
	req, _ := http.NewRequest(http.MethodPost, r.pub+"/emit?key="+key+"&view_key="+key, strings.NewReader(`{"key":"`+key+`"}`))
	req.Header.Set("X-View-Key", key)
	req.Header.Set("X-Ingest-Key", key)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Cookie", "s="+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	r.stop(t)
	body := buf.String()
	if !strings.Contains(body, "auth_failure") {
		t.Fatalf("no line written: %q", body)
	}
	if strings.Contains(body, key) || strings.Contains(body, "test-key") {
		t.Fatalf("a secret reached the security log:\n%s", body)
	}
	metrics := renderMetrics(r.h.Metrics.All())
	if strings.Contains(metrics, "test-key") {
		t.Fatal("a secret reached the metrics")
	}
}

func TestAlertsFireOnThresholdsWithFakeRunner(t *testing.T) {
	f := &fakeRun{}
	r := start(t, map[string]string{"RADAR_ALERT_CMD": `notify --kind {kind} "a b"`, "RADAR_ALERT_BANS": "3"},
		&emitter{}, New(WithWriter(&syncBuf{}), WithRunner(f.run)))
	for i := 0; i < 2; i++ {
		r.h.Audit.Emit(core.AuditEvent{Event: "ban", IP: "198.51.100.4"})
	}
	if n := alertCalls(f); n != 0 {
		t.Fatalf("alert before threshold: %d", n)
	}
	r.h.Audit.Emit(core.AuditEvent{Event: "ban", IP: "198.51.100.5"})
	r.h.Audit.Emit(core.AuditEvent{Event: "ban", IP: "198.51.100.6"}) // inside the cooldown
	r.h.Audit.Emit(core.AuditEvent{Event: "integrity_degraded"})
	r.h.Audit.Emit(core.AuditEvent{Event: "disk_full", Reason: "disk_low"})
	waitFor(t, func() bool { return alertCalls(f) == 3 })
	f.mu.Lock()
	defer f.mu.Unlock()
	kinds := map[string]bool{}
	for _, c := range f.calls {
		if c[0] != "notify" || c[1] != "--kind" || c[3] != "a b" {
			t.Fatalf("argv = %q", c)
		}
		kinds[c[2]] = true
	}
	if !kinds[AlertBanSurge] || !kinds[AlertDBDegraded] || !kinds[AlertDiskLow] {
		t.Fatalf("kinds = %v", kinds)
	}
}

func alertCalls(f *fakeRun) int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestRelayDropsAlertUsesCounterDelta(t *testing.T) {
	c := newClock()
	f := &fakeRun{}
	m := &Module{alert: newAlerter([]string{"x"}, f.run, c.now, 0), dropLim: 100, banWin: 10 * time.Minute}
	m.checkDrops(c.now(), 1000) // baseline
	c.advance(time.Minute)
	m.checkDrops(c.now(), 1050)
	m.alert.wait()
	if alertCalls(f) != 0 {
		t.Fatal("alert below the threshold")
	}
	c.advance(time.Minute)
	m.checkDrops(c.now(), 1100)
	m.alert.wait()
	if alertCalls(f) != 1 {
		t.Fatalf("alerts = %d, want 1 at 100 drops", alertCalls(f))
	}
}

func TestBadAlertCommandRefusesStart(t *testing.T) {
	h := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	m := New(WithWriter(&syncBuf{}))
	if err := h.Register(m); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"RADAR_ALERT_CMD": `notify "unterminated`}
	if err := h.Config.Load([]string{"--listen", "127.0.0.1:0"}, func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	err := m.Start(context.Background())
	if err == nil || strings.Contains(err.Error(), "notify") {
		t.Fatalf("Start error = %v (must fail without echoing the command)", err)
	}
}

func TestFileLogIsPrivateAndRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sec", "security.log")
	r := start(t, map[string]string{"RADAR_AUDIT_LOG": path, "RADAR_AUDIT_MAX_MB": "1", "RADAR_AUDIT_KEEP": "2"}, &emitter{}, New())
	for i := 0; i < 400; i++ {
		r.h.Audit.Emit(core.AuditEvent{Event: "rate_limited", IP: "10.1." + strconv.Itoa(i/250) + "." + strconv.Itoa(i%250+1), Class: "sweep", Reason: strings.Repeat("r", 40)})
	}
	r.stop(t)
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), `"event":"rate_limited"`) {
		t.Fatalf("log: %v %q", err, b)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode().Perm())
		}
	}
}

func TestNothingBreaksWithoutTheAuditModule(t *testing.T) {
	r := start(t, nil, &emitter{})
	resp, err := http.Post(r.pub+"/emit", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if code, _ := get(t, r.intl+"/internal/metrics"); code != http.StatusNotFound {
		t.Fatalf("metrics without the module: %d", code)
	}
}
