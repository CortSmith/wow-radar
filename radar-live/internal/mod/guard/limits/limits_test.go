package limits_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/mod/guard/limits"
)

// routes is a stand-in feature module: it registers the handlers the attacks aim at.
type routes struct{ block chan struct{} }

func (routes) Name() string { return "routes" }

func (r routes) Init(h *core.Host) error {
	for path, fn := range map[string]http.HandlerFunc{
		"/ok":              func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) },
		"/internal/v1/in":  r.echoSize,
		"/api/v1/in":       r.echoSize,
		"/slow":            r.slow,
		"/v1/stream":       r.stream,
		"/v1/stream/other": r.echoSize,
	} {
		if err := h.Routes.HandleFunc(http.MethodPost, path, fn); err != nil {
			return err
		}
	}
	return h.Routes.HandleFunc(http.MethodGet, "/ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
}

// echoSize reads the body and answers 200 with its size, or 413 when the body was over the cap.
func (routes) echoSize(w http.ResponseWriter, r *http.Request) {
	n, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	fmt.Fprint(w, n)
}

// slow holds the request until the test releases it.
func (r routes) slow(w http.ResponseWriter, _ *http.Request) {
	<-r.block
	_, _ = w.Write([]byte("done"))
}

// stream reads the body to its end and reports the byte count and the read error, if any.
func (routes) stream(w http.ResponseWriter, r *http.Request) {
	n, err := io.Copy(io.Discard, r.Body)
	fmt.Fprintf(w, "n=%d err=%v", n, err != nil)
}

type rig struct {
	t     *testing.T
	host  *core.Host
	block chan struct{}
	stop  func() error
	tuned tuned
}

// tuned records what the limits tuner set on the server.
type tuned struct {
	readHeader, read, write, idle time.Duration
	maxHeader                     int
}

func (r *rig) addr() string { return r.host.Addr() }

func (r *rig) url(path string) string { return "http://" + r.addr() + path }

// start runs a real host with the limits module and the routes module.
func start(t *testing.T, args ...string) *rig {
	t.Helper()
	r := &rig{t: t, block: make(chan struct{})}
	r.host = core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := r.host.Register(limits.New(), routes{block: r.block}); err != nil {
		t.Fatal(err)
	}
	r.host.Server.Tune(func(_ string, srv *http.Server) {
		r.tuned = tuned{srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout, srv.MaxHeaderBytes}
	})
	args = append([]string{"--listen", "127.0.0.1:0", "--shutdown-timeout", "2s"}, args...)
	if err := r.host.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.host.Run(ctx) }()
	select {
	case <-r.host.Ready():
	case err := <-done:
		cancel()
		t.Fatalf("host stopped early: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("host never became ready")
	}
	var once sync.Once
	r.stop = func() (err error) {
		once.Do(func() { cancel(); err = <-done })
		return err
	}
	t.Cleanup(func() { close(r.block); _ = r.stop() })
	return r
}

func (r *rig) dial() net.Conn {
	r.t.Helper()
	c, err := net.DialTimeout("tcp", r.addr(), 2*time.Second)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = c.Close() })
	return c
}

// closedWithin reports whether the server closes c (EOF or reset) within d without a response.
func closedWithin(c net.Conn, d time.Duration) (closed bool, got string) {
	_ = c.SetReadDeadline(time.Now().Add(d))
	b, err := io.ReadAll(c)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return false, string(b)
	}
	return true, string(b)
}

func TestServerTimeouts(t *testing.T) {
	r := start(t)
	want := tuned{readHeader: 5 * time.Second, read: 15 * time.Second, write: 30 * time.Second,
		idle: 60 * time.Second, maxHeader: 16 << 10}
	if r.tuned != want {
		t.Fatalf("server tuned to %+v, want %+v", r.tuned, want)
	}
	if r.host.Config.Int(limits.SettingMaxConns) != 512 || r.host.Config.Duration(limits.SettingStreamIdle) != 60*time.Second {
		t.Fatal("connection or stream idle default wrong")
	}
}

func TestSlowlorisHeaderDribbleIsCut(t *testing.T) {
	r := start(t, "--limits-read-header-timeout", "300ms")
	c := r.dial()
	_, _ = c.Write([]byte("GET /ok HTTP/1.1\r\nHost: x\r\n"))
	for i := 0; i < 3; i++ { // dribble one header byte at a time, slower than the deadline overall
		time.Sleep(150 * time.Millisecond)
		if _, err := c.Write([]byte("X")); err != nil {
			break
		}
	}
	if closed, _ := closedWithin(c, 2*time.Second); !closed {
		t.Fatal("slowloris connection was kept open")
	}
}

func TestSlowBodyIsCutByReadTimeout(t *testing.T) {
	r := start(t, "--limits-read-timeout", "300ms")
	c := r.dial()
	_, _ = c.Write([]byte("POST /internal/v1/in HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nabc"))
	if closed, _ := closedWithin(c, 2*time.Second); !closed {
		t.Fatal("slow POST body was kept open")
	}
}

