package screenshot

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"nearbyradar/internal/clock"
)

type fakeReader struct {
	mu    sync.Mutex
	obs   []Observation
	errs  []error
	calls int
}

func (f *fakeReader) Read(ctx context.Context) (Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return Observation{}, f.errs[i]
	}
	if i < len(f.obs) {
		return f.obs[i], nil
	}
	return Observation{}, errors.New("exhausted")
}

func (f *fakeReader) Calls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

type sinkRec struct {
	mu  sync.Mutex
	got []Observation
}

func (s *sinkRec) ApplyObservation(o Observation) {
	s.mu.Lock()
	s.got = append(s.got, o)
	s.mu.Unlock()
}
func (s *sinkRec) N() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.got) }

func TestNoopReader(t *testing.T) {
	_, err := NoopReader{}.Read(context.Background())
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("got %v", err)
	}
}

func TestObservationValid(t *testing.T) {
	tests := []struct {
		o    Observation
		want bool
	}{
		{Observation{}, false},
		{Observation{HasPos: true}, true},
		{Observation{HasZone: true}, true},
		{Observation{HasMap: true}, true},
	}
	for _, tc := range tests {
		if tc.o.Valid() != tc.want {
			t.Errorf("%+v", tc.o)
		}
	}
}

func TestPollerTickTable(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	tests := []struct {
		name      string
		reader    *fakeReader
		wantSink  int
		wantErrs  int
		wantTime  time.Time
		checkTime bool
	}{
		{"valid observation delivered, time stamped by clock", &fakeReader{obs: []Observation{{HasPos: true, X: 1, Y: 2}}}, 1, 0, fc.Now(), true},
		{"time kept when the reader provides it", &fakeReader{obs: []Observation{{HasPos: true, Time: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}}}, 1, 0, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"invalid observation dropped", &fakeReader{obs: []Observation{{}}}, 0, 0, time.Time{}, false},
		{"not implemented is silent", &fakeReader{errs: []error{ErrNotImplemented}}, 0, 0, time.Time{}, false},
		{"other error reported", &fakeReader{errs: []error{errors.New("boom")}}, 0, 1, time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := &sinkRec{}
			errs := 0
			p := &Poller{Reader: tc.reader, Sink: sink, Clock: fc, OnError: func(error) { errs++ }}
			p.Tick(context.Background())
			if sink.N() != tc.wantSink || errs != tc.wantErrs {
				t.Fatalf("sink=%d errs=%d", sink.N(), errs)
			}
			if tc.checkTime && !sink.got[0].Time.Equal(tc.wantTime) {
				t.Fatalf("time %v", sink.got[0].Time)
			}
		})
	}
}

func TestPollerEnabled(t *testing.T) {
	sink := &sinkRec{}
	tests := []struct {
		name string
		p    *Poller
		want bool
	}{
		{"nil reader", &Poller{Sink: sink}, false},
		{"noop reader", &Poller{Reader: NoopReader{}, Sink: sink}, false},
		{"noop pointer", &Poller{Reader: &NoopReader{}, Sink: sink}, false},
		{"no sink", &Poller{Reader: &fakeReader{}}, false},
		{"real reader", &Poller{Reader: &fakeReader{}, Sink: sink}, true},
	}
	for _, tc := range tests {
		if tc.p.Enabled() != tc.want {
			t.Errorf("%s", tc.name)
		}
	}
}

func TestPollerRunDisabledReturnsImmediately(t *testing.T) {
	p := &Poller{Reader: NoopReader{}, Sink: &sinkRec{}, Interval: time.Millisecond}
	done := make(chan struct{})
	go func() { p.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run should return at once for a noop reader")
	}
}

func TestPollerRunPollsUntilCancelled(t *testing.T) {
	obs := make([]Observation, 1000)
	for i := range obs {
		obs[i] = Observation{HasPos: true, X: float64(i)}
	}
	r := &fakeReader{obs: obs}
	sink := &sinkRec{}
	p := &Poller{Reader: r, Sink: sink, Interval: 2 * time.Millisecond, Clock: clock.Real{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for sink.N() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if sink.N() < 3 {
		t.Fatalf("only %d observations", sink.N())
	}
}

func TestPollerDefaultInterval(t *testing.T) {
	p := &Poller{}
	if p.interval() != DefaultInterval {
		t.Fatal("default interval")
	}
	p.Interval = time.Second
	if p.interval() != time.Second {
		t.Fatal("custom interval")
	}
}
