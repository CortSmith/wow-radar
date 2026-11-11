package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nearbyradar/internal/history"
	"nearbyradar/internal/maps"
	"nearbyradar/internal/tracker"
)

type fakeState struct{ ver atomic.Uint64 }

func (f *fakeState) Snapshot() tracker.Snapshot {
	return tracker.Snapshot{Zone: "Dun Morogh", MapID: 1426, Pips: []tracker.Pip{{GUID: "Player-1-A", Name: "A", State: "active", Reaction: "friendly"}}}
}
func (f *fakeState) Version() uint64 { return f.ver.Load() }

type fakeHist struct{ *history.Store }

func (fakeHist) Snapshot() []history.Entry {
	return []history.Entry{{GUID: "Player-1-A", Name: "A", AvgDamagePerHit: 12.5}}
}

func mapsIndex(t *testing.T) *maps.Index {
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "index.json"), []byte(`{"zones":{"dunmorogh":{"name":"Dun Morogh","file":"DunMorogh.png","w":10,"h":5,"revealed":true}}}`), 0o600)
	_ = os.WriteFile(filepath.Join(d, "DunMorogh.png"), []byte("PNGDATA"), 0o600)
	ix, err := maps.Load(d)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func newServer(t *testing.T, ix *maps.Index) (*Server, *httptest.Server) {
	s := New(Options{State: &fakeState{}, History: seededHist(), Maps: ix, Orientation: "std",
		Status: func() Status { return Status{LogFile: "WoWCombatLog-1.txt", LogsDir: "C:/logs"} }, Interval: 10 * time.Millisecond})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r, b
}

func TestStateEndpoint(t *testing.T) {
	_, ts := newServer(t, mapsIndex(t))
	r, b := get(t, ts.URL+"/api/state")
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	var got struct {
		Zone string
		Pips []struct{ GUID, Name, State string }
		View struct {
			Orientation string
			Zone        *struct {
				URL       string
				W, H      int
				Revealed  bool
				Available bool
			}
			LogFile string
		}
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err, string(b))
	}
	if len(got.Pips) != 1 || got.Pips[0].GUID != "Player-1-A" || got.Zone != "Dun Morogh" {
		t.Fatalf("%s", b)
	}
	if got.View.Orientation != "std" || got.View.LogFile != "WoWCombatLog-1.txt" {
		t.Fatalf("view %+v", got.View)
	}
	if got.View.Zone == nil || got.View.Zone.URL != "/api/maps/DunMorogh.png" || got.View.Zone.W != 10 {
		t.Fatalf("zone %+v", got.View.Zone)
	}
	if strings.Contains(string(b), "avgDamage") {
		t.Fatal("history must not be part of the live state")
	}
}

func TestStateWithoutMapsIsFine(t *testing.T) {
	_, ts := newServer(t, nil)
	_, b := get(t, ts.URL+"/api/state")
	if !strings.Contains(string(b), `"zone":null`) {
		t.Fatalf("%s", b)
	}
}

func TestHistoryEndpoint(t *testing.T) {
	_, ts := newServer(t, nil)
	r, b := get(t, ts.URL+"/api/history")
	if r.StatusCode != 200 || !strings.Contains(string(b), `"avgDamagePerHit":12.5`) {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
}

func TestMapImages(t *testing.T) {
	_, ts := newServer(t, mapsIndex(t))
	r, b := get(t, ts.URL+"/api/maps/DunMorogh.png")
	if r.StatusCode != 200 || string(b) != "PNGDATA" || r.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("%d %q %s", r.StatusCode, b, r.Header.Get("Content-Type"))
	}
	for _, p := range []string{"/api/maps/index.json", "/api/maps/..%2Findex.json", "/api/maps/nothere.png"} {
		if r, _ := get(t, ts.URL+p); r.StatusCode != 404 {
			t.Errorf("%s -> %d", p, r.StatusCode)
		}
	}
	_, ts2 := newServer(t, nil)
	if r, _ := get(t, ts2.URL+"/api/maps/DunMorogh.png"); r.StatusCode != 404 {
		t.Fatal("no maps -> 404")
	}
}

func TestEmbeddedUI(t *testing.T) {
	_, ts := newServer(t, nil)
	r, b := get(t, ts.URL+"/")
	if r.StatusCode != 200 || !strings.Contains(string(b), "<canvas") {
		t.Fatalf("%d", r.StatusCode)
	}
	// history panel exists but starts hidden
	if !strings.Contains(string(b), `id="history"`) || !strings.Contains(string(b), "hidden") {
		t.Fatal("history panel must exist and default to hidden")
	}
	if r, _ := get(t, ts.URL+"/app.js"); r.StatusCode != 200 {
		t.Fatal("app.js")
	}
	if r, _ := get(t, ts.URL+"/nope.xyz"); r.StatusCode != 404 {
		t.Fatal("404")
	}
}

func TestHostHeaderCheck(t *testing.T) {
	s, _ := newServer(t, nil)
	h := s.Handler()
	req := httptest.NewRequest("GET", "http://evil.example/api/state", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
	req = httptest.NewRequest("GET", "http://127.0.0.1:1234/api/state", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("loopback host must be allowed: %d", rec.Code)
	}
	req = httptest.NewRequest("GET", "http://localhost:1234/api/state", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal("localhost allowed")
	}
}

func TestSSEStreamsState(t *testing.T) {
	_, ts := newServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("ct %s", resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	var sawEvent, sawData bool
	for sc.Scan() {
		l := sc.Text()
		if l == "event: state" {
			sawEvent = true
		}
		if strings.HasPrefix(l, "data: {") {
			sawData = strings.Contains(l, "Player-1-A")
			break
		}
	}
	if !sawEvent || !sawData {
		t.Fatal("no state event")
	}
}

func TestStartAndShutdown(t *testing.T) {
	s := New(Options{State: &fakeState{}, History: seededHist()})
	ctx, cancel := context.WithCancel(context.Background())
	addr, err := s.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") || s.URL() != "http://"+addr+"/" {
		t.Fatalf("addr %q url %q", addr, s.URL())
	}
	if r, _ := get(t, s.URL()+"api/state"); r.StatusCode != 200 {
		t.Fatal("not serving")
	}
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := http.Get(s.URL() + "api/state"); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server still up after cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestClientsCounter(t *testing.T) {
	s, ts := newServer(t, nil)
	if s.Clients() != 0 {
		t.Fatal("initial")
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for s.Clients() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Clients() != 1 {
		t.Fatalf("clients %d", s.Clients())
	}
	cancel()
	for s.Clients() != 0 && time.Now().Before(deadline.Add(2*time.Second)) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Clients() != 0 {
		t.Fatal("client not released")
	}
}