func TestHugeHeadersRejected(t *testing.T) {
	r := start(t)
	c := r.dial()
	req := "GET /ok HTTP/1.1\r\nHost: x\r\nX-Pad: " + strings.Repeat("a", 64<<10) + "\r\n\r\n"
	_, _ = c.Write([]byte(req))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err == nil && !strings.Contains(line, "431") {
		t.Fatalf("status line %q, want 431", line)
	}
	// A normal request still works afterwards.
	resp, err := http.Get(r.url("/ok"))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("server unhealthy after oversize header: %v", err)
	}
	resp.Body.Close()
}

func TestConnectionCap(t *testing.T) {
	r := start(t, "--limits-max-conns", "2")
	a, b := r.dial(), r.dial()
	waitFor(t, func() bool { return status(t, a) == 200 && status(t, b) == 200 })
	third := r.dial()
	if closed, _ := closedWithin(third, 1*time.Second); !closed {
		t.Fatal("third connection should be refused")
	}
	_ = a.Close() // frees a slot
	waitFor(t, func() bool {
		c := r.dial()
		return status(t, c) == 200
	})
	if status(t, b) != 200 {
		t.Fatal("existing connection broke under the cap")
	}
}

// status sends GET /ok on c and returns the response status, or 0 when the connection is gone.
func status(t *testing.T, c net.Conn) int {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	if _, err := c.Write([]byte("GET /ok HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		return 0
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func TestInFlightCap503(t *testing.T) {
	r := start(t, "--limits-max-inflight", "2")
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Post(r.url("/slow"), "text/plain", nil)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	var resp *http.Response
	waitFor(t, func() bool {
		var err error
		if resp != nil {
			resp.Body.Close()
		}
		resp, err = http.Get(r.url("/ok"))
		return err == nil && resp.StatusCode == http.StatusServiceUnavailable
	})
	defer resp.Body.Close()
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("503 without Retry-After")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "overloaded") {
		t.Fatalf("body = %s", body)
	}
	close(r.block)
	r.block = make(chan struct{}) // Cleanup closes the new one
	wg.Wait()
	waitFor(t, func() bool {
		resp2, err := http.Get(r.url("/ok"))
		if err != nil {
			return false
		}
		resp2.Body.Close()
		return resp2.StatusCode == 200
	})
}

func TestPerRouteBodyCaps(t *testing.T) {
	r := start(t)
	post := func(path string, n int) int {
		resp, err := http.Post(r.url(path), "text/plain", strings.NewReader(strings.Repeat("a", n)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if post("/internal/v1/in", 1<<20) != 200 || post("/internal/v1/in", 3<<20) != http.StatusRequestEntityTooLarge {
		t.Fatal("internal cap (2 MiB) not enforced")
	}
	if post("/api/v1/in", 1<<10) != 200 || post("/api/v1/in", 4<<10) != http.StatusRequestEntityTooLarge {
		t.Fatal("api cap (1 KiB) not enforced")
	}
	if post("/v1/stream/other", 3<<20) != 200 {
		t.Fatal("uncapped route must stay under the global cap only")
	}
}

// streamPost opens a chunked POST whose body the test feeds through the returned writer.
func streamPost(t *testing.T, url string) (*io.PipeWriter, <-chan string) {
	t.Helper()
	pr, pw := io.Pipe()
	out := make(chan string, 1)
	go func() {
		resp, err := http.Post(url, "application/octet-stream", pr)
		if err != nil {
			out <- "client-error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		out <- string(b)
	}()
	t.Cleanup(func() { _ = pw.Close() })
	return pw, out
}

func TestStreamIdleDeadline(t *testing.T) {
	r := start(t, "--limits-read-timeout", "300ms", "--limits-write-timeout", "300ms", "--limits-stream-idle-timeout", "500ms")
	pw, out := streamPost(t, r.url("/v1/stream"))
	// A steady stream outlives ReadTimeout (300 ms) because each chunk re-arms only the idle deadline.
	for i := 0; i < 8; i++ {
		if _, err := pw.Write([]byte("chunk")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	_ = pw.Close()
	if got := <-out; got != "n=40 err=false" {
		t.Fatalf("steady stream = %q", got)
	}
	// A stalled stream is cut once the idle deadline passes.
	pw2, out2 := streamPost(t, r.url("/v1/stream"))
	_, _ = pw2.Write([]byte("x"))
	select {
	case got := <-out2:
		if !strings.Contains(got, "err=true") && !strings.HasPrefix(got, "client-error") {
			t.Fatalf("stalled stream = %q, want a read error", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stalled stream was never cut")
	}
}

func TestSocketAppliesToEveryClass(t *testing.T) {
	h := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := h.EnsureListener(core.ClassInternal, "internal-listen", "127.0.0.1:0", "u"); err != nil {
		t.Fatal(err)
	}
	if err := h.Register(limits.New(limits.WithBodyCaps(nil)), routes{block: make(chan struct{})}); err != nil {
		t.Fatal(err)
	}
	if err := h.Config.Load([]string{"--listen", "127.0.0.1:0", "--limits-max-conns", "1"}, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	<-h.Ready()
	defer func() { cancel(); <-done }()
	// Each listener has its own cap of 1: one connection on each works at the same time.
	a, err := net.Dial("tcp", h.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := net.Dial("tcp", h.ClassAddr(core.ClassInternal))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if status(t, a) != 200 {
		t.Fatal("public connection refused")
	}
	if _, err := b.Write([]byte("GET /healthz HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(b), nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("internal connection: %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	bad := [][]string{
		{"--limits-max-conns", "0"}, {"--limits-max-inflight", "0"}, {"--limits-max-conns", "-5"},
		{"--limits-max-header-bytes", "10"}, {"--limits-read-timeout", "0s"}, {"--limits-read-header-timeout", "-1s"},
		{"--limits-write-timeout", "0s"}, {"--limits-idle-timeout", "0s"}, {"--limits-stream-idle-timeout", "0s"},
	}
	for _, args := range bad {
		h := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		if err := h.Register(limits.New()); err != nil {
			t.Fatal(err)
		}
		args = append([]string{"--listen", "127.0.0.1:0"}, args...)
		if err := h.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
			t.Fatal(err)
		}
		err := h.Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "limits:") {
			t.Errorf("%v: Run error = %v, want a limits validation error", args, err)
		}
	}
}

func TestSettingsFromEnvironment(t *testing.T) {
	h := core.NewHost()
	if err := h.Register(limits.New()); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"RADAR_LIMITS_MAX_CONNS": "7", "RADAR_LIMITS_MAX_INFLIGHT": "9"}
	err := h.Config.Load(nil, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if h.Config.Int(limits.SettingMaxConns) != 7 || h.Config.Int(limits.SettingMaxInflight) != 9 {
		t.Fatal("environment did not reach the limits settings")
	}
}

// TestHostWorksWithoutLimits is the removal test: without the module the same routes still serve.
func TestHostWorksWithoutLimits(t *testing.T) {
	h := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := h.Register(routes{block: make(chan struct{})}); err != nil {
		t.Fatal(err)
	}
	if err := h.Config.Load([]string{"--listen", "127.0.0.1:0"}, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	<-h.Ready()
	resp, err := http.Get("http://" + h.Addr() + "/ok")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET /ok without limits: %v", err)
	}
	resp.Body.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestShutdownWorksWithConnectionsOpen(t *testing.T) {
	r := start(t, "--limits-max-conns", "4")
	c := r.dial()
	if status(t, c) != 200 {
		t.Fatal("request failed")
	}
	if err := r.stop(); err != nil {
		t.Fatalf("graceful stop with an idle keep-alive connection: %v", err)
	}
}

// events returns the audit events the host collected so far (the socket replays them to a late observer).
func events(r *rig) []core.AuditEvent {
	var got []core.AuditEvent
	r.host.Audit.Observe(func(ev core.AuditEvent) { got = append(got, ev) })
	return got
}

func TestConnectionCapRefusalIsAuditedAndCounted(t *testing.T) {
	r := start(t, "--limits-max-conns", "1")
	a := r.dial()
	waitFor(t, func() bool { return status(t, a) == 200 })
	if closed, _ := closedWithin(r.dial(), time.Second); !closed {
		t.Fatal("second connection should be refused")
	}
	waitFor(t, func() bool {
		v, _ := r.host.Metrics.Value("radar_connections_refused_total")
		return v >= 1
	})
	if v, _ := r.host.Metrics.Value("radar_connections"); v != 1 {
		t.Fatalf("radar_connections = %v, want 1", v)
	}
	got := events(r)
	if len(got) == 0 || got[0].Event != "conn_refused" || got[0].IP != "127.0.0.1" || got[0].Class != "public" || got[0].Reason != "conn_cap" {
		t.Fatalf("events = %+v", got)
	}
}

func TestInflightRefusalIsAuditedAndGaugeShowsLoad(t *testing.T) {
	r := start(t, "--limits-max-inflight", "1")
	go func() {
		if resp, err := http.Post(r.url("/slow"), "text/plain", nil); err == nil {
			resp.Body.Close()
		}
	}()
	waitFor(t, func() bool { v, _ := r.host.Metrics.Value("radar_inflight_requests"); return v == 1 })
	waitFor(t, func() bool {
		resp, err := http.Get(r.url("/ok"))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusServiceUnavailable
	})
	got := events(r)
	if len(got) == 0 || got[0].Event != "overloaded" || got[0].IP != "127.0.0.1" || got[0].Reason != "inflight_cap" {
		t.Fatalf("events = %+v", got)
	}
}
