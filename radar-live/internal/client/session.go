package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"radarlive/internal/feed"
	"radarlive/internal/feed/tailer"
)

const (
	uploadBytesPerSec = 256 << 10 // upload cap; also bounds the replay of "include existing content"
	queueLines        = 200_000   // lines held while the server is unreachable (oldest dropped)
	defaultSource     = "client"
)

// ErrAlreadyRunning is returned by Start while an upload is active.
var ErrAlreadyRunning = errors.New("an upload is already running")

// Uploader sends the lines in a queue to the server until ctx ends. *feed.Streamer is the real one.
type Uploader interface {
	Run(ctx context.Context, q *feed.Queue) error
}

// SessionOptions configure a Session; every field is optional and exists for tests.
type SessionOptions struct {
	Now         func() time.Time                   // default time.Now
	Poll        time.Duration                      // how often the log is checked; default tailer.DefaultPoll
	NewUploader func(feed.StreamerConfig) Uploader // default: feed.NewStreamer
}

// StartConfig is everything one run needs. Key is the ingest key and stays in memory.
type StartConfig struct {
	Settings Settings
	Key      string
	Source   string // id shown by ingest; default "client"
}

// Session follows a combat log and uploads new lines while it runs. Start and Stop may be called
// from any goroutine; Status is cheap and safe to call at any time.
type Session struct {
	opts     SessionOptions
	progress *progress

	lifecycle sync.Mutex // serialises Start and Stop
	mu        sync.RWMutex
	active    *activeRun
}

// activeRun is the state of one Start..Stop period.
type activeRun struct {
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	follower *tailer.Follower
	queue    *feed.Queue
}

// NewSession returns an idle Session.
func NewSession(opts SessionOptions) *Session {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewUploader == nil {
		opts.NewUploader = func(cfg feed.StreamerConfig) Uploader { return feed.NewStreamer(cfg) }
	}
	return &Session{opts: opts, progress: newProgress(opts.Now)}
}

// Start validates cfg and begins following the log and uploading. It returns at once.
func (s *Session) Start(cfg StartConfig) error {
	st := cfg.Settings.Normalized()
	if err := st.ValidateForStart(); err != nil {
		return err
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		return ErrAlreadyRunning
	}
	s.progress.begin()
	run := s.launch(st, cfg)
	s.active = run
	return nil
}

// launch starts the tailer and the uploader goroutines.
func (s *Session) launch(st Settings, cfg StartConfig) *activeRun {
	ctx, cancel := context.WithCancel(context.Background())
	dir, pattern := followTarget(st)
	run := &activeRun{cancel: cancel, queue: feed.NewQueue(queueLines)}
	run.follower = tailer.NewFollower(tailer.Options{
		Dir: dir, Pattern: pattern, FromStart: st.IncludeExisting, Poll: s.opts.Poll,
		OnFile: func(path string) {
			s.progress.setWarning("")
			s.progress.note("Following %s", filepath.Base(path))
		},
		OnError: func(err error) { s.progress.setWarning("Cannot read the log folder: " + err.Error()) },
	})
	uploader := s.opts.NewUploader(feed.StreamerConfig{
		URL: st.Server, Key: cfg.Key, Source: sourceName(cfg.Source), Ruleset: st.Ruleset,
		MaxBytesPerSec: uploadBytesPerSec, Observe: s.progress.handle,
	})
	run.wg.Add(2)
	go func() {
		defer run.wg.Done()
		_ = run.follower.Run(ctx, run.queue.Push)
	}()
	go func() {
		defer run.wg.Done()
		if err := uploader.Run(ctx, run.queue); err != nil && ctx.Err() == nil {
			s.progress.fail("The upload stopped: " + rootCause(err))
		}
	}()
	s.progress.note("Started")
	return run
}

// Stop ends the upload, waits for the stream to close (so the server's summary is recorded) and
// returns the final status. Stopping an idle session is a no-op.
func (s *Session) Stop() Status {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	run := s.active
	s.active = nil
	s.mu.Unlock()
	if run != nil {
		run.cancel()
		run.wg.Wait()
		s.progress.end()
		s.progress.note("Stopped")
	}
	return s.Status()
}

// Status returns a snapshot for the UI.
func (s *Session) Status() Status {
	s.mu.RLock()
	run := s.active
	s.mu.RUnlock()
	if run == nil {
		return s.progress.snapshot("", 0, 0)
	}
	return s.progress.snapshot(run.follower.Current(), run.queue.Len(), run.queue.Dropped())
}

// followTarget turns the settings into the folder and file-name pattern the tailer follows: the
// newest combat log of a folder, or exactly one file.
func followTarget(st Settings) (dir, pattern string) {
	if st.FollowNewest {
		if info, err := os.Stat(st.LogPath); err == nil && !info.IsDir() {
			return filepath.Dir(st.LogPath), tailer.DefaultPattern
		}
		return st.LogPath, tailer.DefaultPattern
	}
	return filepath.Dir(st.LogPath), escapeGlob(filepath.Base(st.LogPath))
}

// escapeGlob makes name match only itself in filepath.Match, on every platform (a backslash
// cannot be used as an escape on Windows, so each special character becomes a one-character class).
func escapeGlob(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '*', '?', '[':
			fmt.Fprintf(&b, "[%c]", r)
		case '\\':
			b.WriteString(`[\\]`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sourceName makes a valid ingest source id from s.
func sourceName(s string) string {
	if strings.TrimSpace(s) == "" {
		return defaultSource
	}
	return feed.SanitizeSource(s)
}

// DefaultSource derives the source id from the computer name, e.g. "my-pc-client".
func DefaultSource(hostname string) string {
	return sourceName(strings.TrimSpace(hostname) + "-" + defaultSource)
}
