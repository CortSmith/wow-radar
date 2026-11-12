package feed

import (
	"context"
	"time"
)

// Limiter is a token bucket measured in bytes. The burst is one second of traffic.
type Limiter struct {
	rate   float64 // bytes per second; <= 0 means unlimited
	tokens float64
	last   time.Time
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error
}

// NewLimiter returns a limiter for bytesPerSec (<= 0 disables it). now and sleep are injectable
// for tests; nil means the real clock.
func NewLimiter(bytesPerSec int, now func() time.Time, sleep func(context.Context, time.Duration) error) *Limiter {
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = SleepContext
	}
	l := &Limiter{rate: float64(bytesPerSec), now: now, sleep: sleep}
	l.tokens, l.last = l.rate, now()
	return l
}

// Wait accounts n bytes and sleeps as long as the rate requires. A single call may exceed the
// burst; it then just waits for the deficit.
func (l *Limiter) Wait(ctx context.Context, n int) error {
	if l.rate <= 0 {
		return nil
	}
	now := l.now()
	l.tokens = min(l.rate, l.tokens+now.Sub(l.last).Seconds()*l.rate)
	l.last = now
	l.tokens -= float64(n)
	if l.tokens >= 0 {
		return nil
	}
	wait := time.Duration(-l.tokens / l.rate * float64(time.Second))
	if err := l.sleep(ctx, wait); err != nil {
		return err
	}
	l.tokens, l.last = 0, l.now()
	return nil
}

// SleepContext sleeps for d or until ctx ends, returning ctx.Err() in the latter case.
func SleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
