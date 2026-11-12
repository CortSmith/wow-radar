package readapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
	"radarlive/internal/mod/readapi"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// fakeStore records the arguments of each read and returns canned data.
type fakeStore struct {
	sector  domain.Sector
	search  domain.SearchQuery
	trailRS domain.Ruleset
	trailID string
	trailM  *int
	bountyN int
	err     error
	block   bool
	sweep   domain.SweepResult
}

func (f *fakeStore) ApplyPing(context.Context, domain.Ping) (domain.ApplyResult, error) {
	return domain.ApplyResult{}, nil
}

func (f *fakeStore) wait(ctx context.Context) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.err
}

func (f *fakeStore) Sweep(ctx context.Context, s domain.Sector) (domain.SweepResult, error) {
	f.sector = s
	if err := f.wait(ctx); err != nil {
		return domain.SweepResult{}, err
	}
	res := f.sweep
	res.Sector = s
	if res.Contacts == nil {
		res.Contacts = []domain.Contact{{GUID: "Player-1", Name: "Ann"}}
	}
	return res, nil
}

func (f *fakeStore) Search(ctx context.Context, q domain.SearchQuery) (domain.SearchResult, error) {
	f.search = q
	if err := f.wait(ctx); err != nil {
		return domain.SearchResult{}, err
	}
	return domain.SearchResult{Total: 1, Contacts: []domain.Contact{{GUID: "Player-1"}}}, nil
}

func (f *fakeStore) Trail(ctx context.Context, rs domain.Ruleset, guid string, m *int) ([]domain.Fix, error) {
	f.trailRS, f.trailID, f.trailM = rs, guid, m
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	return []domain.Fix{{T: 1, X: 2, Y: 3, Kind: domain.FixExact}}, nil
}

func (f *fakeStore) Bounty(ctx context.Context, _ domain.Ruleset, limit int) ([]domain.BountyRow, error) {
	f.bountyN = limit
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	return []domain.BountyRow{{Rank: 1, GUID: "Player-1", KB: 2, Score: 20}}, nil
}

func (f *fakeStore) BountyPlayer(ctx context.Context, _ domain.Ruleset, guid string) (domain.BountyRow, error) {
	if err := f.wait(ctx); err != nil {
		return domain.BountyRow{}, err
	}
	return domain.BountyRow{Rank: 3, GUID: guid}, nil
}

func (f *fakeStore) Charts(ctx context.Context, _ domain.Ruleset) ([]domain.Chart, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	return []domain.Chart{{MapID: 1, Name: "Zone", X1: 1, Y1: 1}}, nil
}

func (f *fakeStore) Status(ctx context.Context) ([]domain.RulesetStatus, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	return []domain.RulesetStatus{{ID: domain.RulesetPvP, Contacts: 4, LastWrite: 9}}, nil
}

func (f *fakeStore) Close() error { return nil }

type rig struct {
	host  *core.Host
	store *fakeStore
	now   time.Time
	h     http.Handler
}

func newRig(t *testing.T, args ...string) *rig {
	t.Helper()
	r := &rig{store: &fakeStore{}, now: t0}
	r.host = core.NewHost()
	if err := r.host.Stores.Add("fake", r.store); err != nil {
		t.Fatal(err)
	}
	mod := readapi.NewModule(readapi.WithClock(func() time.Time { return r.now }))
	if err := r.host.Register(mod); err != nil {
		t.Fatal(err)
	}
	if err := r.host.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	r.h = r.host.Routes.Handler()
	return r
}

func (r *rig) get(method, target string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct{ Error, Message string }
	decode(t, rec, &e)
	if e.Message == "" {
		t.Fatalf("error body has no message: %s", rec.Body.String())
	}
	return e.Error
}

