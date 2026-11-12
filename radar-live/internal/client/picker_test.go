package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

func TestUnsupportedPickerSaysSo(t *testing.T) {
	_, err := UnsupportedPicker{}.Pick(context.Background(), PickOptions{})
	if !errors.Is(err, ErrPickerUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestNativePickerExists(t *testing.T) {
	if NewNativePicker() == nil {
		t.Fatal("no picker")
	}
}

func TestFilterIsDoubleNulTerminatedPairs(t *testing.T) {
	got := utf16.Decode(buildFilter([]filterPair{{"Combat logs", "WoWCombatLog*.txt"}, {"All files", "*.*"}}))
	want := "Combat logs\x00WoWCombatLog*.txt\x00All files\x00*.*\x00\x00"
	if string(got) != want {
		t.Fatalf("filter = %q, want %q", string(got), want)
	}
}

func TestInitialDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		dir:                           dir,
		file:                          dir,
		filepath.Join(dir, "nope.x"):  dir,
		filepath.Join(dir, "no", "x"): "",
		"":                            "",
	}
	for in, want := range tests {
		if got := initialDir(in); got != want {
			t.Errorf("initialDir(%q) = %q, want %q", in, got, want)
		}
	}
}
