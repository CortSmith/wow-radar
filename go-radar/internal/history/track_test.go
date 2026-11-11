package history

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func posSample(g string, at time.Time, x, y float64, m int) Sample {
	return Sample{GUID: g, Time: at, HasPos: true, X: x, Y: y, MapID: m, Zone: "Zone"}
}

func TestTrackRecordsExactAndApproxOnly(t *testing.T) {
	s := New("")
	s.Record(Sample{GUID: "G", Time: t0}) // no position: no fix
	s.Record(posSample("G", t0, 10, 20, 7))
	a := posSample("G", t0.Add(time.Minute), 50, 60, 7)
	a.Approx = true
	s.Record(a)
	r, ok := s.Track("G", 0)
	if !ok || len(r.Fixes) != 2 || r.Total != 2 {
		t.Fatalf("%+v", r)
	}
	f0, f1 := r.Fixes[0], r.Fixes[1]
	if f0.Kind != KindExact || f1.Kind != KindApprox || f0.T != t0.UnixMilli() || f0.MapID != 7 || f0.Zone != "Zone" || f0.X != 10 || f0.Y != 20 {
		t.Fatalf("%+v %+v", f0, f1)
	}
	e, _ := s.Get("G")
	if e.LastX != 10 || e.Fixes != 2 || e.Track != nil {
		t.Fatalf("approx must not move the last exact position, Get must not carry the track: %+v", e)
	}
}

func TestTrackDedupe(t *testing.T) {
	s := New("")
	s.Record(posSample("G", t0, 0, 0, 1))
	s.Record(posSample("G", t0.Add(time.Second), MinFixDistance-0.5, 0, 1))                // near + soon: skipped
	s.Record(posSample("G", t0.Add(MinFixInterval), MinFixDistance-0.5, 0, 1))             // near but late enough: kept
	s.Record(posSample("G", t0.Add(MinFixInterval+time.Second), MinFixDistance+5, 0, 2))   // other map: kept
	s.Record(posSample("G", t0.Add(MinFixInterval+2*time.Second), MinFixDistance+9, 0, 2)) // far enough from prev (4.0)? dist 4 >= 2: kept
	r, _ := s.Track("G", 0)
	if len(r.Fixes) != 4 {
		t.Fatalf("got %d fixes: %+v", len(r.Fixes), r.Fixes)
	}
	// moving fast is never dropped
	s.Record(posSample("H", t0, 0, 0, 1))
	s.Record(posSample("H", t0.Add(time.Second), MinFixDistance+1, 0, 1))
	if r, _ := s.Track("H", 0); len(r.Fixes) != 2 {
		t.Fatal("far fix must be kept")
	}
}

func TestTrackOutOfOrderKeptSorted(t *testing.T) {
	s := New("")
	s.Record(posSample("G", t0.Add(time.Hour), 100, 0, 1))
	s.Record(posSample("G", t0, 0, 0, 1))
	s.Record(posSample("G", t0.Add(30*time.Minute), 50, 0, 1))
	r, _ := s.Track("G", 0)
	if len(r.Fixes) != 3 || r.Fixes[0].X != 0 || r.Fixes[1].X != 50 || r.Fixes[2].X != 100 {
		t.Fatalf("%+v", r.Fixes)
	}
}

func TestTrackCapPerPlayerDropsOldest(t *testing.T) {
	s := New("")
	n := MaxTrackPerPlayer + 10
	for i := 0; i < n; i++ {
		s.Record(posSample("G", t0.Add(time.Duration(i)*time.Minute), float64(i*10), 0, 1))
	}
	r, _ := s.Track("G", 0)
	if len(r.Fixes) != MaxTrackPerPlayer {
		t.Fatalf("len %d", len(r.Fixes))
	}
	if r.Fixes[0].X != 100 || r.Fixes[len(r.Fixes)-1].X != float64((n-1)*10) {
		t.Fatalf("oldest not dropped: first %v", r.Fixes[0])
	}
}

func TestTrackTotalCapEvictsLeastRecentPlayers(t *testing.T) {
	old := maxTotalFixes
	maxTotalFixes = 100
	defer func() { maxTotalFixes = old }()
	s := New("")
	for p := 0; p < 12; p++ {
		g := fmt.Sprintf("P%02d", p)
		for i := 0; i < 10; i++ {
			s.Record(posSample(g, t0.Add(time.Duration(p)*time.Hour+time.Duration(i)*time.Minute), float64(i*10), 0, 1))
		}
	}
	total := 0
	for _, e := range s.Snapshot() {
		total += e.Fixes
	}
	if total > 100 {
		t.Fatalf("total %d", total)
	}
	if e, ok := s.Get("P00"); !ok || e.Fixes != 0 || e.Events == 0 {
		t.Fatalf("oldest player should lose its track but keep aggregates: %+v", e)
	}
	if e, _ := s.Get("P11"); e.Fixes != 10 {
		t.Fatalf("newest must be intact: %+v", e)
	}
}

