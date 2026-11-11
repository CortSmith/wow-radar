// Package clock provides an injectable wall clock.
package clock

import (
	"sync"
	"time"
)

// Clock supplies the current wall-clock time.
type Clock interface{ Now() time.Time }

// Real is the system clock.
type Real struct{}

// Now returns time.Now().
func (Real) Now() time.Time { return time.Now() }

// Fake is a manually advanced clock for tests.
type Fake struct {
	mu sync.Mutex
	t  time.Time
}

// NewFake returns a Fake set to t.
func NewFake(t time.Time) *Fake { return &Fake{t: t} }

// Now returns the fake time.
func (f *Fake) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }

// Advance moves the fake time forward by d.
func (f *Fake) Advance(d time.Duration) { f.mu.Lock(); f.t = f.t.Add(d); f.mu.Unlock() }

// Set sets the fake time.
func (f *Fake) Set(t time.Time) { f.mu.Lock(); f.t = t; f.mu.Unlock() }
