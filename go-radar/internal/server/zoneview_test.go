package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"nearbyradar/internal/tracker"
)

type zoneState struct{ snap tracker.Snapshot }

func (z zoneState) Snapshot() tracker.Snapshot { return z.snap }
func (z zoneState) Version() uint64            { return 1 }

func stateView(t *testing.T, snap tracker.Snapshot) *ZoneView {
	t.Helper()
	s := New(Options{State: zoneState{snap}, History: seededHist(), Maps: mapsIndex(t), Orientation: "std"})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	_, body := get(t, ts.URL+"/api/state")
	var out struct {
		View struct {
			Zone *ZoneView `json:"zone"`
		} `json:"view"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out.View.Zone
}

// ZONE_CHANGE is often missing (the app starts mid-log), so the zone image must also be found
// from the MAP_CHANGE name of the current map id.
func TestZoneImageFoundFromMapNameWhenZoneEmpty(t *testing.T) {
	z := stateView(t, tracker.Snapshot{Zone: "", MapID: 1426,
		Maps: map[string]tracker.MapInfo{"1426": {Name: "Dun Morogh"}}})
	if z == nil || z.URL != "/api/maps/DunMorogh.png" {
		t.Fatalf("zone view = %+v, want DunMorogh.png", z)
	}
}

func TestZoneImageMapNameWinsOverStaleZone(t *testing.T) {
	// the zone text can lag behind the current map; the current map's name is authoritative
	z := stateView(t, tracker.Snapshot{Zone: "Nowhere", MapID: 1426,
		Maps: map[string]tracker.MapInfo{"1426": {Name: "Dun Morogh"}}})
	if z == nil || z.URL != "/api/maps/DunMorogh.png" {
		t.Fatalf("zone view = %+v", z)
	}
}

func TestZoneImageFallsBackToZoneName(t *testing.T) {
	z := stateView(t, tracker.Snapshot{Zone: "Dun Morogh", MapID: 0})
	if z == nil {
		t.Fatal("expected zone view from zone name")
	}
}
