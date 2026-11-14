//go:build windows

package keyring

import "os"

func inheritFileAttrs(tmp *os.File, target string) {}
