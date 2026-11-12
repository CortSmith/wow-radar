//go:build !windows

package client

// NewNativePicker returns the typed-path fallback: only Windows has a native dialog here.
func NewNativePicker() FilePicker { return UnsupportedPicker{} }
