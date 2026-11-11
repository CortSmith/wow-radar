package app

import "os"

// appData returns %APPDATA% (or the temp dir if unset).
func appData() string {
	if d := os.Getenv("APPDATA"); d != "" {
		return d
	}
	return os.TempDir()
}
