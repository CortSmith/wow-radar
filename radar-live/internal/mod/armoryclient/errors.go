package armoryclient

import (
	"errors"
	"fmt"
)

// Typed errors. None of them ever contains the client id, client secret or an access token.
var (
	// ErrAuth means the armory refused the credentials (token endpoint 400/401) or still answered 401
	// after the access token was refreshed once. It is fatal for a run.
	ErrAuth = errors.New("armoryclient: the armory rejected the client credentials")
	// ErrCircuitOpen means too many consecutive 429/5xx answers; calls fail fast until the cool-down ends.
	ErrCircuitOpen = errors.New("armoryclient: circuit breaker is open")
	// ErrRedirect means the armory answered with a redirect, which is never followed.
	ErrRedirect = errors.New("armoryclient: redirects are not followed")
	// ErrTooLarge means a response exceeded the size cap.
	ErrTooLarge = errors.New("armoryclient: response too large")
	// ErrBadResponse means the body was not the JSON the mapper expects.
	ErrBadResponse = errors.New("armoryclient: malformed response")
	// ErrBadName means a realm, name or guild failed validation and no request was made.
	ErrBadName = errors.New("armoryclient: invalid realm, name or guild")
	// ErrConfig means the configuration is unusable.
	ErrConfig = errors.New("armoryclient: invalid configuration")
)

// StatusError is an unexpected HTTP status (after retries, for 429/5xx).
type StatusError struct{ Code int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("armoryclient: unexpected HTTP status %d", e.Code)
}

// scrubbed wraps an error whose text had secrets removed while keeping the cause for errors.Is.
type scrubbed struct {
	msg string
	err error
}

func (e *scrubbed) Error() string { return e.msg }
func (e *scrubbed) Unwrap() error { return e.err }
