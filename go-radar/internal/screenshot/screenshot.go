// Package screenshot is the CONNECTOR for a future screenshot/OCR based position reader.
//
// NOTHING IN THIS PACKAGE CAPTURES THE SCREEN OR PERFORMS OCR. It only defines the
// seam: an Observation value, a Reader interface, a Noop implementation and a Poller that
// periodically asks a Reader for an Observation and hands it to a Sink (the tracker).
//
// How a future Reader should work (not implemented here):
//
//  1. Capture the World of Warcraft game window (e.g. Windows.Graphics.Capture / PrintWindow).
//  2. Crop and blank everything except the two small text regions that matter: the minimap
//     coordinate readout ("x, y") and the zone name text beside the minimap.
//  3. Run a tiny digit/letter recogniser over those two regions only.
//  4. Return an Observation with the Valid flags set for the parts it actually recognised
//     (HasZone, HasPos, HasMap). Coordinates are map percentages the reader converts to
//     the same world-coordinate system the combat log uses, or leaves with HasPos false.
//
// The tracker treats a valid Observation as an event for the player ("me").
package screenshot

import (
	"context"
	"errors"
	"time"

	"nearbyradar/internal/clock"
)

// ErrNotImplemented is returned by NoopReader.
var ErrNotImplemented = errors.New("screenshot reader not implemented")

// DefaultInterval is the poll period used when Poller.Interval is zero.
const DefaultInterval = 2 * time.Second

// Observation is what a screenshot reader saw at one instant.
type Observation struct {
	Time  time.Time // UTC; the Poller fills it from its clock when zero
	Zone  string
	MapID int
	X, Y  float64

	HasZone bool // Zone is valid
	HasMap  bool // MapID is valid
	HasPos  bool // X, Y are valid
}

// Valid reports whether the observation carries any usable information.
func (o Observation) Valid() bool { return o.HasZone || o.HasMap || o.HasPos }

// Reader produces an Observation from the screen (or anything else).
type Reader interface {
	Read(ctx context.Context) (Observation, error)
}

// NoopReader is the default Reader: it always returns ErrNotImplemented.
type NoopReader struct{}

// Read implements Reader.
func (NoopReader) Read(context.Context) (Observation, error) {
	return Observation{}, ErrNotImplemented
}

// Sink receives observations; the tracker implements it.
type Sink interface {
	ApplyObservation(Observation)
}

// Poller calls Reader every Interval and feeds valid observations into Sink.
// It is disabled (Run returns immediately) while the Reader is a NoopReader.
type Poller struct {
	Reader   Reader
	Sink     Sink
	Interval time.Duration // DefaultInterval when zero
	Clock    clock.Clock   // clock.Real{} when nil
	OnError  func(error)   // optional; ErrNotImplemented is never reported
}

func (p *Poller) interval() time.Duration {
	if p.Interval <= 0 {
		return DefaultInterval
	}
	return p.Interval
}

// Enabled reports whether the poller has a real reader and a sink.
func (p *Poller) Enabled() bool {
	if p.Reader == nil || p.Sink == nil {
		return false
	}
	switch p.Reader.(type) {
	case NoopReader, *NoopReader:
		return false
	}
	return true
}

// Tick performs one poll.
func (p *Poller) Tick(ctx context.Context) {
	o, err := p.Reader.Read(ctx)
	if err != nil {
		if !errors.Is(err, ErrNotImplemented) && p.OnError != nil {
			p.OnError(err)
		}
		return
	}
	if !o.Valid() {
		return
	}
	if o.Time.IsZero() {
		c := p.Clock
		if c == nil {
			c = clock.Real{}
		}
		o.Time = c.Now().UTC()
	}
	p.Sink.ApplyObservation(o)
}

// Run polls until ctx is cancelled. It returns immediately when the poller is disabled.
func (p *Poller) Run(ctx context.Context) {
	if !p.Enabled() {
		return
	}
	tk := time.NewTicker(p.interval())
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			p.Tick(ctx)
		}
	}
}
