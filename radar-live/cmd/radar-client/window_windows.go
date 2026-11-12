//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2"
)

const (
	windowWidth         = 900
	windowHeight        = 640
	messageBoxIconError = 0x10
)

// runWindow opens a native WebView2 window showing url and blocks until it is closed or ctx ends.
// It returns an error when WebView2 cannot start; the caller then uses the default browser.
func runWindow(ctx context.Context, url, title string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("WebView2 failed to start")
		}
	}()
	runtime.LockOSThread() // the window and its message loop must stay on one OS thread
	defer runtime.UnlockOSThread()

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  webviewDataPath(),
		WindowOptions: webview2.WindowOptions{
			Title: title, Width: windowWidth, Height: windowHeight, Center: true,
		},
	})
	if w == nil {
		return errors.New("WebView2 runtime not available")
	}
	defer w.Destroy()
	w.SetSize(windowWidth, windowHeight, webview2.HintNone) // user-resizable
	w.Navigate(url)

	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			w.Dispatch(w.Terminate)
		case <-stopped:
		}
	}()
	w.Run()
	close(stopped)
	return nil
}

// webviewDataPath keeps the embedded browser's cache outside the program folder.
func webviewDataPath() string {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "RadarClient", "webview2")
	}
	return ""
}

func openBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// reportFatal shows a message box (the windowed build has no console to print to).
func reportFatal(msg string) {
	os.Stderr.WriteString(msg + "\n")
	user32 := syscall.NewLazyDLL("user32.dll")
	text, errText := syscall.UTF16PtrFromString(msg)
	caption, errCaption := syscall.UTF16PtrFromString(windowTitle)
	if errText != nil || errCaption != nil {
		return
	}
	_, _, _ = user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(caption)), messageBoxIconError)
}
