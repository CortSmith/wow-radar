// Command radar-feed runs next to the game: it follows the newest WoWCombatLog*.txt and streams
// every new line to radar-ingest's /v1/stream. It starts at the end of the log (use --from-start
// to send the whole file), survives log rotation, and keeps lines in a bounded queue while the
// ingest server is unreachable.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
	"radarlive/internal/feed"
	"radarlive/internal/feed/tailer"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	defaultLogs         = `C:\Program Files (x86)\World of Warcraft\_forever_\Logs`
	defaultIngestURL    = "http://127.0.0.1:8788"
	defaultMaxBytesRate = 256 << 10
	defaultQueueLines   = 200_000
	envPrefix           = "RADAR_FEED_"
	exitFailure         = 1
)

// config is the parsed command line.
type config struct {
	logs, pattern, ingestURL, key, source, rs string
	fromStart                                 bool
	maxBytesPerSec, queueLines                int
	poll                                      time.Duration
	showVersion                               bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.LookupEnv, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "radar-feed:", err)
		os.Exit(exitFailure)
	}
}

// parseConfig reads flags; RADAR_FEED_<NAME> variables supply the defaults of the string flags.
// The ingest key is a secret: it comes only from RADAR_FEED_KEY or from the file named by
// --key-file / RADAR_FEED_KEY_FILE (a path, not a value); there is no --key flag.
func parseConfig(args []string, lookup func(string) (string, bool)) (config, error) {
	var c config
	var keyFile string
	if err := core.RejectSecretFlags(args, map[string]string{"key": envPrefix + "KEY"}); err != nil {
		return c, err
	}
	fromEnv := func(name, fallback string) string {
		if v, ok := lookup(envPrefix + name); ok && v != "" {
			return v
		}
		return fallback
	}
	c.key = fromEnv("KEY", "")
	host, _ := os.Hostname()
	fs := flag.NewFlagSet("radar-feed", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.logs, "logs", fromEnv("LOGS", defaultLogs), "folder holding WoWCombatLog*.txt [RADAR_FEED_LOGS]")
	fs.StringVar(&c.pattern, "pattern", tailer.DefaultPattern, "log file name pattern")
	fs.StringVar(&c.ingestURL, "ingest-url", fromEnv("INGEST_URL", defaultIngestURL), "base URL of radar-ingest [RADAR_FEED_INGEST_URL]")
	fs.StringVar(&keyFile, "key-file", fromEnv("KEY_FILE", ""), "file holding the ingest key (path only) [RADAR_FEED_KEY_FILE]")
	fs.StringVar(&c.source, "source", fromEnv("SOURCE", host), "source id shown by ingest [RADAR_FEED_SOURCE]")
	fs.StringVar(&c.rs, "rs", fromEnv("RS", ""), "ruleset normal|pvp|rp|rppvp; empty = ingest detects it [RADAR_FEED_RS]")
	fs.BoolVar(&c.fromStart, "from-start", false, "send the whole current log instead of starting at its end")
	fs.IntVar(&c.maxBytesPerSec, "max-bytes-per-sec", defaultMaxBytesRate, "upload cap in bytes per second (0 = unlimited)")
	fs.IntVar(&c.queueLines, "queue-lines", defaultQueueLines, "lines kept while ingest is unreachable (oldest dropped)")
	fs.DurationVar(&c.poll, "poll", tailer.DefaultPoll, "how often the log is checked")
	fs.BoolVar(&c.showVersion, "version", false, "print the version and exit")
	var help bytes.Buffer
	fs.SetOutput(&help)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return c, fmt.Errorf("usage of radar-feed (the ingest key is read from RADAR_FEED_KEY or RADAR_FEED_KEY_FILE, never a flag):\n%s", help.String())
		}
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return c, c.finish(keyFile)
}

// finish loads the key file and validates the values.
func (c *config) finish(keyFile string) error {
	if keyFile != "" {
		data, err := os.ReadFile(keyFile)
		if err != nil {
			return fmt.Errorf("key file: %w", err)
		}
		c.key = strings.TrimSpace(string(data))
	}
	c.source = feed.SanitizeSource(c.source)
	if u, err := url.Parse(c.ingestURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("--ingest-url %q must be http:// or https:// with a host", c.ingestURL)
	}
	switch {
	case c.queueLines < 1:
		return errors.New("--queue-lines must be at least 1")
	case c.maxBytesPerSec < 0:
		return errors.New("--max-bytes-per-sec must not be negative")
	case c.poll <= 0:
		return errors.New("--poll must be positive")
	}
	if c.rs != "" {
		if _, err := domain.ParseRuleset(c.rs); err != nil {
			return fmt.Errorf("--rs: %w", err)
		}
	}
	return nil
}

// run follows the log and streams it until ctx ends.
func run(ctx context.Context, args []string, lookup func(string) (string, bool), logOut io.Writer) error {
	cfg, err := parseConfig(args, lookup)
	if err != nil {
		return err
	}
	if cfg.showVersion {
		_, err := fmt.Fprintln(logOut, "radar-feed", version)
		return err
	}
	log := slog.New(slog.NewTextHandler(logOut, nil))
	queue := feed.NewQueue(cfg.queueLines)
	follower := tailer.NewFollower(tailer.Options{
		Dir: cfg.logs, Pattern: cfg.pattern, FromStart: cfg.fromStart, Poll: cfg.poll,
		OnFile:  func(p string) { log.Info("following log", "file", p) },
		OnError: onceEvery(time.Minute, func(e error) { log.Warn("cannot read log folder", "dir", cfg.logs, "err", e) }),
	})
	streamer := feed.NewStreamer(feed.StreamerConfig{
		URL: cfg.ingestURL, Key: cfg.key, Source: cfg.source, Ruleset: cfg.rs, MaxBytesPerSec: cfg.maxBytesPerSec,
		Logf: func(format string, a ...any) { log.Warn(fmt.Sprintf(format, a...)) },
	})
	log.Info("radar-feed started", "version", version, "ingest", cfg.ingestURL, "source", cfg.source, "logs", cfg.logs)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = follower.Run(ctx, queue.Push)
	}()
	err = streamer.Run(ctx, queue)
	wg.Wait()
	if dropped := queue.Dropped(); dropped > 0 {
		log.Warn("lines dropped while ingest was unreachable", "lines", dropped)
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// onceEvery wraps fn so that it runs at most once per interval (a missing folder is polled twice a second).
func onceEvery(interval time.Duration, fn func(error)) func(error) {
	var last time.Time
	return func(e error) {
		if now := time.Now(); now.Sub(last) >= interval {
			last = now
			fn(e)
		}
	}
}
