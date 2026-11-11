// Command radar is the Nearby Radar desktop application.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"nearbyradar/internal/app"
)

func main() { os.Exit(run()) }

func run() int {
	exe, err := os.Executable()
	exeDir := "."
	if err == nil {
		exeDir = filepath.Dir(exe)
	}
	cfg, err := app.ParseFlags(os.Args[1:], os.Getenv, exeDir)
	if err != nil {
		reportFatal("Nearby Radar: " + err.Error())
		return 2
	}

	var out io.Writer = io.Discard
	if cfg.Debug {
		out = os.Stderr
	}
	if cfg.LogFile != "" {
		if f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			defer f.Close()
			out = io.MultiWriter(out, f)
		}
	}
	lg := log.New(out, "", log.Ltime|log.Lmicroseconds)
	logf := func(f string, a ...any) { lg.Printf(f, a...) }

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	a, err := app.New(cfg, nil, logf)
	if err != nil {
		reportFatal("Nearby Radar: " + err.Error())
		return 1
	}
	url, err := a.Start(ctx)
	if err != nil {
		reportFatal("Nearby Radar: cannot start: " + err.Error())
		return 1
	}
	if cfg.Debug {
		fmt.Println("Nearby Radar UI: " + url) // console build only
	}

	if cfg.NoUI {
		<-ctx.Done()
		a.Wait()
		return 0
	}
	usedWindow := false
	if !cfg.Browser {
		if err := runWindow(ctx, url, "Nearby Radar"); err != nil {
			logf("native window unavailable (%v); falling back to the default browser", err)
		} else {
			usedWindow = true
		}
	}
	if usedWindow {
		cancel() // window closed: shut everything down
	} else {
		if err := openBrowser(url); err != nil {
			logf("could not open the browser: %v (open %s manually)", err, url)
		}
		if cfg.KeepAlive || cfg.Debug {
			<-ctx.Done()
		} else {
			a.WaitForClients(ctx, 30*time.Second) // closing the tab ends the process
		}
		cancel()
	}
	a.Wait()
	return 0
}
