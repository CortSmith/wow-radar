package clock

import (
	"testing"
	"time"
)

func TestFake(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := NewFake(start)
	if !f.Now().Equal(start) {
		t.Fatal("start")
	}
	f.Advance(time.Minute)
	if !f.Now().Equal(start.Add(time.Minute)) {
		t.Fatal("advance")
	}
	f.Set(start)
	if !f.Now().Equal(start) {
		t.Fatal("set")
	}
}

func TestReal(t *testing.T) {
	var c Clock = Real{}
	if d := time.Since(c.Now()); d < 0 || d > time.Minute {
		t.Fatal(d)
	}
}
