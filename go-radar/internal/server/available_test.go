package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nearbyradar/internal/maps"
	"nearbyradar/internal/tracker"
)

func twoMapIndex(t *testing.T) *maps.Index {
	t.Helper()
	d := t.TempDir()
	idx := `{"zones":{
"dunmorogh":{"name":"Dun Morogh","file":"DunMorogh.png","w":10,"h":5,"revealed":true},
"azshara":{"name":"Azshara","file":"Azshara.png","w":20,"h":8,"revealed":false}}}`
	_ = os.WriteFile(filepath.Join(d, "index.json"), []byte(idx), 0o600)
	ix, err := maps.Load(d)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func rawView(t *testing.T, snap tracker.Snapshot, ix *maps.Index) map[string]json.RawMessage {
	t.Helper()
	s := New(Options{State: zoneState{snap}, History: seededHist(), Maps: ix})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	_, body := get(t, ts.URL+"/api/state")
	var out struct {
		View map[string]json.RawMessage `json:"view"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out.View
}

type availRow struct {
	MapID    int    `json:"mapId"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	W        int    `json:"w"`
	H        int    `json:"h"`
	Revealed bool   `json:"revealed"`
}

func rect(name string) tracker.MapInfo {
	return tracker.MapInfo{Name: name, X0: -100, X1: 100, Y0: -50, Y1: 50}
}

func TestAvailableSortedAndJoined(t *testing.T) {
	snap := tracker.Snapshot{MapID: 1426, Maps: map[string]tracker.MapInfo{
		"1426": rect("Dun Morogh"), "1447": rect("Azshara"),
		"9999": rect("Mystery Isle"),
		"5":    {Name: "Azshara"}, // no rect: absent
	}}
	v := rawView(t, snap, twoMapIndex(t))
	var av []availRow
	if err := json.Unmarshal(v["available"], &av); err != nil {
		t.Fatal(err)
	}
	if len(av) != 2 || av[0].Name != "Azshara" || av[1].Name != "Dun Morogh" {
		t.Fatalf("available = %+v", av)
	}
	if av[0].MapID != 1447 || av[0].URL != "/api/maps/Azshara.png" || av[0].W != 20 || av[0].H != 8 || av[0].Revealed {
		t.Fatalf("row0 = %+v", av[0])
	}
	if av[1].MapID != 1426 || !av[1].Revealed {
		t.Fatalf("row1 = %+v", av[1])
	}
	var ni []struct {
		MapID int    `json:"mapId"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(v["noImage"], &ni); err != nil {
		t.Fatal(err)
	}
	if len(ni) != 1 || ni[0].MapID != 9999 || ni[0].Name != "Mystery Isle" {
		t.Fatalf("noImage = %+v", ni)
	}
	if _, ok := v["zone"]; !ok {
		t.Fatal("view.zone must be kept")
	}
}

func TestAvailableEmptyArraysNotNull(t *testing.T) {
	for _, ix := range []*maps.Index{nil, twoMapIndex(t)} {
		v := rawView(t, tracker.Snapshot{}, ix)
		for _, k := range []string{"available", "noImage"} {
			if strings.TrimSpace(string(v[k])) != "[]" {
				t.Fatalf("%s = %s, want []", k, v[k])
			}
		}
	}
}

func TestAvailableNilIndexListsRectMapsAsNoImage(t *testing.T) {
	v := rawView(t, tracker.Snapshot{Maps: map[string]tracker.MapInfo{"7": rect("Azshara")}}, nil)
	if string(v["available"]) != "[]" || !strings.Contains(string(v["noImage"]), `"mapId":7`) {
		t.Fatalf("available=%s noImage=%s", v["available"], v["noImage"])
	}
}