func TestSweepHappyPath(t *testing.T) {
	r := newRig(t)
	rec := r.get("GET", "/api/v1/sweep?rs=rp&scope=zone&mapId=0&grp=Hostile&q=ann&since=500&trail=1&activeWithin=60000")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := r.store.sector
	if got.MapID == nil || *got.MapID != 0 {
		t.Fatalf("mapId 0 must be passed through: %+v", got)
	}
	got.MapID = nil
	want := domain.Sector{RS: domain.RulesetRP, Scope: domain.ScopeZone, Group: domain.GroupHostile,
		Query: "ann", Since: 500, ActiveWithinMs: 60000, Trail: true}
	if got != want {
		t.Fatalf("sector = %+v, want %+v", got, want)
	}
	var res struct {
		Now      int64
		Contacts []domain.Contact
	}
	decode(t, rec, &res)
	if res.Now != t0.UnixMilli() || len(res.Contacts) != 1 {
		t.Fatalf("response = %+v", res)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestSweepDefaults(t *testing.T) {
	r := newRig(t)
	if rec := r.get("GET", "/api/v1/sweep"); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := r.store.sector
	if got.RS != domain.RulesetPvP || got.Scope != domain.ScopeAll || got.Since != 0 || got.Trail {
		t.Fatalf("defaults = %+v", got)
	}
	r2 := newRig(t, "--default-ruleset", "rppvp")
	r2.get("GET", "/api/v1/sweep")
	if r2.store.sector.RS != domain.RulesetRPPvP {
		t.Fatalf("default ruleset flag ignored: %q", r2.store.sector.RS)
	}
}

func TestSweepNowFallsBackToClock(t *testing.T) {
	r := newRig(t)
	r.store.sweep.Now = 42
	var res domain.SweepResult
	decode(t, r.get("GET", "/api/v1/sweep"), &res)
	if res.Now != 42 {
		t.Fatalf("store now should be kept, got %d", res.Now)
	}
	r.store.sweep.Now = 0
	decode(t, r.get("GET", "/api/v1/sweep"), &res)
	if res.Now != t0.UnixMilli() {
		t.Fatalf("now = %d, want clock", res.Now)
	}
}

func TestParameterValidation(t *testing.T) {
	r := newRig(t)
	bad := []string{
		"/api/v1/sweep?rs=bogus",
		"/api/v1/sweep?scope=galaxy",
		"/api/v1/sweep?scope=zone",
		"/api/v1/sweep?scope=zone&mapId=abc",
		"/api/v1/sweep?scope=player",
		"/api/v1/sweep?grp=purple",
		"/api/v1/sweep?since=-1",
		"/api/v1/sweep?since=soon",
		"/api/v1/sweep?activeWithin=-5",
		"/api/v1/sweep?trail=yes",
		"/api/v1/sweep?q=" + strings.Repeat("a", 200),
		"/api/v1/contacts?rs=bogus",
		"/api/v1/contacts?limit=x",
		"/api/v1/contacts?limit=-1",
		"/api/v1/contacts?offset=-1",
		"/api/v1/contacts?grp=nope",
		"/api/v1/contacts?mapId=1.5",
		"/api/v1/trail",
		"/api/v1/trail?guid=Player-1&mapId=x",
		"/api/v1/trail?guid=Player-1&rs=bogus",
		"/api/v1/bounty?rs=bogus",
		"/api/v1/bounty?limit=zz",
		"/api/v1/bounty/player",
		"/api/v1/charts?rs=bogus",
	}
	for _, target := range bad {
		t.Run(target[:min(len(target), 50)], func(t *testing.T) {
			rec := r.get("GET", target)
			if rec.Code != 400 {
				t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
			}
			if code := errCode(t, rec); code != "invalid_request" {
				t.Fatalf("error code = %q", code)
			}
		})
	}
}

func TestContactsClampAndPassThrough(t *testing.T) {
	r := newRig(t)
	tests := []struct {
		query     string
		wantLimit int
		wantOff   int
	}{
		{"", domain.SearchDefaultLimit, 0},
		{"limit=7&offset=3", 7, 3},
		{"limit=9999", domain.SearchMaxLimit, 0},
	}
	for _, tc := range tests {
		rec := r.get("GET", "/api/v1/contacts?rs=normal&q=Ann&mapId=3&grp=friendly&"+tc.query)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", tc.query, rec.Code, rec.Body)
		}
		got := r.store.search
		if got.Limit != tc.wantLimit || got.Offset != tc.wantOff || got.RS != domain.RulesetNormal ||
			got.Q != "Ann" || got.Group != domain.GroupFriendly || got.MapID == nil || *got.MapID != 3 {
			t.Fatalf("%s: search = %+v", tc.query, got)
		}
	}
	var res domain.SearchResult
	decode(t, r.get("GET", "/api/v1/contacts"), &res)
	if res.Total != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestTrail(t *testing.T) {
	r := newRig(t)
	rec := r.get("GET", "/api/v1/trail?guid=Player-1&mapId=5&rs=rp")
	var fixes []domain.Fix
	decode(t, rec, &fixes)
	if len(fixes) != 1 || r.store.trailID != "Player-1" || *r.store.trailM != 5 || r.store.trailRS != domain.RulesetRP {
		t.Fatalf("fixes %+v store %+v", fixes, r.store)
	}
	r.get("GET", "/api/v1/trail?guid=Player-1")
	if r.store.trailM != nil {
		t.Fatal("mapId should be nil when absent")
	}
}

func TestBounty(t *testing.T) {
	r := newRig(t)
	var rows []domain.BountyRow
	decode(t, r.get("GET", "/api/v1/bounty"), &rows)
	if len(rows) != 1 || r.store.bountyN != 50 {
		t.Fatalf("rows %+v limit %d", rows, r.store.bountyN)
	}
	r.get("GET", "/api/v1/bounty?limit=100000")
	if r.store.bountyN != 500 {
		t.Fatalf("limit not clamped: %d", r.store.bountyN)
	}
	var row domain.BountyRow
	decode(t, r.get("GET", "/api/v1/bounty/player?guid=Player-9"), &row)
	if row.Rank != 3 || row.GUID != "Player-9" {
		t.Fatalf("row = %+v", row)
	}
}

func TestChartsAndStatus(t *testing.T) {
	r := newRig(t)
	var charts []domain.Chart
	decode(t, r.get("GET", "/api/v1/charts"), &charts)
	if len(charts) != 1 {
		t.Fatalf("charts = %+v", charts)
	}
	readapi.Version = "9.9"
	r.now = t0.Add(90 * time.Second)
	var st struct {
		Version   string
		Rulesets  []domain.RulesetStatus
		UptimeSec int64
	}
	decode(t, r.get("GET", "/api/v1/status"), &st)
	if st.Version != "9.9" || len(st.Rulesets) != 1 || st.UptimeSec != 90 {
		t.Fatalf("status = %+v", st)
	}
}

func TestStoreErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantErr  string
	}{
		{"invalid", fmt.Errorf("x: %w", domain.ErrInvalid), 400, "invalid_request"},
		{"not found", fmt.Errorf("x: %w", domain.ErrNotFound), 404, "not_found"},
		{"deadline", context.DeadlineExceeded, 503, "timeout"},
		{"other", errors.New("disk exploded at /secret/path"), 500, "internal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.store.err = tc.err
			for _, p := range []string{"/api/v1/contacts", "/api/v1/status"} {
				rec := r.get("GET", p)
				if rec.Code != tc.wantCode || errCode(t, rec) != tc.wantErr {
					t.Fatalf("%s: %d %s", p, rec.Code, rec.Body)
				}
				if tc.name == "other" && strings.Contains(rec.Body.String(), "secret") {
					t.Fatal("internal error detail leaked")
				}
			}
		})
	}
}

