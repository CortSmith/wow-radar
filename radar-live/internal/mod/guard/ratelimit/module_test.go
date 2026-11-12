package ratelimit_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/mod/guard/limits"
	"radarlive/internal/mod/guard/ratelimit"
)

// routes is a stand-in feature module.
type routes struct{}

func (routes) Name() string { return "routes" }

func (routes) Init(h *core.Host) error {
	ok := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }
	if err := h.Routes.HandleFunc(http.MethodGet, "/", ok); err != nil {
		return err
	}
	return h.Routes.HandleFunc(http.MethodGet, "/api/v1/sweep", ok)
}

type running struct {
	host *core.Host
	stop func()
}

func start(t *testing.T, mods []core.Module, args ...string) *running {
	t.Helper()
	host := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := host.Register(mods...); err != nil {
		t.Fatal(err)
	}
	args = append([]string{"--listen", "127.0.0.1:0", "--shutdown-timeout", "2s"}, args...)
	if err := host.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Run(ctx) }()
	select {
	case <-host.Ready():
	case err := <-done:
		cancel()
		t.Fatalf("host stopped early: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("host never became ready")
	}
	r := &running{host: host, stop: func() { cancel(); <-done }}
	t.Cleanup(r.stop)
	return r
}

func (r *running) get(t *testing.T, path string) *http.Response {
	t.Helper()
	resp, err := http.Get("http://" + r.host.Addr() + path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func TestModuleLimitsRealRequests(t *testing.T) {
	r := start(t, []core.Module{ratelimit.New(), limits.New(), routes{}},
		"--ratelimit-sweep-rate", "30", "--ratelimit-sweep-burst", "2", "--ratelimit-headers=true")
	for i := 0; i < 2; i++ {
		if resp := r.get(t, "/api/v1/sweep"); resp.StatusCode != 200 {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
	}
	resp := r.get(t, "/api/v1/sweep")
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status %d Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if resp := r.get(t, "/healthz"); resp.StatusCode != 200 {
		t.Fatalf("loopback /healthz: %d", resp.StatusCode)
	}
}

func TestKillSwitch(t *testing.T) {
	r := start(t, []core.Module{ratelimit.New(), routes{}},
		"--ratelimit-enabled=false", "--ratelimit-sweep-rate", "1", "--ratelimit-sweep-burst", "1")
	for i := 0; i < 10; i++ {
		if resp := r.get(t, "/api/v1/sweep"); resp.StatusCode != 200 {
			t.Fatalf("disabled limiter returned %d", resp.StatusCode)
		}
	}
}

func TestBadConfigStopsTheHost(t *testing.T) {
	for _, args := range [][]string{
		{"--ratelimit-sweep-rate", "0"},
		{"--ratelimit-sweep-burst", "-1"},
		{"--ratelimit-max-entries", "0"},
		{"--ratelimit-ttl", "0s"},
		{"--ratelimit-trusted-proxies", "not-a-cidr"},
	} {
		host := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		if err := host.Register(ratelimit.New(), routes{}); err != nil {
			t.Fatal(err)
		}
		if err := host.Config.Load(append([]string{"--listen", "127.0.0.1:0"}, args...), func(string) (string, bool) { return "", false }); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := host.Run(ctx)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "ratelimit") {
			t.Errorf("%v: Run returned %v, want a ratelimit config error", args, err)
		}
	}
}

func TestEnvNames(t *testing.T) {
	cfg := core.NewConfig("RADAR")
	for key, want := range map[string]string{
		"ratelimit-enabled":         "RADAR_RATELIMIT_ENABLED",
		"ratelimit-trusted-proxies": "RADAR_RATELIMIT_TRUSTED_PROXIES",
		"ratelimit-write-rate":      "RADAR_RATELIMIT_WRITE_RATE",
	} {
		if got := cfg.EnvName(key); got != want {
			t.Errorf("%s -> %s, want %s", key, got, want)
		}
	}
}

func TestHostWorksWithoutRatelimit(t *testing.T) {
	r := start(t, []core.Module{limits.New(), routes{}})
	for i := 0; i < 200; i++ {
		if resp := r.get(t, "/api/v1/sweep"); resp.StatusCode != 200 {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
	}
}

func TestRatelimitModuleIsRemovable(t *testing.T) {
	m := ratelimit.New()
	if m.Name() != ratelimit.ModuleName {
		t.Fatalf("name %q", m.Name())
	}
}

func TestRefusalsAreAuditedWithResolvedAddressAndClass(t *testing.T) {
	r := start(t, []core.Module{ratelimit.New(), routes{}},
		"--ratelimit-sweep-rate", "30", "--ratelimit-sweep-burst", "1")
	r.get(t, "/api/v1/sweep")
	if resp := r.get(t, "/api/v1/sweep?key=test-key-0000000000000000"); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", resp.StatusCode)
	}
	var got []core.AuditEvent
	r.host.Audit.Observe(func(ev core.AuditEvent) { got = append(got, ev) })
	if len(got) != 1 || got[0].Event != "rate_limited" || got[0].IP != "127.0.0.1" || got[0].Class != "sweep" || got[0].Reason != "ip_budget" {
		t.Fatalf("events = %+v", got)
	}
}

func TestDisabledLimiterWarnsThroughAudit(t *testing.T) {
	r := start(t, []core.Module{ratelimit.New(), routes{}}, "--ratelimit-enabled=false")
	var got []core.AuditEvent
	r.host.Audit.Observe(func(ev core.AuditEvent) { got = append(got, ev) })
	if len(got) != 1 || got[0].Event != "config_warning" || got[0].Reason != "ratelimit_disabled" {
		t.Fatalf("events = %+v", got)
	}
}
