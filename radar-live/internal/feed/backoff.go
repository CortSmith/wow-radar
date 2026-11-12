package feed

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	limitedWaitMin       = 30 * time.Second // floor for a 429, whatever Retry-After says
	limitedWaitMax       = 15 * time.Minute // cap for a 429
	busyWaitMin          = 10 * time.Second // first wait after a 409 (source already in use)
	busyWaitMax          = 2 * time.Minute  // cap for a 409
	jitterFraction       = 0.2              // waits are stretched by 0..20% (network errors: -20%..+20%)
	maxRetryAfterSeconds = 366 * 24 * 3600  // larger values cannot be a delay and would overflow a Duration
)

// ErrAuthRejected is returned by Streamer.Run when ingest answers 401 or 403. Retrying cannot help
// (and would trip the server's abuse lockout), so the streamer stops.
var ErrAuthRejected = errors.New("the server rejected the ingest key")

// AuthRejectedError is the error Run returns for a refused key. Its text is meant for the user
// and never contains the key; errors.Is(err, ErrAuthRejected) matches it.
type AuthRejectedError struct{ Status int }

func (e *AuthRejectedError) Error() string { return AuthRejectedMessage(e.Status) }

// Unwrap lets errors.Is match ErrAuthRejected.
func (e *AuthRejectedError) Unwrap() error { return ErrAuthRejected }

// AuthRejectedMessage is the terminal message for a 401 or 403 reply.
func AuthRejectedMessage(status int) string {
	const stopped = " The upload stopped; fix the key and click Start again."
	if status == http.StatusForbidden {
		return "The server refused this key for uploading (403)." + stopped
	}
	return "The server rejected the ingest key (401)." + stopped
}

// CleanKey removes the whitespace and line breaks a pasted or file-stored key often carries.
func CleanKey(key string) string { return strings.TrimSpace(key) }

// ParseRetryAfter reads a Retry-After header value: whole seconds or an HTTP date. A date in the
// past gives 0. ok is false for an empty, negative, fractional or unparsable value.
func ParseRetryAfter(value string, now time.Time) (wait time.Duration, ok bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		if n < 0 || n > maxRetryAfterSeconds {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}

// backoff decides how long the streamer waits after a failed connection. Network errors and other
// failures use a fast exponential schedule; 429 and 409 use slow schedules of their own so that a
// busy or locked-out server is not hammered. random returns a value in [0, 1).
type backoff struct {
	min, max time.Duration
	random   func() float64
	network  time.Duration // next wait after a network error or other failure
	limited  time.Duration // next base wait after a 429 without a usable Retry-After
	busy     time.Duration // next wait after a 409
}

func newBackoff(minWait, maxWait time.Duration, random func() float64) *backoff {
	b := &backoff{min: minWait, max: maxWait, random: random}
	b.reset()
	return b
}

// reset starts every schedule over (after a successful upload).
func (b *backoff) reset() {
	b.network, b.limited, b.busy = b.min, limitedWaitMin, busyWaitMin
}

// next returns the wait before the next attempt after a reply with the given status (0 = none)
// and Retry-After header value, and advances the schedule.
func (b *backoff) next(status int, retryAfter string, now time.Time) time.Duration {
	switch status {
	case http.StatusTooManyRequests:
		return b.nextLimited(retryAfter, now)
	case http.StatusConflict:
		wait := b.busy
		b.busy = min(b.busy*2, busyWaitMax)
		return b.stretch(wait)
	}
	wait := b.network
	b.network = min(b.network*2, b.max)
	return b.spread(wait)
}

func (b *backoff) nextLimited(retryAfter string, now time.Time) time.Duration {
	wait := b.limited
	b.limited = min(b.limited*2, limitedWaitMax)
	if hint, ok := ParseRetryAfter(retryAfter, now); ok {
		wait = max(wait, hint)
	}
	return b.stretch(min(wait, limitedWaitMax))
}

// stretch adds 0..20% so several clients do not retry in step.
func (b *backoff) stretch(d time.Duration) time.Duration {
	return d + time.Duration(float64(d)*jitterFraction*b.random())
}

// spread moves d by -20%..+20%.
func (b *backoff) spread(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (1 + jitterFraction*(2*b.random()-1)))
}
