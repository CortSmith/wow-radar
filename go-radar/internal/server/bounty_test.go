package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nearbyradar/internal/bounty"
)

type fakeJobs struct {
	mu      sync.Mutex
	started [][]string
	err     error
	status  bounty.Status
}

func (f *fakeJobs) Start(names []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.started = append(f.started, names)
	return "job-1", nil
}
func (f *fakeJobs) Status() bounty.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func bountyServer(t *testing.T) (*httptest.Server, *bounty.Store, *fakeJobs) {
	st := bounty.New("")
	k := bounty.Participant{GUID: "Player-1-E1", Raw: "Foe-Realm", Display: "Foe"}
	a := bounty.Participant{GUID: "Player-1-E2", Raw: "Ally-Realm", Display: "Ally"}
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	st.RecordDeath(bounty.Death{ID: "1", Time: t0, VictimGUID: "Player-1-V1", VictimName: "V1", Killer: &k, Assists: []bounty.Participant{a}, Zone: "Z", MapID: 1})
	st.RecordDeath(bounty.Death{ID: "2", Time: t0.Add(time.Minute), VictimGUID: "Player-1-V2", VictimName: "V2", Killer: &k})
	jobs := &fakeJobs{status: bounty.Status{JobID: "job-1", Running: true, FilesTotal: 3, FilesDone: 1, Errors: []string{}}}
	s := New(Options{State: &fakeState{}, History: seededHist(), Bounty: st, Analyzer: jobs, Interval: 10 * time.Millisecond})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, st, jobs
}

func do(t *testing.T, method, url, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r, string(b)
}

func TestBountyLeaderboard(t *testing.T) {
	ts, _, _ := bountyServer(t)
	r, b := getS(t, ts.URL+"/api/bounty")
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	var out struct {
		Rows  []bounty.Row `json:"rows"`
		Stats bounty.Stats `json:"stats"`
	}
	if err := json.Unmarshal([]byte(b), &out); err != nil {
		t.Fatal(err, string(b))
	}
	if len(out.Rows) != 2 || out.Rows[0].Name != "Foe" || out.Rows[0].Bounty != 20 || out.Rows[0].Rank != 1 || out.Rows[1].Assists != 1 || out.Stats.Deaths != 2 {
		t.Fatalf("%s", b)
	}
	_, b = getS(t, ts.URL+"/api/bounty?limit=1")
	_ = json.Unmarshal([]byte(b), &out)
	if len(out.Rows) != 1 {
		t.Fatalf("limit: %s", b)
	}
}