func TestTrackMapFilterAndCounts(t *testing.T) {
	s := New("")
	s.Record(posSample("G", t0, 1, 1, 1))
	s.Record(posSample("G", t0.Add(time.Minute), 100, 1, 2))
	s.Record(posSample("G", t0.Add(2*time.Minute), 200, 1, 2))
	r, _ := s.Track("G", 2)
	if len(r.Fixes) != 2 || r.Total != 3 || len(r.Maps) != 2 || r.Maps[0].MapID != 1 || r.Maps[1].Count != 2 {
		t.Fatalf("%+v", r)
	}
	if r, ok := s.Track("G", 99); !ok || len(r.Fixes) != 0 || r.Fixes == nil {
		t.Fatal("unknown map: empty non-nil list")
	}
	if _, ok := s.Track("nope", 0); ok {
		t.Fatal("unknown guid")
	}
	// mutating the result does not touch the store
	r, _ = s.Track("G", 0)
	r.Fixes[0].X = 12345
	if r2, _ := s.Track("G", 0); r2.Fixes[0].X == 12345 {
		t.Fatal("must return a copy")
	}
}

func TestSearchCaseInsensitiveNamesAndPast(t *testing.T) {
	s := New("")
	s.Record(Sample{GUID: "Player-1", Name: "Alice-PvP-US", Display: "Alice", Time: t0, Reaction: "friendly"})
	s.Record(posSample("Player-1", t0.Add(time.Minute), 1, 1, 1))
	s.Record(Sample{GUID: "Player-1", Name: "Alicia-PvP-US", Display: "Alicia", Time: t0.Add(2 * time.Minute)})
	s.Record(Sample{GUID: "Player-2", Name: "Bob-PvP-US", Display: "Bob", Time: t0.Add(time.Hour)})
	s.Record(Sample{GUID: "Player-3", Time: t0}) // nameless: never matches a non-empty query
	got := s.Search("ALIC", 50)
	if len(got) != 1 || got[0].GUID != "Player-1" || got[0].Name != "Alicia" || got[0].Fixes != 1 || got[0].Reaction != "friendly" {
		t.Fatalf("%+v", got)
	}
	if got := s.Search("alice", 50); len(got) != 1 {
		t.Fatalf("past name must match: %+v", got)
	}
	if got := s.Search("pvp-us", 50); len(got) != 2 || got[0].GUID != "Player-2" {
		t.Fatalf("raw name, sorted by last seen: %+v", got)
	}
	if got := s.Search("zzz", 50); got == nil || len(got) != 0 {
		t.Fatalf("no match must be empty non-nil: %#v", got)
	}
	if got := s.Search("", 50); len(got) != 2 {
		t.Fatalf("empty query lists named players: %+v", got)
	}
	if got := s.Search("b", 1); len(got) != 1 {
		t.Fatal("limit")
	}
	e, _ := s.Get("Player-1")
	if len(e.PastNames) == 0 || e.Name != "Alicia-PvP-US" {
		t.Fatalf("%+v", e)
	}
}

func TestSearchDisplayFallbackWhenOnlyRaw(t *testing.T) {
	s := New("")
	s.Record(Sample{GUID: "G", Name: "Old-Name", Time: t0})
	got := s.Search("old name", 5)
	if len(got) != 1 || got[0].Name != "Old Name" {
		t.Fatalf("%+v", got)
	}
}

func TestTrackPersistsAndOldFilesLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "h.json")
	s := New(p)
	s.Record(Sample{GUID: "G", Name: "N", Display: "N", Time: t0, Reaction: "hostile"})
	s.Record(posSample("G", t0, 5, 6, 3))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "\n ") {
		t.Fatal("history must be written compact")
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := s2.Track("G", 0)
	if !ok || len(r.Fixes) != 1 || r.Fixes[0].X != 5 || r.Reaction != "hostile" {
		t.Fatalf("%+v", r)
	}
	// version 1 file without tracks
	old := filepath.Join(dir, "old.json")
	v1 := `{"version":1,"entries":{"Player-9":{"guid":"Player-9","name":"Zed-R","firstSeen":"2026-01-01T12:00:00Z","lastSeen":"2026-01-01T13:00:00Z","events":4,"hasPos":true,"lastX":1,"lastY":2,"lastMap":5}}}`
	_ = os.WriteFile(old, []byte(v1), 0o600)
	s3, err := Open(old)
	if err != nil {
		t.Fatal(err)
	}
	if got := s3.Search("zed", 5); len(got) != 1 || got[0].Fixes != 0 {
		t.Fatalf("%+v", got)
	}
	s3.Record(posSample("Player-9", t0, 1, 1, 5)) // can start a track on a migrated entry
	if r, _ := s3.Track("Player-9", 0); len(r.Fixes) != 1 {
		t.Fatal("track on migrated entry")
	}
	if err := s3.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestFlushNotDirtyAfterReadOnlyCalls(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "h.json")
	s := New(p)
	s.Record(posSample("G", t0, 1, 1, 1))
	_ = s.Flush()
	_ = os.Remove(p)
	s.Search("g", 5)
	s.Track("G", 0)
	s.Snapshot()
	_ = s.Flush()
	if _, err := os.Stat(p); err == nil {
		t.Fatal("reads must not make the store dirty")
	}
	s.Record(posSample("G", t0.Add(time.Second), 1, 1, 1)) // fully deduped: nothing changed except events
	_ = s.Flush()
}
