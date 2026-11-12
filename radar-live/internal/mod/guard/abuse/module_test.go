package abuse_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/mod/guard/abuse"
)

const goodKey = "good-key-0000000000"

// app is a stand-in feature module: a view-protected route with one valid key, and an internal route.
type app struct{}

func (app) Name() string { return "app" }

func (app) Init(h *core.Host) error {
	h.Auth.Add(core.RoleView, core.AuthenticatorFunc(func(r *http.Request) bool { return r.Header.Get("X-View-Key") == goodKey }))
	ok := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }
	if err := h.Routes.HandleFunc(http.MethodGet, "/api/v1/sweep", h.Auth.Require(core.RoleView, http.HandlerFunc(ok)).ServeHTTP); err != nil {
		return err
	}
	if err := h.EnsureListener(core.ClassInternal, "internal-listen", "127.0.0.1:0", "internal"); err != nil {
		return err
	}
	return h.Routes.HandleFuncOn(core.ClassInternal, http.MethodGet, "/internal/x", ok)
}

type running struct {
	host *core.Host
	stop func()
}

func startHost(t *testing.T, mods []core.Module, args ...string) (*running, error) {
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
		return nil, err
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("host never became ready")
	}
	r := &running{host: host, stop: func() { cancel(); <-done }}
	return r, nil
}

