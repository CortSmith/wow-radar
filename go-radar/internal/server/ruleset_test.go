package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nearbyradar/internal/bounty"
	"nearbyradar/internal/dataset"
	"nearbyradar/internal/history"
)

func rulesetServer(t *testing.T, dir string) (*httptest.Server, *dataset.Registry) {
	t.Helper()
	reg, err := dataset.New(dataset.Options{HistoryPath: filepath.Join(dir, "h.json")})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{State: &fakeState{}, History: reg, Bounty: reg, Rulesets: reg, Interval: 10 * time.Millisecond})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, reg
}

type rsResp struct {
	Active   string  `json:"active"`
	Mode     string  `json:"mode"`
	Detected *string `json:"detected"`
	Rulesets []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		HasData bool   `json:"hasData"`
		Players int    `json:"players"`
	} `json:"rulesets"`
}

func postRS(t *testing.T, url, body string, hdr map[string]string) (int, rsResp, string) {
	t.Helper()
	r, b := do(t, "POST", url+"/api/ruleset", body, hdr)
	var out rsResp
	_ = json.Unmarshal([]byte(b), &out)
	return r.StatusCode, out, b
}

func TestRulesetGet(t *testing.T) {
	ts, _ := rulesetServer(t, t.TempDir())
	r, b := getS(t, ts.URL+"/api/ruleset")
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	var out rsResp
	if err := json.Unmarshal([]byte(b), &out); err != nil {
		t.Fatal(err)
	}
	if out.Active != "pvp" || out.Mode != "auto" || out.Detected != nil || len(out.Rulesets) != 4 {
		t.Fatalf("%s", b)
	}
	if !strings.Contains(b, `"detected":null`) {
		t.Fatalf("detected must be null: %s", b)
	}
	names := []string{}
	for _, e := range out.Rulesets {
		names = append(names, e.ID+"="+e.Name)
	}
	if strings.Join(names, ",") != "normal=Normal,pvp=PvP,rp=RP,rppvp=RP-PvP" {
		t.Fatal(names)
	}
	if r, _ := do(t, "DELETE", ts.URL+"/api/ruleset", "", nil); r.StatusCode != 405 {
		t.Fatalf("%d", r.StatusCode)
	}
}

func TestRulesetPostSwitchesDatasetsEndToEnd(t *testing.T) {
	ts, reg := rulesetServer(t, t.TempDir())
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	reg.Record(history.Sample{GUID: "Player-1-A", Name: "Alpha-PvP-US", Display: "Alpha", Time: t0, HasPos: true, X: 1, Y: 2, MapID: 5})
	k := bounty.Participant{GUID: "Player-1-K", Raw: "Killer", Display: "Killer"}
	reg.RecordDeath(bounty.Death{ID: "1", Time: t0, VictimGUID: "Player-1-V", VictimName: "V", Killer: &k})

	count := func(path string) int {
		_, b := getS(t, ts.URL+path)
		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(b), &arr); err != nil {
			t.Fatalf("%s: %s", path, b)
		}
		return len(arr)
	}
	if count("/api/players?q=alpha") != 1 || count("/api/history") != 1 {
		t.Fatal("pvp data expected")
	}
	if r, _ := getS(t, ts.URL+"/api/track?guid=Player-1-A"); r.StatusCode != 200 {
		t.Fatal("track under pvp")
	}
	if r, _ := getS(t, ts.URL+"/api/bounty/player?guid=Player-1-K"); r.StatusCode != 200 {
		t.Fatal("bounty player under pvp")
	}

	code, out, body := postRS(t, ts.URL, `{"ruleset":"rp"}`, map[string]string{"X-Radar": "1"})
	if code != 200 || out.Active != "rp" || out.Mode != "manual" {
		t.Fatalf("%d %s", code, body)
	}
	if count("/api/players?q=alpha") != 0 || count("/api/history") != 0 {
		t.Fatal("rp must not see pvp players")
	}
	if r, _ := getS(t, ts.URL+"/api/track?guid=Player-1-A"); r.StatusCode != 404 {
		t.Fatalf("track must be unknown under rp: %d", r.StatusCode)
	}
	if r, _ := getS(t, ts.URL+"/api/bounty/player?guid=Player-1-K"); r.StatusCode != 404 {
		t.Fatal("bounty player must be unknown under rp")
	}
	_, b := getS(t, ts.URL+"/api/bounty")
	var bo struct {
		Rows  []bounty.Row `json:"rows"`
		Stats bounty.Stats `json:"stats"`
	}
	_ = json.Unmarshal([]byte(b), &bo)
	if len(bo.Rows) != 0 || bo.Stats.Deaths != 0 || !strings.Contains(b, `"rows":[]`) {
		t.Fatalf("%s", b)
	}
	// GET reflects the switch and the counts
	_, gb := getS(t, ts.URL+"/api/ruleset")
	var g rsResp
	_ = json.Unmarshal([]byte(gb), &g)
	for _, e := range g.Rulesets {
		if (e.ID == "pvp") != e.HasData || (e.ID == "pvp") != (e.Players == 1) {
			t.Fatalf("%s", gb)
		}
	}
	// back to pvp, then auto
	if code, out, _ := postRS(t, ts.URL, `{"ruleset":"PvP"}`, map[string]string{"Content-Type": "application/json"}); code != 200 || out.Active != "pvp" {
		t.Fatal("json content type is accepted")
	}
	if count("/api/players?q=alpha") != 1 {
		t.Fatal("pvp data back")
	}
	reg.ObserveMe("Me-Realm-RP-US")
	if code, out, _ := postRS(t, ts.URL, `{"ruleset":"auto"}`, map[string]string{"X-Radar": "1"}); code != 200 || out.Active != "rp" || out.Mode != "auto" || out.Detected == nil || *out.Detected != "rp" {
		t.Fatalf("auto -> detected rp: %+v", out)
	}
}

