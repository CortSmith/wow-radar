//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
)

// runWindow reports that this platform has no native window; the caller uses the default browser.
func runWindow(context.Context, string, string) error {
	return errors.New("native window is only available on Windows")
}

func openBrowser(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.Command(opener, url).Start()
}

func reportFatal(msg string) { os.Stderr.WriteString(msg + "\n") }
