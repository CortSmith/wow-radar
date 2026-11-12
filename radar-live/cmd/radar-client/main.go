// Command radar-client is the desktop app that uploads a World of Warcraft combat log to radar-ingest
// while the game writes it. It serves a small web UI on 127.0.0.1 and shows it in a native WebView2
// window (Windows) or the default browser. The ingest key is read from RADAR_CLIENT_INGEST_KEY or
// typed into the UI; it is never written to disk.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"radarlive/internal/client"
	"radarlive/internal/core"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	windowTitle = "Radar Client"
	maxPort     = 65535
	exitFailure = 1
)

// config is the parsed command line. Nothing secret can be given on the command line.
type config struct {
	port         int
	noWindow     bool
	settingsPath string
	showVersion  bool
}

// deps are the parts of the outside world run needs; tests replace them.
type deps struct {
	lookup   func(string) (string, bool)
	stdout   io.Writer
	stderr   io.Writer
	hostname func() (string, error)
	window   func(ctx context.Context, url, title string) error // blocks until the window closes
	browser  func(url string) error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], deps{lookup: os.LookupEnv, stdout: os.Stdout, stderr: os.Stderr})
	stop()
	if err != nil {
		reportFatal("Radar Client: " + err.Error())
		os.Exit(exitFailure)
	}
}

func parseConfig(args []string) (config, error) {
	var c config
	if err := core.RejectSecretFlags(args, map[string]string{"key": client.EnvKey, "ingest-key": client.EnvKey}); err != nil {
		return c, err
	}
	fs := flag.NewFlagSet("radar-client", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.IntVar(&c.port, "port", 0, "port of the local UI on 127.0.0.1 (0 = pick a free one)")
	fs.BoolVar(&c.noWindow, "no-window", false, "do not open a window or a browser; print the UI address instead")
	fs.StringVar(&c.settingsPath, "settings", "", "settings file (default: the per-user RadarClient folder)")
	fs.BoolVar(&c.showVersion, "version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if c.port < 0 || c.port > maxPort {
		return c, fmt.Errorf("--port %d is not a valid port", c.port)
	}
	return c, nil
}

// withDefaults fills the dependencies that were not injected.
func (d deps) withDefaults() deps {
	if d.hostname == nil {
		d.hostname = os.Hostname
	}
	if d.window == nil {
		d.window = runWindow
	}
	if d.browser == nil {
		d.browser = openBrowser
	}
	return d
}

// run starts the UI and blocks until ctx ends, the window is closed or Quit is pressed.
func run(ctx context.Context, args []string, d deps) error {
	d = d.withDefaults()
	cfg, err := parseConfig(args)
	if err != nil {
		return err
	}
	if cfg.showVersion {
		_, err := fmt.Fprintln(d.stdout, "radar-client", version)
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	session := client.NewSession(client.SessionOptions{})
	ui, err := newUI(cfg, d, session, cancel)
	if err != nil {
		return err
	}
	ln, err := client.ListenLocal(cfg.port)
	if err != nil {
		return err
	}
	pageURL := client.PageURL(ln, ui.token)
	served := make(chan error, 1)
	go func() { served <- ui.server.Serve(ctx, ln) }()
	fmt.Fprintln(d.stdout, "radar-client UI:", pageURL)

	if cfg.noWindow {
		<-ctx.Done()
	} else {
		showUI(ctx, d, pageURL)
		cancel()
	}
	session.Stop() // closes the upload cleanly before the program ends
	if err := <-served; err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// showUI opens the native window and waits for it to close; without a window it falls back to the
// default browser and waits until ctx ends (Ctrl+C or the Quit button).
func showUI(ctx context.Context, d deps, pageURL string) {
	err := d.window(ctx, pageURL, windowTitle)
	if err == nil {
		return
	}
	fmt.Fprintf(d.stderr, "radar-client: no native window (%v); opening the default browser\n", err)
	if berr := d.browser(pageURL); berr != nil {
		fmt.Fprintf(d.stderr, "radar-client: could not open the browser (%v); open %s yourself\n", berr, pageURL)
	}
	<-ctx.Done()
}
