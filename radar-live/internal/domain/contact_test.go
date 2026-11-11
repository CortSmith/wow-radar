package domain

import "testing"

func TestRound1(t *testing.T) {
	tests := []struct{ in, want float64 }{{1.04, 1.0}, {1.05, 1.1}, {-2.26, -2.3}, {0, 0}, {100, 100}}
	for _, tt := range tests {
		if got := Round1(tt.in); got != tt.want {
			t.Errorf("Round1(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestContactHasPosition(t *testing.T) {
	if (Contact{}).HasPosition() {
		t.Error("zero contact has no position")
	}
	if !(Contact{PosTime: 5}).HasPosition() {
		t.Error("contact with posTime has a position")
	}
}

func TestContactMatchesQuery(t *testing.T) {
	c := Contact{Name: "Thrallson", Raw: "Thrallson-Realm", PastNames: []string{"Oldname"}}
	tests := map[string]bool{"": true, "thrall": true, "REALM": true, "oldn": true, "zzz": false}
	for q, want := range tests {
		if got := c.MatchesQuery(q); got != want {
			t.Errorf("MatchesQuery(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestContactCloneIsIndependent(t *testing.T) {
	c := Contact{GUID: "Player-1", PastNames: []string{"a"}}
	d := c.Clone()
	d.PastNames[0] = "b"
	if c.PastNames[0] != "a" {
		t.Fatal("Clone shares the PastNames slice")
	}
	if (Contact{}).Clone().PastNames != nil {
		t.Fatal("Clone of nil slice must stay nil")
	}
}

func TestContactEqual(t *testing.T) {
	a := Contact{GUID: "Player-1", PastNames: []string{"a"}, Events: 3}
	b := a.Clone()
	if !a.Equal(b) {
		t.Fatal("clones must be equal")
	}
	b.PastNames = []string{"z"}
	if a.Equal(b) {
		t.Fatal("different past names must differ")
	}
	b = a.Clone()
	b.Events++
	if a.Equal(b) {
		t.Fatal("different counters must differ")
	}
}