func TestQueryTimeout(t *testing.T) {
	r := newRig(t, "--query-timeout", "20ms")
	r.store.block = true
	start := time.Now()
	rec := r.get("GET", "/api/v1/contacts")
	if rec.Code != 503 || time.Since(start) > 2*time.Second {
		t.Fatalf("status %d after %v", rec.Code, time.Since(start))
	}
}

func TestNoStoreIsServerError(t *testing.T) {
	host := core.NewHost()
	if err := host.Register(readapi.NewModule()); err != nil {
		t.Fatal(err)
	}
	_ = host.Config.Load(nil, func(string) (string, bool) { return "", false })
	rec := httptest.NewRecorder()
	host.Routes.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/status", nil))
	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAuthRequired(t *testing.T) {
	r := newRig(t)
	r.host.Auth.Add(core.RoleView, core.AuthenticatorFunc(func(req *http.Request) bool {
		return req.Header.Get("X-View-Key") == "k"
	}))
	paths := []string{"sweep", "contacts", "trail?guid=Player-1", "bounty", "bounty/player?guid=Player-1", "charts", "status"}
	for _, p := range paths {
		if rec := r.get("GET", "/api/v1/"+p); rec.Code != 401 {
			t.Fatalf("%s without key: %d", p, rec.Code)
		}
		if rec := r.get("GET", "/api/v1/"+p, "X-View-Key", "k"); rec.Code != 200 {
			t.Fatalf("%s with key: %d %s", p, rec.Code, rec.Body)
		}
	}
	if rec := r.get("GET", "/healthz"); rec.Code != 200 {
		t.Fatalf("healthz = %d", rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	r := newRig(t)
	if rec := r.get("POST", "/api/v1/status"); rec.Code != 405 {
		t.Fatalf("POST status = %d", rec.Code)
	}
}

func TestModuleName(t *testing.T) {
	if readapi.NewModule().Name() != "readapi" {
		t.Fatal("unexpected name")
	}
}
