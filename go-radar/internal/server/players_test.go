package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nearbyradar/internal/history"
)

var st0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// seededHist returns a history with two players (one with a track on two maps).
func seededHist() fakeHist {
	h := history.New("")
	h.Record(history.Sample{GUID: "Player-1-A", Name: "Alice-PvP-US", Display: "Alice", Reaction: "friendly", Time: st0, HasPos: true, X: 10, Y: 20, MapID: 1426, Zone: "Dun Morogh"})
	h.Record(history.Sample{GUID: "Player-1-A", Time: st0.Add(time.Minute), HasPos: true, X: 50, Y: 60, MapID: 1426, Zone: "Dun Morogh"})
	h.Record(history.Sample{GUID: "Player-1-A", Time: st0.Add(2 * time.Minute), HasPos: true, X: 1, Y: 2, MapID: 1453, Zone: "Stormwind"})
	h.Record(history.Sample{GUID: "Player-2-B", Name: "Bob-PvP-US", Display: "Bob", Reaction: "hostile", Time: st0.Add(time.Hour)})
	return fakeHist{h}
}

func TestPlayersEndpoint(t *testing.T) {
	_, ts := newServer(t, nil)
	var rows []history.Match
	r, b := get(t, ts.URL+"/api/players?q=ALI")
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	if err := json.Unmarshal(b, &rows); err != nil || len(rows) != 1 || rows[0].GUID != "Player-1-A" || rows[0].Name != "Alice" || rows[0].Fixes != 3 {
		t.Fatalf("%v %s", err, b)
	}
	_, b = get(t, ts.URL+"/api/players?q=pvp")
	_ = json.Unmarshal(b, &rows)
	if len(rows) != 2 || rows[0].GUID != "Player-2-B" {
		t.Fatalf("sorted by last seen: %s", b)
	}
	_, b = get(t, ts.URL+"/api/players?q=nobody")
	if strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("unknown: %s", b)
	}
	_, b = get(t, ts.URL+"/api/players")
	_ = json.Unmarshal(b, &rows)
	if len(rows) != 2 {
		t.Fatalf("no q lists recent players: %s", b)
	}
	// absurdly long query is harmless
	if r, _ := get(t, ts.URL+"/api/players?q="+strings.Repeat("a", 5000)); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	resp, err := http.Post(ts.URL+"/api/players", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST %d", resp.StatusCode)
	}
}

func TestTrackEndpoint(t *testing.T) {
	_, ts := newServer(t, nil)
	var tr history.TrackResult
	r, b := get(t, ts.URL+"/api/track?guid=Player-1-A")
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	if err := json.Unmarshal(b, &tr); err != nil || len(tr.Fixes) != 3 || tr.Total != 3 || len(tr.Maps) != 2 || tr.Name != "Alice" || tr.Reaction != "friendly" {
		t.Fatalf("%v %s", err, b)
	}
	if tr.Fixes[0].T != st0.UnixMilli() || tr.Fixes[0].X != 10 || tr.Fixes[0].Kind != "exact" || tr.Fixes[2].T <= tr.Fixes[1].T {
		t.Fatalf("order/fields: %+v", tr.Fixes)
	}
	_, b = get(t, ts.URL+"/api/track?guid=Player-1-A&map=1453")
	tr = history.TrackResult{}
	_ = json.Unmarshal(b, &tr)
	if len(tr.Fixes) != 1 || tr.Total != 3 || tr.Fixes[0].MapID != 1453 {
		t.Fatalf("map filter: %s", b)
	}
	_, b = get(t, ts.URL+"/api/track?guid=Player-1-A&map=9")
	if !strings.Contains(string(b), `"fixes":[]`) {
		t.Fatalf("unknown map: %s", b)
	}
	_, b = get(t, ts.URL+"/api/track?guid=Player-2-B")
	if !strings.Contains(string(b), `"fixes":[]`) || !strings.Contains(string(b), `"maps":[]`) {
		t.Fatalf("player without fixes: %s", b)
	}
}

func TestTrackEndpointBadInput(t *testing.T) {
	_, ts := newServer(t, nil)
	cases := map[string]int{
		"/api/track":                                  400,
		"/api/track?guid=":                            400,
		"/api/track?guid=Player-1-A&map=abc":          400,
		"/api/track?guid=Player-1-A&map=-3":           400,
		"/api/track?guid=Player-1-A&map=1.5":          400,
		"/api/track?guid=Player-9-ZZ":                 404,
		"/api/track?guid=" + strings.Repeat("x", 500): 404,
	}
	for p, want := range cases {
		if r, b := get(t, ts.URL+p); r.StatusCode != want {
			t.Errorf("%s: %d (%s) want %d", p, r.StatusCode, b, want)
		}
	}
}

func TestPlayersAndTrackRejectForeignHost(t *testing.T) {
	s, _ := newServer(t, nil)
	for _, p := range []string{"/api/players?q=a", "/api/track?guid=Player-1-A"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", p, nil)
		req.Host = "evil.example.com"
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
}

func TestUIHasSearchAndTrack(t *testing.T) {
	_, ts := newServer(t, nil)
	_, idx := get(t, ts.URL+"/")
	for _, want := range []string{`id="q"`, `id="qclear"`, `id="results"`, `id="trackInfo"`} {
		if !strings.Contains(string(idx), want) {
			t.Errorf("index.html lacks %s", want)
		}
	}
	_, js := get(t, ts.URL+"/app.js")
	for _, want := range []string{"/api/players?q=", "/api/track?guid=", "setLineDash", "Escape"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js lacks %s", want)
		}
	}
	if !strings.Contains(string(idx), `<section id="history" hidden>`) {
		t.Error("history panel must stay hidden by default")
	}
}
