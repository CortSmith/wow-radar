package writeapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
	"radarlive/internal/mod/writeapi"
)

const pingPath = "/internal/v1/ping"

// fakeNow is the server clock of every rig; fixtures use timestamps around it.
var fakeNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// fakeStore applies pings by remembering batch ids.
type fakeStore struct {
	mu   sync.Mutex
	seen map[string]bool
	got  []domain.Ping
	err  error
}

func (f *fakeStore) ApplyPing(_ context.Context, p domain.Ping) (domain.ApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return domain.ApplyResult{}, f.err
	}
	if f.seen[p.BatchID] {
		return domain.ApplyResult{Received: len(p.Contacts), Duplicate: true}, nil
	}
	f.seen[p.BatchID] = true
	f.got = append(f.got, p)
	return domain.ApplyResult{Received: len(p.Contacts), Changed: len(p.Contacts)}, nil
}

func (f *fakeStore) Sweep(context.Context, domain.Sector) (domain.SweepResult, error) {
	return domain.SweepResult{}, nil
}
func (f *fakeStore) Search(context.Context, domain.SearchQuery) (domain.SearchResult, error) {
	return domain.SearchResult{}, nil
}
func (f *fakeStore) Trail(context.Context, domain.Ruleset, string, *int) ([]domain.Fix, error) {
	return nil, nil
}
func (f *fakeStore) Bounty(context.Context, domain.Ruleset, int) ([]domain.BountyRow, error) {
	return nil, nil
}
func (f *fakeStore) BountyPlayer(context.Context, domain.Ruleset, string) (domain.BountyRow, error) {
	return domain.BountyRow{}, nil
}
func (f *fakeStore) Charts(context.Context, domain.Ruleset) ([]domain.Chart, error) { return nil, nil }
func (f *fakeStore) Status(context.Context) ([]domain.RulesetStatus, error)         { return nil, nil }
func (f *fakeStore) Close() error                                                   { return nil }

// recordingSink remembers published pings.
type recordingSink struct {
	got []domain.Ping
	err error
}

func (s *recordingSink) Name() string { return "recorder" }
func (s *recordingSink) Accept(_ context.Context, p domain.Ping) error {
	s.got = append(s.got, p)
	return s.err
}

type rig struct {
	host  *core.Host
	store *fakeStore
	sink  *recordingSink
}

func newRig(t *testing.T, args ...string) *rig {
	t.Helper()
	r := &rig{host: core.NewHost(), store: &fakeStore{seen: map[string]bool{}}, sink: &recordingSink{}}
	if err := r.host.Stores.Add("fake", r.store); err != nil {
		t.Fatal(err)
	}
	r.host.Sinks.Add(r.sink)
	if err := r.host.Register(writeapi.NewModule(writeapi.WithClock(func() time.Time { return fakeNow }))); err != nil {
		t.Fatal(err)
	}
	if err := r.host.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) post(body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, pingPath, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.host.Routes.HandlerFor(core.ClassInternal).ServeHTTP(rec, req)
	return rec
}

func validPing(batch string) string {
	return fmt.Sprintf(`{"rs":"pvp","batchId":%q,"source":"s1","contacts":[{"guid":"Player-1-A","name":"Ann",
		"reaction":"friendly","lastSeen":%d,"firstSeen":%d,"d":{"events":2},"fixes":[]}],"charts":[],"deaths":[]}`,
		batch, fakeNow.UnixMilli(), fakeNow.UnixMilli()-100)
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct{ Error, Message string }
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Message == "" {
		t.Fatalf("bad error body %q (%v)", rec.Body.String(), err)
	}
	return e.Error
}

func TestPingApplied(t *testing.T) {
	r := newRig(t)
	rec := r.post(validPing("b1"))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res domain.ApplyResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Received != 1 || res.Changed != 1 || res.Duplicate {
		t.Fatalf("result %+v err %v", res, err)
	}
	if len(r.store.got) != 1 || r.store.got[0].Contacts[0].D.Events != 2 {
		t.Fatalf("store got %+v", r.store.got)
	}
	if len(r.sink.got) != 1 || r.sink.got[0].BatchID != "b1" {
		t.Fatalf("sink got %+v", r.sink.got)
	}
}

func TestDuplicateIsNotPublished(t *testing.T) {
	r := newRig(t)
	r.post(validPing("b1"))
	rec := r.post(validPing("b1"))
	var res domain.ApplyResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || !res.Duplicate || res.Changed != 0 {
		t.Fatalf("duplicate result: %d %+v", rec.Code, res)
	}
	if len(r.sink.got) != 1 {
		t.Fatalf("duplicate was published: %d sink calls", len(r.sink.got))
	}
}

