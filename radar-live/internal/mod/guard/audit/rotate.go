package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// rotatingFile is an append-only log file (mode 0600) that moves itself to path.1 when it would
// grow past maxBytes, shifting older generations up and deleting those beyond keep. It is pure
// Go, safe for concurrent use, and never blocks on anything but the disk.
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	f        *os.File
	size     int64
}

func openRotating(path string, maxBytes int64, keep int) (*rotatingFile, error) {
	if maxBytes < 1 {
		return nil, fmt.Errorf("audit log size limit must be positive")
	}
	if keep < 0 {
		keep = 0
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("audit log directory: %w", err)
	}
	r := &rotatingFile{path: path, maxBytes: maxBytes, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	_ = f.Chmod(0o600) // tighten a file created earlier with a wider mode (no-op on Windows)
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat audit log: %w", err)
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Write appends p, rotating first when it would not fit. A single line larger than the limit is
// still written (to a fresh file) rather than dropped.
func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	r.f = nil
	if r.keep == 0 {
		_ = os.Remove(r.path)
	} else {
		_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
		for i := r.keep - 1; i >= 1; i-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
		}
		if err := os.Rename(r.path, r.path+".1"); err != nil {
			_ = r.open() // keep logging into the old file rather than losing events
			return err
		}
	}
	return r.open()
}

// Close closes the file.
func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
