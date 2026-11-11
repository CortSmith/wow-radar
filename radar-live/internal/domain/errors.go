package domain

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalid marks input that violates the data contract; callers map it to HTTP 400.
	ErrInvalid = errors.New("invalid input")
	// ErrNotFound marks a lookup that matched nothing; callers map it to HTTP 404.
	ErrNotFound = errors.New("not found")
)

// invalidf builds an error wrapping ErrInvalid with a formatted explanation.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}