func TestSinkFailureDoesNotFailPing(t *testing.T) {
	r := newRig(t)
	r.sink.err = errors.New("sink down")
	if rec := r.post(validPing("b1")); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestBadRequests(t *testing.T) {
	r := newRig(t)
	tests := []struct {
		name, body string
	}{
		{"empty body", ``},
		{"not json", `{nope`},
		{"unknown field", `{"rs":"pvp","batchId":"b","extra":1,"contacts":[]}`},
		{"unknown contact field", `{"rs":"pvp","batchId":"b","contacts":[{"guid":"Player-1","bogus":1}]}`},
		{"trailing data", validPing("b2") + ` {}`},
		{"bad ruleset", `{"rs":"zzz","batchId":"b"}`},
		{"missing batch", `{"rs":"pvp"}`},
		{"bad batch id", `{"rs":"pvp","batchId":"has space"}`},
		{"bad guid", `{"rs":"pvp","batchId":"b","contacts":[{"guid":"Creature-1"}]}`},
		{"negative delta", `{"rs":"pvp","batchId":"b","contacts":[{"guid":"Player-1","d":{"kb":-1}}]}`},
		{"array body", `[]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := r.post(tc.body)
			if rec.Code != 400 || errCode(t, rec) != "invalid_request" {
				t.Fatalf("status %d body %s", rec.Code, rec.Body)
			}
		})
	}
	if len(r.store.got) != 0 || len(r.sink.got) != 0 {
		t.Fatal("rejected pings reached the store or sinks")
	}
}

func TestBodyCap(t *testing.T) {
	r := newRig(t, "--max-body", "200")
	rec := r.post(validPing("big") + strings.Repeat(" ", 400))
	if rec.Code != http.StatusRequestEntityTooLarge || errCode(t, rec) != "too_large" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestStoreErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
		want string
	}{
		{"invalid", fmt.Errorf("x: %w", domain.ErrInvalid), 400, "invalid_request"},
		{"internal", errors.New("db locked at /secret"), 500, "internal"},
		{"timeout", context.DeadlineExceeded, 503, "timeout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.store.err = tc.err
			rec := r.post(validPing("b1"))
			if rec.Code != tc.code || errCode(t, rec) != tc.want || strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("status %d body %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestNoStore(t *testing.T) {
	host := core.NewHost()
	if err := host.Register(writeapi.NewModule(writeapi.WithClock(func() time.Time { return fakeNow }))); err != nil {
		t.Fatal(err)
	}
	_ = host.Config.Load(nil, func(string) (string, bool) { return "", false })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, pingPath, strings.NewReader(validPing("b")))
	host.Routes.HandlerFor(core.ClassInternal).ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestAuthAndMethod(t *testing.T) {
	r := newRig(t)
	r.host.Auth.Add(core.RoleIngest, core.AuthenticatorFunc(func(req *http.Request) bool {
		return req.Header.Get("X-Ingest-Key") == "k"
	}))
	if rec := r.post(validPing("b1")); rec.Code != 401 {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := r.post(validPing("b1"), "X-Ingest-Key", "k"); rec.Code != 200 {
		t.Fatalf("with key: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, pingPath, nil)
	rec := httptest.NewRecorder()
	r.host.Routes.HandlerFor(core.ClassInternal).ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Fatalf("GET status %d", rec.Code)
	}
}

func TestModuleName(t *testing.T) {
	if writeapi.NewModule(writeapi.WithClock(func() time.Time { return fakeNow })).Name() != "writeapi" {
		t.Fatal("unexpected name")
	}
}

func TestPingRouteIsNotOnThePublicClass(t *testing.T) {
	r := newRig(t)
	req := httptest.NewRequest(http.MethodPost, pingPath, strings.NewReader(validPing("pub-1")))
	req.Header.Set("X-Ingest-Key", "whatever-key-1234567")
	rec := httptest.NewRecorder()
	r.host.Routes.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("public class served the write API: %d", rec.Code)
	}
}

func TestInternalListenSettingDefaultsToLoopback(t *testing.T) {
	r := newRig(t)
	if got := r.host.Config.String(writeapi.SettingInternalListen); got != "127.0.0.1:8789" {
		t.Fatalf("internal-listen default = %q", got)
	}
}

func TestDiskFullReturns507(t *testing.T) {
	r := newRig(t)
	r.store.err = fmt.Errorf("sqlstore: apply: %w", domain.ErrStorageFull)
	rec := r.post(validPing("full1"))
	if rec.Code != http.StatusInsufficientStorage || errCode(t, rec) != "insufficient_storage" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(r.sink.got) != 0 {
		t.Fatal("a refused write must not be published")
	}
	// Space returns: the same ping is accepted again.
	r.store.err = nil
	if rec := r.post(validPing("full1")); rec.Code != 200 {
		t.Fatalf("after recovery status %d: %s", rec.Code, rec.Body)
	}
}

func TestDegradedStoreReturns503(t *testing.T) {
	r := newRig(t)
	r.store.err = fmt.Errorf("wrapped: %w", domain.ErrDegraded)
	rec := r.post(validPing("deg1"))
	if rec.Code != http.StatusServiceUnavailable || errCode(t, rec) != "store_degraded" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "wrapped") {
		t.Fatal("internal error text leaked")
	}
}