func TestRulesetPostBadInput(t *testing.T) {
	ts, reg := rulesetServer(t, t.TempDir())
	h := map[string]string{"X-Radar": "1"}
	for name, body := range map[string]string{
		"unknown": `{"ruleset":"pve"}`, "empty": `{"ruleset":""}`, "missing": `{}`, "badjson": `{`,
		"empty body": ``, "extra field": `{"ruleset":"rp","x":1}`, "trailing": `{"ruleset":"rp"} {}`, "wrong type": `{"ruleset":5}`,
	} {
		if code, _, b := postRS(t, ts.URL, body, h); code != 400 {
			t.Errorf("%s: %d %s", name, code, b)
		}
	}
	big := `{"ruleset":"` + strings.Repeat("a", 5000) + `"}`
	if code, _, _ := postRS(t, ts.URL, big, h); code != 413 && code != 400 {
		t.Errorf("oversized: %d", code)
	}
	if reg.Active() != "pvp" || reg.Mode() != "auto" {
		t.Fatal("bad input must not change state")
	}
}

func TestRulesetPostCSRF(t *testing.T) {
	ts, reg := rulesetServer(t, t.TempDir())
	body := `{"ruleset":"rp"}`
	if code, _, _ := postRS(t, ts.URL, body, nil); code != 403 {
		t.Errorf("no header / plain content type: %d", code)
	}
	if code, _, _ := postRS(t, ts.URL, body, map[string]string{"X-Radar": "1", "Origin": "https://evil.example"}); code != 403 {
		t.Errorf("foreign origin: %d", code)
	}
	if code, _, _ := postRS(t, ts.URL, body, map[string]string{"Content-Type": "text/plain"}); code != 403 {
		t.Errorf("text/plain: %d", code)
	}
	if reg.Active() != "pvp" {
		t.Fatal("rejected requests must not switch")
	}
	if code, _, _ := postRS(t, ts.URL, body, map[string]string{"X-Radar": "1", "Origin": "http://127.0.0.1:1234"}); code != 200 {
		t.Errorf("loopback origin: %d", code)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/api/ruleset", strings.NewReader(`{"ruleset":"pvp"}`))
	req.Host = "evil.example"
	req.Header.Set("X-Radar", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 || reg.Active() != "rp" {
		t.Fatalf("host check: %d %s", resp.StatusCode, reg.Active())
	}
}

func TestStateViewCarriesRuleset(t *testing.T) {
	ts, reg := rulesetServer(t, t.TempDir())
	reg.ObserveMe("Me-Realm-RPPvP-US")
	_, b := getS(t, ts.URL+"/api/state")
	var st struct {
		View struct {
			Orientation string
			Ruleset     *struct {
				Active   string
				Mode     string
				Detected string
			}
		}
	}
	if err := json.Unmarshal([]byte(b), &st); err != nil {
		t.Fatal(err)
	}
	if st.View.Orientation == "" || st.View.Ruleset == nil || st.View.Ruleset.Active != "rppvp" || st.View.Ruleset.Mode != "auto" || st.View.Ruleset.Detected != "rppvp" {
		t.Fatalf("%s", b)
	}
}

func TestNoRulesetsOptionKeepsOldBehaviour(t *testing.T) {
	ts, _, _ := bountyServer(t)
	if r, _ := getS(t, ts.URL+"/api/ruleset"); r.StatusCode == 200 {
		t.Fatal("endpoint must not exist without a source")
	}
	_, b := getS(t, ts.URL+"/api/state")
	if strings.Contains(b, `"ruleset"`) {
		t.Fatalf("no ruleset in state without a source: %s", b)
	}
}
