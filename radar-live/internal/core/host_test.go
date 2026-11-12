package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/domain"
)

// stubStore is a do-nothing Store that records Close.
type stubStore struct {
	closed   bool
	closeErr error
}

func (s *stubStore) ApplyPing(context.Context, domain.Ping) (domain.ApplyResult, error) {
	return domain.ApplyResult{}, nil
}
func (s *stubStore) Sweep(context.Context, domain.Sector) (domain.SweepResult, error) {
	return domain.SweepResult{}, nil
}
func (s *stubStore) Search(context.Context, domain.SearchQuery) (domain.SearchResult, error) {
	return domain.SearchResult{}, nil
}
func (s *stubStore) Trail(context.Context, domain.Ruleset, string, *int) ([]domain.Fix, error) {
	return nil, nil
}
func (s *stubStore) Bounty(context.Context, domain.Ruleset, int) ([]domain.BountyRow, error) {
	return nil, nil
}
func (s *stubStore) BountyPlayer(context.Context, domain.Ruleset, string) (domain.BountyRow, error) {
	return domain.BountyRow{}, nil
}
func (s *stubStore) Charts(context.Context, domain.Ruleset) ([]domain.Chart, error) { return nil, nil }
func (s *stubStore) Status(context.Context) ([]domain.RulesetStatus, error)         { return nil, nil }
func (s *stubStore) Close() error                                                   { s.closed = true; return s.closeErr }

// probe is a module that records its lifecycle into a shared log.
type probe struct {
	name              string
	log               *[]string
	initErr, startErr error
	stopErr           error
}

func (p *probe) Name() string { return p.name }
func (p *probe) Init(*Host) error {
	*p.log = append(*p.log, "init:"+p.name)
	return p.initErr
}
func (p *probe) Start(context.Context) error {
	*p.log = append(*p.log, "start:"+p.name)
	return p.startErr
}
func (p *probe) Stop(context.Context) error {
	*p.log = append(*p.log, "stop:"+p.name)
	return p.stopErr
}

// plain only implements Module (no Start/Stop).
type plain struct{ name string }

func (p plain) Name() string     { return p.name }
func (p plain) Init(*Host) error { return nil }

func newTestHost(t *testing.T) *Host {
	t.Helper()
	log, _ := bufferLogger()
	h := NewHost(WithLogger(log))
	if err := h.Config.Load([]string{"--listen", "127.0.0.1:0"}, envOf(nil)); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRegisterInitsInOrder(t *testing.T) {
	var calls []string
	h := newTestHost(t)
	err := h.Register(&probe{name: "a", log: &calls}, plain{name: "b"}, &probe{name: "c", log: &calls})
	if err != nil || !reflect.DeepEqual(calls, []string{"init:a", "init:c"}) {
		t.Fatalf("err=%v calls=%v", err, calls)
	}
	if !reflect.DeepEqual(h.ModuleNames(), []string{"a", "b", "c"}) {
		t.Fatalf("names %v", h.ModuleNames())
	}
}

func TestRegisterRejectsDuplicatesAndNamesFailingModule(t *testing.T) {
	var calls []string
	h := newTestHost(t)
	if err := h.Register(plain{name: "a"}, plain{name: "a"}); err == nil || !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf("duplicate: %v", err)
	}
	boom := errors.New("boom")
	h = newTestHost(t)
	err := h.Register(&probe{name: "ok", log: &calls}, &probe{name: "bad", log: &calls, initErr: boom})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), `"bad"`) {
		t.Fatalf("init failure: %v", err)
	}
	if err := h.Register(plain{name: ""}); err == nil {
		t.Fatal("empty module name must fail")
	}
}

// runHost starts the host and waits until it listens; the returned func stops it and returns Run's error.
func runHost(t *testing.T, h *Host) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	select {
	case <-h.Ready():
	case err := <-done:
		cancel()
		t.Fatalf("host exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("host never became ready")
	}
	var once sync.Once
	var result error
	return func() error {
		once.Do(func() { cancel(); result = <-done })
		return result
	}
}

func TestRunServesHealthzAndStopsInReverse(t *testing.T) {
	var calls []string
	h := newTestHost(t)
	if err := h.Register(&probe{name: "a", log: &calls}, &probe{name: "b", log: &calls}); err != nil {
		t.Fatal(err)
	}
	store := &stubStore{}
	_ = h.Stores.Add("mem", store)
	h.Hooks.OnStart(func(context.Context) error { calls = append(calls, "hook:start"); return nil })
	h.Hooks.OnStop(func(context.Context) error { calls = append(calls, "hook:stop"); return nil })

	stop := runHost(t, h)
	resp, err := http.Get("http://" + h.Addr() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("healthz: %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Request-Id") == "" {
		t.Error("middleware not applied to served requests")
	}
	if err := stop(); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	want := []string{"init:a", "init:b", "hook:start", "start:a", "start:b", "stop:b", "stop:a", "hook:stop"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("lifecycle = %v\nwant       %v", calls, want)
	}
	if !store.closed {
		t.Error("stores must be closed on shutdown")
	}
}

func TestRunStartFailureStopsStartedModules(t *testing.T) {
	var calls []string
	boom := errors.New("cannot start")
	h := newTestHost(t)
	_ = h.Register(&probe{name: "a", log: &calls}, &probe{name: "b", log: &calls, startErr: boom}, &probe{name: "c", log: &calls})
	err := h.Run(context.Background())
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("err = %v", err)
	}
	want := []string{"init:a", "init:b", "init:c", "start:a", "start:b", "stop:a"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestRunReportsStopErrors(t *testing.T) {
	var calls []string
	boom := errors.New("stuck")
	h := newTestHost(t)
	_ = h.Register(&probe{name: "a", log: &calls, stopErr: boom})
	stop := runHost(t, h)
	if err := stop(); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunFailsWhenPortBusy(t *testing.T) {
	first := newTestHost(t)
	stop := runHost(t, first)
	defer stop()
	log, _ := bufferLogger()
	second := NewHost(WithLogger(log))
	if err := second.Config.Load([]string{"--listen", first.Addr()}, envOf(nil)); err != nil {
		t.Fatal(err)
	}
	if err := second.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartRunsOnStartHookFailure(t *testing.T) {
	h := newTestHost(t)
	boom := errors.New("hook failed")
	h.Hooks.OnStart(func(context.Context) error { return boom })
	if err := h.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
