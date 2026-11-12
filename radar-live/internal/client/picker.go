package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"unicode/utf16"
)

// Errors a FilePicker may return.
var (
	// ErrPickerUnsupported means this platform has no native file dialog; the user types the path.
	ErrPickerUnsupported = errors.New("no file dialog on this platform")
	// ErrPickCancelled means the user closed the dialog without choosing a file.
	ErrPickCancelled = errors.New("file selection cancelled")
)

// PickOptions describe the dialog to show.
type PickOptions struct {
	Title       string
	InitialPath string // a file or folder the dialog starts in
}

// FilePicker lets the user choose a log file with a native dialog. It exists as an interface so the
// UI server can be tested with a fake.
type FilePicker interface {
	// Pick shows the dialog and returns the chosen path, ErrPickCancelled or ErrPickerUnsupported.
	Pick(ctx context.Context, opts PickOptions) (string, error)
}

// UnsupportedPicker is the fallback where no native dialog exists.
type UnsupportedPicker struct{}

// Pick always fails with ErrPickerUnsupported.
func (UnsupportedPicker) Pick(context.Context, PickOptions) (string, error) {
	return "", ErrPickerUnsupported
}

// filterPair is one "description, pattern" entry of a file dialog's type list.
type filterPair struct{ description, pattern string }

// buildFilter encodes pairs the way the Windows common dialogs want them: UTF-16, every string
// ends with NUL and the list ends with an extra NUL.
func buildFilter(pairs []filterPair) []uint16 {
	var out []uint16
	for _, p := range pairs {
		out = append(out, utf16.Encode([]rune(p.description))...)
		out = append(out, 0)
		out = append(out, utf16.Encode([]rune(p.pattern))...)
		out = append(out, 0)
	}
	return append(out, 0)
}

// initialDir is the folder a dialog should open in for path: the path itself if it is a folder, the
// folder holding it if it is (or could be) a file, or "" when neither exists.
func initialDir(path string) string {
	if path == "" {
		return ""
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	dir := filepath.Dir(path)
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}
