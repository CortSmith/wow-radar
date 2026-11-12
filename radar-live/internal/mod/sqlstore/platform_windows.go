//go:build windows

package sqlstore

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// diskFree returns the bytes available to the caller on the volume holding dir.
func diskFree(dir string) (uint64, error) {
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	r, _, callErr := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&free)))
	if r == 0 {
		return 0, fmt.Errorf("GetDiskFreeSpaceEx %s: %w", dir, callErr)
	}
	return avail, nil
}

// lockFile opens path with sharing disabled, so a second open (any process) fails while it is held.
func lockFile(path string) (func() error, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33)) { // sharing or lock violation
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("sqlstore: lock %s: %w", path, err)
	}
	return func() error { return syscall.CloseHandle(h) }, nil
}
