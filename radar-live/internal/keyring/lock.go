package keyring

import (
	"fmt"
	"os"
	"sync"
)

var procLocks sync.Map // lock path -> *sync.Mutex, serialises goroutines in this process

// Lock takes an exclusive advisory lock on path+".lock" (a sidecar file, so the locked file can be
// replaced by rename) and returns the unlock function. It blocks until the lock is free.
func Lock(path string) (unlock func(), err error) {
	lp := path + ".lock"
	m, _ := procLocks.LoadOrStore(lp, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		mu.Unlock()
		return nil, fmt.Errorf("lock %s: %w", lp, err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		mu.Unlock()
		return nil, fmt.Errorf("lock %s: %w", lp, err)
	}
	return func() {
		unlockFile(f)
		f.Close()
		mu.Unlock()
	}, nil
}
