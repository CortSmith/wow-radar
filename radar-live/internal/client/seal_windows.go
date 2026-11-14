//go:build windows

package client

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sealBytes seals plain with DPAPI for the current Windows user.
func sealBytes(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("empty key")
	}
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// openBytes reverses sealBytes; it fails for another user or computer.
func openBytes(sealed []byte) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, errors.New("empty sealed key")
	}
	in := windows.DataBlob{Size: uint32(len(sealed)), Data: &sealed[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