func get(t *testing.T, addr, path, key string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
	if key != "" {
		req.Header.Set("X-View-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestModuleBansRealClientAndPersists(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bans.json")
	r, err := startHost(t, []core.Module{abuse.New(abuse.WithFlushInterval(10 * time.Millisecond)), app{}},
		"--abuse-ban-loopback=true", "--abuse-ban-file", file)
	if err != nil {
		t.Fatal(err)
	}
	addr := r.host.Addr()
	for i := 0; i < 5; i++ {
		if c := get(t, addr, "/api/v1/sweep", "bad-key-0000000000"); c != 401 {
			t.Fatalf("attempt %d: %d", i, c)
		}
	}
	if c := get(t, addr, "/api/v1/sweep", goodKey); c != http.StatusTooManyRequests {
		t.Fatalf("banned: %d", c)
	}
	r.stop() // flushes the ban file

	data, err := os.ReadFile(file)
	if err != nil || len(data) == 0 {
		t.Fatalf("ban file: %v", err)
	}
	r2, err := startHost(t, []core.Module{abuse.New(), app{}}, "--abuse-ban-loopback=true", "--abuse-ban-file", file)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.stop()
	if c := get(t, r2.host.Addr(), "/api/v1/sweep", goodKey); c != http.StatusTooManyRequests {
		t.Fatalf("ban must survive a restart: %d", c)
	}
}

func TestModulePicksUpAdminUnban(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bans.json")
	r, err := startHost(t, []core.Module{abuse.New(abuse.WithFlushInterval(10 * time.Millisecond)), app{}},
		"--abuse-ban-loopback=true", "--abuse-ban-file", file, "--abuse-max-failures", "1")
	if err != nil {
		t.Fatal(err)
	}
	defer r.stop()
	get(t, r.host.Addr(), "/api/v1/sweep", "bad-key-0000000000")
	if c := get(t, r.host.Addr(), "/api/v1/sweep", goodKey); c != 429 {
		t.Fatalf("banned: %d", c)
	}
	// Wait for the server's own write, then unban through the admin function like the CLI does.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if bans, _ := abuse.LoadBans(file, time.Now()); len(bans) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ban file never written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := abuse.Admin([]string{"unban", "127.0.0.1"}, file, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	for deadline = time.Now().Add(5 * time.Second); ; {
		if get(t, r.host.Addr(), "/api/v1/sweep", goodKey) == 200 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("running server never noticed the unban")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCorruptBanFileStartsEmptyAndIsKept(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bans.json")
	if err := os.WriteFile(file, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := startHost(t, []core.Module{abuse.New(), app{}}, "--abuse-ban-file", file)
	if err != nil {
		t.Fatalf("a corrupt ban file must not stop the host: %v", err)
	}
	defer r.stop()
	if c := get(t, r.host.Addr(), "/api/v1/sweep", goodKey); c != 200 {
		t.Fatalf("%d", c)
	}
	if data, err := os.ReadFile(file + ".corrupt"); err != nil || string(data) != "{broken" {
		t.Fatalf("corrupt file should be kept for inspection: %v %q", err, data)
	}
}

func TestBadConfigStopsTheHost(t *testing.T) {
	for _, args := range [][]string{
		{"--abuse-max-failures", "0"}, {"--abuse-window", "0s"}, {"--abuse-ban-base", "0s"},
		{"--abuse-ban-base", "2h", "--abuse-ban-max", "1h"}, {"--abuse-max-entries", "0"},
		{"--abuse-trusted-proxies", "not-a-cidr"}, {"--abuse-internal-allow", "x/y"},
	} {
		if _, err := startHost(t, []core.Module{abuse.New(), app{}}, args...); err == nil {
			t.Errorf("%v should stop the host", args)
		}
	}
}

func TestKillSwitch(t *testing.T) {
	r, err := startHost(t, []core.Module{abuse.New(), app{}}, "--abuse-enabled=false", "--abuse-ban-loopback=true", "--abuse-max-failures", "1")
	if err != nil {
		t.Fatal(err)
	}
	defer r.stop()
	for i := 0; i < 5; i++ {
		get(t, r.host.Addr(), "/api/v1/sweep", "bad-key-0000000000")
	}
	if c := get(t, r.host.Addr(), "/api/v1/sweep", goodKey); c != 200 {
		t.Fatalf("disabled module must not ban: %d", c)
	}
}

func TestLoopbackClientsAreNeverBannedByDefault(t *testing.T) {
	r, err := startHost(t, []core.Module{abuse.New(), app{}}, "--abuse-max-failures", "1")
	if err != nil {
		t.Fatal(err)
	}
	defer r.stop()
	for i := 0; i < 5; i++ {
		get(t, r.host.Addr(), "/api/v1/sweep", "bad-key-0000000000")
	}
	if c := get(t, r.host.Addr(), "/api/v1/sweep", goodKey); c != 200 {
		t.Fatalf("loopback was banned: %d", c)
	}
}

// TestHostWorksWithoutAbuse is the removal test: the same app runs with the module left out.
func TestHostWorksWithoutAbuse(t *testing.T) {
	r, err := startHost(t, []core.Module{app{}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.stop()
	for i := 0; i < 10; i++ {
		get(t, r.host.Addr(), "/api/v1/sweep", "bad-key-0000000000")
	}
	if c := get(t, r.host.Addr(), "/api/v1/sweep", goodKey); c != 200 {
		t.Fatalf("%d", c)
	}
}

func TestModuleName(t *testing.T) {
	if abuse.New().Name() != "abuse" {
		t.Fatal("name")
	}
}

func TestInternalListenerStillServesLoopbackPeers(t *testing.T) {
	r, err := startHost(t, []core.Module{abuse.New(), app{}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.stop()
	if c := get(t, r.host.ClassAddr(core.ClassInternal), "/internal/x", ""); c != 200 {
		t.Fatalf("loopback peer on the internal listener: %d", c)
	}
}

type eventLog struct {
	mu  sync.Mutex
	got []core.AuditEvent
}

func (e *eventLog) add(ev core.AuditEvent) { e.mu.Lock(); e.got = append(e.got, ev); e.mu.Unlock() }
func (e *eventLog) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.got {
		out = append(out, ev.Event+"/"+ev.Reason+"/"+ev.IP)
	}
	return out
}

func TestAuthFailuresAndBansAreAudited(t *testing.T) {
	r, err := startHost(t, []core.Module{abuse.New(), app{}}, "--abuse-ban-loopback=true", "--abuse-max-failures", "2")
	if err != nil {
		t.Fatal(err)
	}
	defer r.stop()
	var log eventLog
	r.host.Audit.Observe(log.add)
	get(t, r.host.Addr(), "/api/v1/sweep", "bad-key-0000000000")
	get(t, r.host.Addr(), "/api/v1/sweep", "bad-key-0000000000")
	want := []string{"auth_failure/bad_key/127.0.0.1", "auth_failure/bad_key/127.0.0.1", "ban/lockout/127.0.0.1"}
	if got := log.names(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if v, _ := r.host.Metrics.Value("radar_bans_active"); v != 1 {
		t.Fatalf("radar_bans_active = %v", v)
	}
}

func TestAdminUnbanIsAudited(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bans.json")
	r, err := startHost(t, []core.Module{abuse.New(abuse.WithFlushInterval(10 * time.Millisecond)), app{}},
		"--abuse-ban-loopback=true", "--abuse-ban-file", file, "--abuse-max-failures", "1")
	if err != nil {
		t.Fatal(err)
	}
	defer r.stop()
	var log eventLog
	r.host.Audit.Observe(log.add)
	get(t, r.host.Addr(), "/api/v1/sweep", "bad-key-0000000000")
	for deadline := time.Now().Add(3 * time.Second); ; {
		if bans, _ := abuse.LoadBans(file, time.Now()); len(bans) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ban file never written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := abuse.Admin([]string{"unban", "127.0.0.1"}, file, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		if slices.Contains(log.names(), "unban/admin/127.0.0.1") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no unban event: %v", log.names())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDisabledGuardWarnsThroughAudit(t *testing.T) {
	r, err := startHost(t, []core.Module{abuse.New(), app{}}, "--abuse-enabled=false")
	if err != nil {
		t.Fatal(err)
	}
	defer r.stop()
	var log eventLog
	r.host.Audit.Observe(log.add)
	if got := log.names(); len(got) != 1 || got[0] != "config_warning/abuse_disabled/" {
		t.Fatalf("events = %v", got)
	}
}
