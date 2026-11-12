//go:build !linux && !darwin && !windows

package sqlstore

import "errors"

// diskFree is not implemented on this platform; the free-space guard then stays off.
func diskFree(string) (uint64, error) {
	return 0, errors.New("free-space probe not supported on this platform")
}

// lockFile is not implemented on this platform; the single-server lock is then not enforced.
func lockFile(string) (func() error, error) { return func() error { return nil }, nil }