func TestBountyLimitParamValidation(t *testing.T) {
	ts, _, _ := bountyServer(t)
	for _, q := range []string{"limit=abc", "limit=-1", "limit=0", "limit=1.5", "limit=99999999999999999999"} {
		if r, _ := getS(t, ts.URL+"/api/bounty?"+q); r.StatusCode != 400 {
			t.Errorf("%s -> %d", q, r.StatusCode)
		}
	}
	// over the maximum is clamped, not an error
	if r, b := getS(t, ts.URL+"/api/bounty?limit=100000"); r.StatusCode != 200 || !strings.Contains(b, `"rows"`) {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	if r, _ := do(t, "POST", ts.URL+"/api/bounty", "", nil); r.StatusCode != 405 {
		t.Fatalf("POST leaderboard %d", r.StatusCode)
	}
}

func TestBountyEmptyLeaderboardIsEmptyArray(t *testing.T) {
	s := New(Options{State: &fakeState{}, History: seededHist(), Bounty: bounty.New(""), Analyzer: &fakeJobs{}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	_, b := getS(t, ts.URL+"/api/bounty")
	if !strings.Contains(b, `"rows":[]`) {
		t.Fatal(b)
	}
}

func TestBountyPlayerDetail(t *testing.T) {
	ts, _, _ := bountyServer(t)
	r, b := getS(t, ts.URL+"/api/bounty/player?guid=Player-1-E1")
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	var d bounty.Detail
	if err := json.Unmarshal([]byte(b), &d); err != nil {
		t.Fatal(err)
	}
	if d.GUID != "Player-1-E1" || d.KillingBlows != 2 || len(d.Recent) != 2 || d.Recent[0].Victim != "V2" || d.Recent[0].Role != "killer" {
		t.Fatalf("%s", b)
	}
	if r, _ := getS(t, ts.URL+"/api/bounty/player"); r.StatusCode != 400 {
		t.Fatalf("missing guid %d", r.StatusCode)
	}
	if r, _ := getS(t, ts.URL+"/api/bounty/player?guid="); r.StatusCode != 400 {
		t.Fatalf("empty guid %d", r.StatusCode)
	}
	if r, _ := getS(t, ts.URL+"/api/bounty/player?guid=nope"); r.StatusCode != 404 {
		t.Fatalf("unknown guid %d", r.StatusCode)
	}
	if r, _ := getS(t, ts.URL+"/api/bounty/player?guid="+strings.Repeat("x", 500)); r.StatusCode != 400 {
		t.Fatalf("overlong guid %d", r.StatusCode)
	}
	if r, _ := do(t, "DELETE", ts.URL+"/api/bounty/player?guid=Player-1-E1", "", nil); r.StatusCode != 405 {
		t.Fatalf("method %d", r.StatusCode)
	}
}

func TestAnalyzeStatus(t *testing.T) {
	ts, _, _ := bountyServer(t)
	r, b := getS(t, ts.URL+"/api/bounty/analyze")
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	var s bounty.Status
	_ = json.Unmarshal([]byte(b), &s)
	if !s.Running || s.FilesTotal != 3 || s.FilesDone != 1 || s.JobID != "job-1" {
		t.Fatalf("%s", b)
	}
}

func TestAnalyzeStartRequiresCSRFSafeRequest(t *testing.T) {
	ts, _, jobs := bountyServer(t)
	u := ts.URL + "/api/bounty/analyze"
	// a cross-site <form> can only send these: must be refused
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		h := map[string]string{}
		if ct != "" {
			h["Content-Type"] = ct
		}
		if r, _ := do(t, "POST", u, "", h); r.StatusCode != 403 {
			t.Errorf("content-type %q -> %d", ct, r.StatusCode)
		}
	}
	if len(jobs.started) != 0 {
		t.Fatal("job started without CSRF protection")
	}
	// a foreign Origin is refused even with the right header
	if r, _ := do(t, "POST", u, "", map[string]string{"X-Radar": "1", "Origin": "http://evil.example"}); r.StatusCode != 403 {
		t.Fatalf("foreign origin %d", r.StatusCode)
	}
	// custom header or JSON content type is accepted; loopback origin is fine
	r, b := do(t, "POST", u, "", map[string]string{"X-Radar": "1", "Origin": "http://127.0.0.1:1234"})
	if r.StatusCode != 202 || !strings.Contains(b, `"jobId":"job-1"`) {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	r, _ = do(t, "POST", u, "{}", map[string]string{"Content-Type": "application/json; charset=utf-8"})
	if r.StatusCode != 202 {
		t.Fatalf("json: %d", r.StatusCode)
	}
	if len(jobs.started) != 2 || jobs.started[0] != nil {
		t.Fatalf("optional body means all files: %#v", jobs.started)
	}
}

func TestAnalyzeStartBodyAndErrors(t *testing.T) {
	ts, _, jobs := bountyServer(t)
	u := ts.URL + "/api/bounty/analyze"
	h := map[string]string{"X-Radar": "1"}
	r, _ := do(t, "POST", u, `{"files":["WoWCombatLog-a.txt","WoWCombatLog-b.txt"]}`, h)
	if r.StatusCode != 202 || len(jobs.started) != 1 || strings.Join(jobs.started[0], ",") != "WoWCombatLog-a.txt,WoWCombatLog-b.txt" {
		t.Fatalf("%d %#v", r.StatusCode, jobs.started)
	}
	for _, body := range []string{`{`, `[]`, `{"files":"x"}`, `{"files":[1]}`, `{"files":[]}x`, `{"bogus":1}`} {
		if r, _ := do(t, "POST", u, body, h); r.StatusCode != 400 {
			t.Errorf("body %q -> %d", body, r.StatusCode)
		}
	}
	// oversized body
	if r, _ := do(t, "POST", u, `{"files":["`+strings.Repeat("a", 200000)+`"]}`, h); r.StatusCode != 400 && r.StatusCode != 413 {
		t.Errorf("huge body -> %d", r.StatusCode)
	}
	// errors from the job runner
	jobs.err = bounty.ErrBusy
	if r, _ := do(t, "POST", u, "", h); r.StatusCode != 409 {
		t.Fatalf("busy %d", r.StatusCode)
	}
	jobs.err = &bounty.BadFileError{Name: "../x", Reason: "no"}
	r, b := do(t, "POST", u, `{"files":["../x"]}`, h)
	if r.StatusCode != 400 {
		t.Fatalf("bad file %d %s", r.StatusCode, b)
	}
	jobs.err = errors.New("disk on fire")
	if r, _ := do(t, "POST", u, "", h); r.StatusCode != 500 {
		t.Fatalf("other %d", r.StatusCode)
	}
	if r, _ := do(t, "PUT", u, "", h); r.StatusCode != 405 {
		t.Fatalf("PUT %d", r.StatusCode)
	}
}

func TestBountyEndpointsAbsentWithoutStore(t *testing.T) {
	_, ts := newServer(t, nil)
	for _, p := range []string{"/api/bounty", "/api/bounty/player?guid=x", "/api/bounty/analyze"} {
		if r, _ := getS(t, ts.URL+p); r.StatusCode != 404 {
			t.Errorf("%s -> %d", p, r.StatusCode)
		}
	}
}

func TestBountyEndpointsHonourHostGuard(t *testing.T) {
	ts, _, _ := bountyServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/api/bounty", nil)
	req.Host = "evil.example"
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatalf("%d", r.StatusCode)
	}
}

func TestPathTraversalInGuidIsJustAnUnknownKey(t *testing.T) {
	ts, _, _ := bountyServer(t)
	for _, g := range []string{"../../etc/passwd", "..%5C..%5Cwindows", "Player-1-E1%00"} {
		if r, _ := getS(t, ts.URL+"/api/bounty/player?guid="+g); r.StatusCode != 404 && r.StatusCode != 400 {
			t.Errorf("%s -> %d", g, r.StatusCode)
		}
	}
}

func getS(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	r, b := get(t, url)
	return r, string(b)
}
