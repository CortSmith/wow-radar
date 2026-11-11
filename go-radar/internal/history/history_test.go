package history

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func near(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestRecordAggregates(t *testing.T) {
	s := New("")
	s.Record(Sample{GUID: "Player-1-A", Name: "A-R", Time: t0, AsSource: true, HasHP: true, HP: 50, MaxHP: 100, Damage: 10, HasDamage: true, HasPos: true, X: 1, Y: 2, MapID: 7, Zone: "Z"})
	s.Record(Sample{GUID: "Player-1-A", Time: t0.Add(time.Second), AsSource: true, HasHP: true, HP: 70, MaxHP: 120, Damage: 30, HasDamage: true})
	s.Record(Sample{GUID: "Player-1-A", Time: t0.Add(2 * time.Second), AsSource: true}) // non-damage event as source
	s.Record(Sample{GUID: "Player-1-A", Time: t0.Add(3 * time.Second)})                 // as target only
	e, ok := s.Get("Player-1-A")
	if !ok {
		t.Fatal("missing")
	}
	if e.Events != 4 || e.SrcEvents != 3 || e.DamageHits != 2 {
		t.Fatalf("counts %+v", e)
	}
	near(t, e.DamageTotal, 40)
	near(t, e.AvgDamagePerHit, 20)
	near(t, e.AvgDamagePerEvent, 40.0/3)
	if e.HPSamples != 2 {
		t.Fatalf("hp samples %d", e.HPSamples)
	}
	near(t, e.AvgHP, 60)
	near(t, e.AvgMaxHP, 110)
	near(t, e.MaxHP, 120)
	near(t, e.MaxObservedHP, 70)
	if e.Name != "A-R" {
		t.Fatalf("name %q", e.Name)
	}
	if !e.FirstSeen.Equal(t0) || !e.LastSeen.Equal(t0.Add(3*time.Second)) {
		t.Fatalf("times %v %v", e.FirstSeen, e.LastSeen)
	}
	if e.LastX != 1 || e.LastY != 2 || e.LastMap != 7 || e.LastZone != "Z" || !e.HasPos {
		t.Fatalf("pos %+v", e)
	}
}

func TestRecordEmptyGUIDIgnored(t *testing.T) {
	s := New("")
	s.Record(Sample{Time: t0})
	if len(s.Snapshot()) != 0 {
		t.Fatal("should ignore")
	}
}

func TestOutOfOrderSamplesMerge(t *testing.T) {
	s := New("")
	s.Record(Sample{GUID: "G", Time: t0.Add(10 * time.Second), HasPos: true, X: 10, Zone: "new"})
	s.Record(Sample{GUID: "G", Time: t0, HasPos: true, X: 1, Zone: "old"}) // older, must not override position
	e, _ := s.Get("G")
	if !e.FirstSeen.Equal(t0) || !e.LastSeen.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("times %+v", e)
	}
	if e.LastX != 10 || e.LastZone != "new" {
		t.Fatalf("position overridden by older sample: %+v", e)
	}
}

func TestSaveLoadRoundTripAndMergeOnReturn(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "history.json")
	s := New(p)
	s.Record(Sample{GUID: "G", Name: "N", Time: t0, AsSource: true, Damage: 5, HasDamage: true})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".tmp"); err == nil {
		t.Fatal("temp file left behind")
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	// target seen again later: history merges rather than resets
	s2.Record(Sample{GUID: "G", Time: t0.Add(time.Hour), AsSource: true, Damage: 15, HasDamage: true})
	e, _ := s2.Get("G")
	if e.DamageHits != 2 || e.AvgDamagePerHit != 10 || e.Name != "N" {
		t.Fatalf("merge: %+v", e)
	}
}

func TestOpenMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "none.json"))
	if err != nil || s == nil {
		t.Fatalf("missing file: %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(bad)
	if err == nil {
		t.Fatal("expected corruption error")
	}
	if s == nil || len(s.Snapshot()) != 0 {
		t.Fatal("must return a usable empty store")
	}
	if _, err := os.Stat(bad + ".corrupt"); err != nil {
		t.Fatalf("corrupt file should be preserved: %v", err)
	}
}

func TestFlushOnlyWhenDirty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "h.json")
	s := New(p)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("clean store must not write")
	}
	s.Record(Sample{GUID: "G", Time: t0})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("dirty store must write")
	}
	if New("").Flush() != nil || New("").Save() != nil {
		t.Fatal("pathless store is memory only")
	}
}

func TestSaveError(t *testing.T) {
	dir := t.TempDir()
	// parent "directory" is a file -> MkdirAll fails
	f := filepath.Join(dir, "file")
	_ = os.WriteFile(f, nil, 0o600)
	s := New(filepath.Join(f, "h.json"))
	s.Record(Sample{GUID: "G", Time: t0})
	if err := s.Save(); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunFlushesPeriodicallyAndOnCancel(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "h.json")
	s := New(p)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx, 10*time.Millisecond); close(done) }()
	s.Record(Sample{GUID: "G", Time: t0})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(p); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never flushed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Record(Sample{GUID: "H", Time: t0})
	cancel()
	<-done
	s2, _ := Open(p)
	if _, ok := s2.Get("H"); !ok {
		t.Fatal("final flush missing")
	}
}

func TestSnapshotSortedAndPrune(t *testing.T) {
	s := New("")
	s.Record(Sample{GUID: "old", Time: t0})
	s.Record(Sample{GUID: "new", Time: t0.Add(time.Hour)})
	got := s.Snapshot()
	if len(got) != 2 || got[0].GUID != "new" {
		t.Fatalf("order %+v", got)
	}
	if _, ok := s.Get("zzz"); ok {
		t.Fatal("unexpected")
	}
}

func TestLen(t *testing.T) {
	s := New("")
	if s.Len() != 0 {
		t.Fatal("empty")
	}
	s.Record(Sample{GUID: "Player-1-A", Time: time.Unix(1, 0)})
	s.Record(Sample{GUID: "Player-1-A", Time: time.Unix(2, 0)})
	s.Record(Sample{GUID: "Player-1-B", Time: time.Unix(3, 0)})
	if s.Len() != 2 {
		t.Fatalf("%d", s.Len())
	}
}
