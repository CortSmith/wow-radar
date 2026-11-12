package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"radarlive/internal/domain"
)

func TestRoutesMethodAware(t *testing.T) {
	r := NewRoutes()
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	if err := r.HandleFunc(http.MethodGet, "/thing", ok); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	r.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/thing", nil))
	post := httptest.NewRecorder()
	r.Handler().ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/thing", nil))
	if get.Code != http.StatusNoContent || post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET=%d POST=%d", get.Code, post.Code)
	}
}

func TestRoutesRejectsDuplicatesAndBadInput(t *testing.T) {
	r := NewRoutes()
	h := func(http.ResponseWriter, *http.Request) {}
	if err := r.HandleFunc("GET", "/a", h); err != nil {
		t.Fatal(err)
	}
	if err := r.HandleFunc("GET", "/a", h); err == nil {
		t.Error("duplicate must fail")
	}
	if err := r.HandleFunc("POST", "/a", h); err != nil {
		t.Errorf("same path other method is fine: %v", err)
	}
	if err := r.HandleFunc("GET", "/b/{x}", h); err != nil {
		t.Fatal(err)
	}
	if err := r.HandleFunc("GET", "/b/{y}", h); err == nil {
		t.Error("conflicting wildcard pattern must fail, not panic")
	}
	if err := r.HandleFunc("", "/c", h); err == nil {
		t.Error("empty method must fail")
	}
	if err := r.HandleFunc("GET", "c", h); err == nil {
		t.Error("pattern without leading slash must fail")
	}
	if err := r.HandleFunc("GET", "/d", nil); err == nil {
		t.Error("nil handler must fail")
	}
}

type fakeParser struct {
	name string
	got  []any
	err  error
}

func (p *fakeParser) Name() string { return p.name }
func (p *fakeParser) Parse(_ context.Context, ev any) error {
	p.got = append(p.got, ev)
	return p.err
}

func TestParsersFeedReachesEveryParserAndJoinsErrors(t *testing.T) {
	ps := NewParsers()
	a, b := &fakeParser{name: "a", err: errors.New("boom")}, &fakeParser{name: "b"}
	ps.Add(a)
	ps.Add(b)
	err := ps.Feed(context.Background(), "event")
	if err == nil || !errors.Is(err, a.err) {
		t.Fatalf("err = %v", err)
	}
	if len(a.got) != 1 || len(b.got) != 1 {
		t.Fatal("a failing parser must not stop the others")
	}
	if !reflect.DeepEqual(ps.Names(), []string{"a", "b"}) {
		t.Fatalf("names = %v", ps.Names())
	}
}

type fakeSink struct {
	name  string
	pings []domain.Ping
	err   error
}

func (s *fakeSink) Name() string { return s.name }
func (s *fakeSink) Accept(_ context.Context, p domain.Ping) error {
	s.pings = append(s.pings, p)
	return s.err
}

func TestSinksPublish(t *testing.T) {
	sinks := NewSinks()
	a, b := &fakeSink{name: "a", err: errors.New("down")}, &fakeSink{name: "b"}
	sinks.Add(a)
	sinks.Add(b)
	err := sinks.Publish(context.Background(), domain.Ping{BatchID: "x"})
	if !errors.Is(err, a.err) || len(b.pings) != 1 || b.pings[0].BatchID != "x" {
		t.Fatalf("err=%v b=%v", err, b.pings)
	}
	if sinks.Len() != 2 {
		t.Fatal("Len")
	}
}

func TestQueries(t *testing.T) {
	q := NewQueries()
	fn := func(context.Context, map[string][]string) (any, error) { return 7, nil }
	if err := q.Add("sweep", fn); err != nil {
		t.Fatal(err)
	}
	if err := q.Add("sweep", fn); err == nil {
		t.Error("duplicate query must fail")
	}
	if err := q.Add("", fn); err == nil {
		t.Error("empty name must fail")
	}
	got, ok := q.Get("sweep")
	if !ok {
		t.Fatal("Get")
	}
	if v, _ := got(context.Background(), nil); v != 7 {
		t.Fatal("handler not returned")
	}
	if _, ok := q.Get("nope"); ok {
		t.Error("unknown query found")
	}
	if !reflect.DeepEqual(q.Names(), []string{"sweep"}) {
		t.Fatal("Names")
	}
}

func TestStores(t *testing.T) {
	s := NewStores()
	if _, err := s.Default(); err == nil {
		t.Error("Default on empty registry must fail")
	}
	first, second := &stubStore{}, &stubStore{}
	if err := s.Add("mem", first); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("sql", second); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("mem", second); err == nil {
		t.Error("duplicate store name must fail")
	}
	if err := s.Add("x", nil); err == nil {
		t.Error("nil store must fail")
	}
	if got, err := s.Default(); err != nil || got != Store(first) {
		t.Errorf("Default = %v, %v; want first registered", got, err)
	}
	if got, ok := s.Get("sql"); !ok || got != Store(second) {
		t.Error("Get by name")
	}
	if !reflect.DeepEqual(s.Names(), []string{"mem", "sql"}) {
		t.Errorf("Names = %v", s.Names())
	}
	if err := s.CloseAll(); err != nil || !first.closed || !second.closed {
		t.Errorf("CloseAll err=%v closed=%v/%v", err, first.closed, second.closed)
	}
}

func TestStoresCloseAllJoinsErrors(t *testing.T) {
	s := NewStores()
	bad := &stubStore{closeErr: errors.New("disk")}
	_ = s.Add("bad", bad)
	if err := s.CloseAll(); !errors.Is(err, bad.closeErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestHooks(t *testing.T) {
	h := NewHooks()
	var calls []string
	h.OnStart(func(context.Context) error { calls = append(calls, "start1"); return nil })
	h.OnStart(func(context.Context) error { calls = append(calls, "start2"); return errors.New("fail") })
	h.OnStart(func(context.Context) error { calls = append(calls, "start3"); return nil })
	h.OnStop(func(context.Context) error { calls = append(calls, "stop1"); return errors.New("e1") })
	h.OnStop(func(context.Context) error { calls = append(calls, "stop2"); return nil })
	if err := h.RunStart(context.Background()); err == nil {
		t.Error("failing start hook must stop the run")
	}
	if err := h.RunStop(context.Background()); err == nil {
		t.Error("stop errors must be returned")
	}
	want := []string{"start1", "start2", "stop2", "stop1"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v (stop hooks run in reverse, all of them)", calls, want)
	}
}
