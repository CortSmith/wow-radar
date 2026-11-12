package armoryclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type response struct {
	status int
	body   []byte
}

// request is one outbound call. Credentials are Secret values so a stray print cannot show them.
type request struct {
	method               string
	url                  string
	body                 string // form body (token request), empty for GET
	bearer               Secret // access token for API calls
	basicID, basicSecret Secret // client credentials for the token request
}

// get performs one logical GET with limiter, breaker and retries. 429 and 5xx are retried (honouring
// Retry-After) and counted by the breaker; transport errors are retried; every other status is
// returned to the caller to interpret. Redirects and oversized bodies fail at once.
func (c *Client) get(ctx context.Context, rq request) (response, error) {
	var last error
	for attempt := 0; ; attempt++ {
		if !c.breaker.allow(c.now()) {
			return response{}, ErrCircuitOpen
		}
		if err := c.sleep(ctx, c.limiter.reserve(c.now())); err != nil {
			return response{}, err
		}
		res, retryAfter, err := c.once(ctx, rq)
		switch {
		case err == nil && res.status != http.StatusTooManyRequests && res.status < 500:
			c.breaker.success()
			return res, nil
		case err == nil:
			c.breaker.failure(c.now())
			last = &StatusError{Code: res.status}
		case isFatal(err) || ctx.Err() != nil:
			c.breaker.abort()
			return response{}, c.scrub(err)
		default:
			c.breaker.abort()
			last = c.scrub(err)
		}
		if attempt >= c.cfg.MaxRetries || c.cfg.MaxRetries < 0 {
			return response{}, last
		}
		if c.breaker.isOpen(c.now()) {
			return response{}, fmt.Errorf("%w: %w", ErrCircuitOpen, last)
		}
		if err := c.sleep(ctx, c.backoff(attempt, retryAfter)); err != nil {
			return response{}, err
		}
	}
}

func isFatal(err error) bool {
	return errors.Is(err, ErrRedirect) || errors.Is(err, ErrTooLarge)
}

// once sends one request and reads the capped body. The returned duration is the server's Retry-After (0 if none).
func (c *Client) once(ctx context.Context, rq request) (response, time.Duration, error) {
	var rd io.Reader
	if rq.body != "" {
		rd = strings.NewReader(rq.body)
	}
	req, err := http.NewRequestWithContext(ctx, rq.method, rq.url, rd)
	if err != nil {
		return response{}, 0, ErrBadName
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	switch {
	case rq.bearer.IsSet():
		req.Header.Set("Authorization", "Bearer "+rq.bearer.Reveal())
	case rq.basicID.IsSet():
		req.SetBasicAuth(rq.basicID.Reveal(), rq.basicSecret.Reveal())
	}
	if rq.body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return response{}, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return response{}, 0, ErrRedirect
	}
	if resp.ContentLength > c.cfg.MaxResponseBytes {
		return response{}, 0, ErrTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxResponseBytes+1))
	if err != nil {
		return response{}, 0, err
	}
	if int64(len(body)) > c.cfg.MaxResponseBytes {
		return response{}, 0, ErrTooLarge
	}
	return response{status: resp.StatusCode, body: body}, parseRetryAfter(resp.Header.Get("Retry-After"), c.now()), nil
}

// parseRetryAfter reads delta-seconds or an HTTP date; anything else is 0.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseInt(v, 10, 32); err == nil {
		return time.Duration(max(n, 0)) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0)
	}
	return 0
}

// backoff is the wait before retry number attempt+1: Retry-After when given, otherwise exponential
// with jitter; both are capped at BackoffMax.
func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, c.cfg.BackoffMax)
	}
	d := time.Duration(float64(c.cfg.BackoffMin) * math.Pow(2, float64(min(attempt, 30))))
	d = min(d, c.cfg.BackoffMax)
	return min(d+c.jitter(d/2), c.cfg.BackoffMax)
}

func randomJitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	return time.Duration(mrand.Int64N(int64(limit) + 1))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// limiter is a token bucket; reserve books one token and returns how long the caller must wait for it.
type limiter struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newLimiter(rate float64, burst int, now time.Time) *limiter {
	return &limiter{rate: rate, burst: float64(burst), tokens: float64(burst), last: now}
}

func (l *limiter) reserve(now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el := now.Sub(l.last).Seconds(); el > 0 {
		l.tokens = math.Min(l.burst, l.tokens+el*l.rate)
		l.last = now
	}
	l.tokens--
	if l.tokens >= 0 {
		return 0
	}
	return time.Duration(-l.tokens / l.rate * float64(time.Second))
}

// breaker opens after threshold consecutive 429/5xx answers, fails fast for the cool-down, then lets
// one probe through (half-open): success closes it, failure reopens it.
type breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	fails     int
	openUntil time.Time
	probing   bool
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	return &breaker{threshold: threshold, cooldown: cooldown}
}

func (b *breaker) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return true
	}
	if now.Before(b.openUntil) || b.probing {
		return false
	}
	b.probing = true // half-open: exactly one probe
	return true
}

// isOpen reports an open breaker without taking the half-open probe.
func (b *breaker) isOpen(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.openUntil.IsZero() && (now.Before(b.openUntil) || b.probing)
}

// abort releases a half-open probe whose request ended without a verdict (network error, cancel).
func (b *breaker) abort() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
}

func (b *breaker) success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails, b.openUntil, b.probing = 0, time.Time{}, false
}

func (b *breaker) failure(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails++
	if b.probing || b.fails >= b.threshold {
		b.openUntil, b.probing = now.Add(b.cooldown), false
	}
}
