package domain

import (
	"math"
	"reflect"
	"testing"
)

func fix(t int64, mapID int, x, y float64) Fix {
	return Fix{T: t, MapID: mapID, X: x, Y: y, Kind: FixExact}
}

func TestCleanFixes(t *testing.T) {
	in := []Fix{
		fix(3000, 1, 10.04, 20.06),
		{T: 1000, MapID: 1, X: 1, Y: 2, Kind: "weird"},
		fix(2000, 1, math.NaN(), 0),
		fix(2500, 1, 0, math.Inf(1)),
		{T: -5, MapID: 1},
		{T: 4000, MapID: 1, X: 5, Y: 5, Kind: FixApprox},
	}
	want := []Fix{
		{T: 1000, MapID: 1, X: 1, Y: 2, Kind: FixExact},
		{T: 3000, MapID: 1, X: 10, Y: 20.1, Kind: FixExact},
		{T: 4000, MapID: 1, X: 5, Y: 5, Kind: FixApprox},
	}
	if got := CleanFixes(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("CleanFixes = %+v\nwant %+v", got, want)
	}
}

func TestAppendFixes(t *testing.T) {
	tests := []struct {
		name      string
		trail     []Fix
		incoming  []Fix
		wantTimes []int64
	}{
		{"empty trail takes all", nil, []Fix{fix(1000, 1, 0, 0), fix(9000, 1, 50, 0)}, []int64{1000, 9000}},
		{"watermark drops replays", []Fix{fix(5000, 1, 0, 0)}, []Fix{fix(5000, 1, 99, 99), fix(4000, 1, 99, 99), fix(9000, 1, 99, 99)}, []int64{5000, 9000}},
		{"near and soon on same map is redundant", []Fix{fix(1000, 1, 0, 0)}, []Fix{fix(2000, 1, 1, 1)}, []int64{1000}},
		{"near but late is kept", []Fix{fix(1000, 1, 0, 0)}, []Fix{fix(6000, 1, 1, 1)}, []int64{1000, 6000}},
		{"near and soon on another map is kept", []Fix{fix(1000, 1, 0, 0)}, []Fix{fix(2000, 2, 0, 0)}, []int64{1000, 2000}},
		{"soon but far is kept", []Fix{fix(1000, 1, 0, 0)}, []Fix{fix(2000, 1, 10, 0)}, []int64{1000, 2000}},
	}
	for _, tt := range tests {
		got := AppendFixes(tt.trail, tt.incoming)
		var times []int64
		for _, f := range got {
			times = append(times, f.T)
		}
		if !reflect.DeepEqual(times, tt.wantTimes) {
			t.Errorf("%s: times = %v, want %v", tt.name, times, tt.wantTimes)
		}
	}
}

func TestAppendFixesCapsTrailDroppingOldest(t *testing.T) {
	var trail []Fix
	for i := 0; i < TrailCapFixes; i++ {
		trail = append(trail, fix(int64(i)*10000, 1, float64(i)*10, 0))
	}
	extra := []Fix{fix(int64(TrailCapFixes)*10000, 1, 1e6, 0)}
	got := AppendFixes(trail, extra)
	if len(got) != TrailCapFixes {
		t.Fatalf("len = %d, want %d", len(got), TrailCapFixes)
	}
	if got[0].T != 10000 || got[len(got)-1].T != extra[0].T {
		t.Fatalf("oldest not dropped / newest not kept: first=%d last=%d", got[0].T, got[len(got)-1].T)
	}
}

func TestFixesForMap(t *testing.T) {
	trail := []Fix{fix(1, 1, 0, 0), fix(2, 2, 0, 0), fix(3, 1, 0, 0)}
	mapID := 1
	if got := FixesForMap(trail, &mapID); len(got) != 2 {
		t.Fatalf("filtered = %v", got)
	}
	if got := FixesForMap(trail, nil); len(got) != 3 {
		t.Fatalf("unfiltered = %v", got)
	}
}

func TestFixesAfter(t *testing.T) {
	trail := []Fix{fix(1, 1, 0, 0), fix(5, 1, 0, 0)}
	if got := FixesAfter(trail, 1); len(got) != 1 || got[0].T != 5 {
		t.Fatalf("FixesAfter = %v", got)
	}
	if got := FixesAfter(trail, 0); len(got) != 2 {
		t.Fatalf("FixesAfter(0) = %v", got)
	}
}
