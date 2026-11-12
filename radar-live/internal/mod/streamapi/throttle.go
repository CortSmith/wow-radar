package streamapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"sync"
	"time"

	"radarlive/internal/netx"
)

// Settings of the ingest limits. Every rate is per second; 0 switches that limit off. The environment
// names are RADAR_STREAM_LINE_RATE and so on.
const (
	SettingLineRate      = "stream-line-rate"
	SettingLineBurst     = "stream-line-burst"
	SettingByteRate      = "stream-byte-rate"
	SettingByteBurst     = "stream-byte-burst"
	SettingKeyLineRate   = "stream-key-line-rate"
	SettingKeyLineBurst  = "stream-key-line-burst"
	SettingKeyByteRate   = "stream-key-byte-rate"
	SettingKeyByteBurst  = "stream-key-byte-burst"
	SettingPatience      = "stream-rate-patience"
	SettingMaxPerKey     = "stream-max-per-key"
	SettingMaxStreams    = "stream-max-streams"
	SettingLineTimeout   = "stream-line-timeout"
	defaultLineRate      = 5_000
	defaultLineBurst     = 10_000
	defaultByteRate      = 4 << 20
	defaultByteBurst     = 8 << 20
	defaultKeyLineRate   = 20_000
	defaultKeyLineBurst  = 40_000
	defaultKeyByteRate   = 16 << 20
	defaultKeyByteBurst  = 32 << 20
	defaultPatience      = 30 * time.Second
	defaultMaxPerKey     = 4
	defaultMaxStreams    = 32
	defaultLineTimeout   = 30 * time.Second
	maxTrackedKeys       = 1024
	keyIDBytes           = 16
	endedRate            = "rate"
	endedSlow            = "slow"
	errCodeRate          = "rate"
	retryAfterSecondsMax = 3600
)

// keyID identifies an ingest key without keeping it: the first bytes of its SHA-256.
type keyID [keyIDBytes]byte

func keyOf(r *http.Request) keyID {
	raw, _ := netx.PresentedKey(r)
	sum := sha256.Sum256([]byte(raw))
	return keyID(sum[:keyIDBytes])
}

// bucket is a token bucket that may go into debt: reserve says how long the caller must wait.
type bucket struct {
	rate, burst, tokens float64
	last                time.Time
}

func newBucket(rate, burst int, now time.Time) *bucket {
	if rate <= 0 {
		return nil
	}
	b := float64(max(burst, rate))
	return &bucket{rate: float64(rate), burst: b, tokens: b, last: now}
}

func (b *bucket) refill(now time.Time) {
	if dt := now.Sub(b.last).Seconds(); dt > 0 {
		b.tokens = min(b.burst, b.tokens+dt*b.rate)
	}
	b.last = now
}

// wait returns how long n tokens would have to wait (0 when available) without taking them.
func (b *bucket) wait(now time.Time, n float64) time.Duration {
	if b == nil || n <= 0 {
		return 0
	}
	b.refill(now)
	if b.tokens >= n {
		return 0
	}
	return time.Duration((n - b.tokens) / b.rate * float64(time.Second))
}

func (b *bucket) take(n float64) {
	if b != nil {
		b.tokens -= n
	}
}

// limiter is a lines bucket plus a bytes bucket, charged together.
type limiter struct {
	mu          sync.Mutex
	lines, data *bucket
}

func newLimiter(lineRate, lineBurst, byteRate, byteBurst int, now time.Time) *limiter {
	return &limiter{lines: newBucket(lineRate, lineBurst, now), data: newBucket(byteRate, byteBurst, now)}
}

// peek returns the wait for the charge and whether it is within maxWait; nothing is taken.
func (l *limiter) peek(now time.Time, lines, bytes float64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return max(l.lines.wait(now, lines), l.data.wait(now, bytes))
}

func (l *limiter) take(lines, bytes float64) {
	l.mu.Lock()
	l.lines.take(lines)
	l.data.take(bytes)
	l.mu.Unlock()
}

// limits are the validated ingest limits.
type limits struct {
	lineRate, lineBurst, byteRate, byteBurst         int
	keyLineRate, keyLineBurst, keyByteRate, keyBytes int
	patience, lineTimeout                            time.Duration
	maxPerKey, maxStreams                            int
}

func (m *Module) limits() (limits, error) {
	c := m.host.Config
	l := limits{
		lineRate: c.Int(SettingLineRate), lineBurst: c.Int(SettingLineBurst),
		byteRate: c.Int(SettingByteRate), byteBurst: c.Int(SettingByteBurst),
		keyLineRate: c.Int(SettingKeyLineRate), keyLineBurst: c.Int(SettingKeyLineBurst),
		keyByteRate: c.Int(SettingKeyByteRate), keyBytes: c.Int(SettingKeyByteBurst),
		patience: c.Duration(SettingPatience), lineTimeout: c.Duration(SettingLineTimeout),
		maxPerKey: c.Int(SettingMaxPerKey), maxStreams: c.Int(SettingMaxStreams),
	}
	for name, v := range map[string]int{
		SettingLineRate: l.lineRate, SettingLineBurst: l.lineBurst, SettingByteRate: l.byteRate, SettingByteBurst: l.byteBurst,
		SettingKeyLineRate: l.keyLineRate, SettingKeyLineBurst: l.keyLineBurst, SettingKeyByteRate: l.keyByteRate,
		SettingKeyByteBurst: l.keyBytes, SettingMaxPerKey: l.maxPerKey, SettingMaxStreams: l.maxStreams,
	} {
		if v < 0 {
			return l, fmt.Errorf("streamapi: %s must not be negative", name)
		}
	}
	if l.patience < 0 || l.lineTimeout < 0 {
		return l, fmt.Errorf("streamapi: %s and %s must not be negative", SettingPatience, SettingLineTimeout)
	}
	return l, nil
}

// keyState is what the registry remembers per ingest key: its shared limiter and open count.
type keyState struct {
	lim     *limiter
	streams int
	used    time.Time
}

// sleepCtx waits d, or less when either context ends first; it reports the context error.
func sleepCtx(ctx, run context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-run.Done():
		return run.Err()
	}
}

// charge takes lines and bytes from the session's and the key's buckets. It returns how long the
// caller must wait first, or ok=false when that wait exceeds maxWait (nothing is taken then).
func (m *Module) charge(s *session, lines, bytes float64, maxWait time.Duration) (wait time.Duration, ok bool) {
	now := m.now()
	w := s.lim.peek(now, lines, bytes)
	if s.key != nil {
		w = max(w, s.key.peek(now, lines, bytes))
	}
	if maxWait > 0 && w > maxWait {
		return w, false
	}
	s.lim.take(lines, bytes)
	if s.key != nil {
		s.key.take(lines, bytes)
	}
	return w, true
}
