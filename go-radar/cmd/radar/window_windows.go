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

// runWindow opens a native, resizable WebView2 window showing url and blocks until it is
// closed or ctx is cancelled. It returns an error if WebView2 cannot be initialised
// (the caller then falls back to the default browser).
func runWindow(ctx context.Context, url, title string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("webview2 panic during init")
		}
	}()
	runtime.LockOSThread() // the window and its message loop must stay on one OS thread
	defer runtime.UnlockOSThread()

	data := ""
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		data = filepath.Join(d, "NearbyRadar", "webview2")
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  data,
		WindowOptions: webview2.WindowOptions{
			Title: title, Width: 1180, Height: 780, Center: true,
		},
	})
	if w == nil {
		return errors.New("WebView2 runtime not available")
	}
	defer w.Destroy()
	w.SetSize(1180, 780, webview2.HintNone) // HintNone: user-resizable
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

func openBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// reportFatal shows a message box (the GUI build has no console to print to).
func reportFatal(msg string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	mb := user32.NewProc("MessageBoxW")
	t, _ := syscall.UTF16PtrFromString("Nearby Radar")
	m, _ := syscall.UTF16PtrFromString(msg)
	_, _, _ = mb.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
	os.Stderr.WriteString(msg + "\n")
}
