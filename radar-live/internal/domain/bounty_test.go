package domain

import "testing"

func TestRankBounty(t *testing.T) {
	contacts := []Contact{
		{GUID: "Player-b", Name: "B", KB: 2, LastSeen: 10},
		{GUID: "Player-a", Name: "A", KB: 2, LastSeen: 10},
		{GUID: "Player-c", Name: "C", KB: 5, LastSeen: 1},
		{GUID: "Player-d", Name: "D", KB: 2, Assists: 3, LastSeen: 1},
		{GUID: "Player-e", Name: "E", KB: 2, LastSeen: 99},
		{GUID: "Player-f", Name: "F"},
		{GUID: "Player-g", Name: "G", Assists: 1},
	}
	got := RankBounty(contacts)
	wantOrder := []string{"Player-c", "Player-d", "Player-e", "Player-a", "Player-b", "Player-g"}
	if len(got) != len(wantOrder) {
		t.Fatalf("rows = %d, want %d: %+v", len(got), len(wantOrder), got)
	}
	for i, g := range wantOrder {
		if got[i].GUID != g || got[i].Rank != i+1 {
			t.Errorf("row %d = %+v, want guid %s rank %d", i, got[i], g, i+1)
		}
	}
	if got[0].Score != 50 || got[1].Score != 20 {
		t.Errorf("score = %d, %d; want 50, 20 (assists are worth 0)", got[0].Score, got[1].Score)
	}
}

func TestRankBountyEmpty(t *testing.T) {
	if got := RankBounty(nil); got == nil || len(got) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v", got)
	}
}
