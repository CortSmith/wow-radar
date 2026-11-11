package app

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"nearbyradar/internal/bounty"
	"nearbyradar/internal/clock"
	"nearbyradar/internal/dataset"
	"nearbyradar/internal/maps"
	"nearbyradar/internal/parser"
	"nearbyradar/internal/screenshot"
	"nearbyradar/internal/server"
	"nearbyradar/internal/tailer"
	"nearbyradar/internal/tracker"
)

// App owns all long-running components.
type App struct {
	cfg     Config
	logf    func(string, ...any)
	Tracker *tracker.Tracker
	Data    *dataset.Registry // per-ruleset history + bounty stores; routes to the active rule-set
	History *dataset.Registry // same object as Data (history side)
	Bounty  *dataset.Registry // same object as Data (bounty side: hostile-player kill ledger)
	Runner  *bounty.Runner    // background reverse analysis of old logs
	Server  *server.Server
	Sources []tailer.LogSource // MULTI-LOG: append more sources here before Start

	// ScreenshotReader feeds the tracker through the screenshot connector. nil = NoopReader (disabled).
	ScreenshotReader   screenshot.Reader
	ScreenshotInterval time.Duration

	follower *tailer.Follower
	wg       sync.WaitGroup
	lines    atomic.Int64
	bad      atomic.Int64
}

// New builds the application; nothing runs until Start.
func New(cfg Config, clk clock.Clock, logf func(string, ...any)) (*App, error) {
	if clk == nil {
		clk = clock.Real{}
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	a := &App{cfg: cfg, logf: logf}
	hp := ResolveHistoryPath(cfg.ExeDir, cfg.HistoryPath, appData())
	// One dataset (history + bounty ledger) per server rule-set; the registry routes every
	// read and write to the active one, so tracker, analyzer, runner and server just use it.
	data, err := dataset.New(dataset.Options{HistoryPath: hp, Initial: cfg.Ruleset, Logf: logf})
	if err != nil {
		return nil, err
	}
	a.Data, a.History, a.Bounty = data, data, data
	a.Tracker = tracker.New(tracker.Config{
		TrackNPCs: cfg.TrackNPCs, DisableApprox: cfg.NoApprox, RealmSuffix: cfg.RealmSuffix,
		StaleAfter: cfg.Stale, ExpireAfter: cfg.Expire, History: data,
	}, clk)

	a.Runner = bounty.NewRunner(bounty.RunnerConfig{
		Dir: cfg.LogsDir, Sink: data, Loc: time.Local,
		RealmSuffix: func() string {
			if cfg.RealmSuffix != "" {
				return cfg.RealmSuffix
			}
			return a.Tracker.RealmSuffix()
		},
	})

	a.follower = tailer.NewFollower("log1", tailer.Options{
		Dir: cfg.LogsDir, FromStart: cfg.FromStart, TailBytes: int64(cfg.TailMB) << 20,
		OnFile:  func(p string) { logf("following %s", p) },
		OnError: func(err error) { logf("tailer: %v", err) },
	})
	// MULTI-LOG: add further followers (one per folder / WoW client) here, each with a
	// distinct ID, e.g. a.Sources = append(a.Sources, tailer.NewFollower("log2", ...)).
	a.Sources = []tailer.LogSource{a.follower}

	var idx *maps.Index
	if ix, err := maps.LoadFirst(maps.Candidates(cfg.ExeDir, cfg.MapsDir)); err == nil {
		idx = ix
		logf("maps: %s (%d zones)", ix.Dir, len(ix.List()))
	} else {
		logf("maps: none found (radar background disabled)")
	}
	a.Server = server.New(server.Options{
		State: a.Tracker, History: data, Bounty: data, Rulesets: data, Analyzer: a.Runner, Maps: idx, Orientation: cfg.Orientation,
		Status: func() server.Status {
			f := a.follower.Current()
			name := ""
			if f != "" {
				name = baseName(f)
			}
			return server.Status{LogFile: name, LogsDir: cfg.LogsDir}
		},
	})
	return a, nil
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '\\' || p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// Counters returns how many log lines were seen and how many failed to parse.
func (a *App) Counters() (lines, bad int64) { return a.lines.Load(), a.bad.Load() }

// Pump runs one LogSource into the tracker, tagging every event with the source's ID.
//
// MULTI-LOG: this is the single place where a source meets the tracker. N sources simply
// means N Pump goroutines; the tracker serialises Apply calls and keeps "me", zone and map
// per SourceID.
func (a *App) Pump(ctx context.Context, src tailer.LogSource) {
	id := tracker.SourceID(src.ID())
	// MULTI-LOG: one live bounty analyzer per source; its damage windows are per log file and the
	// shared Bounty store de-duplicates deaths by id (victim + time + log file name), so a live
	// run and a later reverse analysis of the same file never double count.
	an := bounty.NewAnalyzer(a.Bounty, bounty.Options{RealmSuffix: a.cfg.RealmSuffix})
	current, _ := src.(interface{ Current() string })
	lastMe, lastSfx, n := "", "", 0
	_ = src.Run(ctx, func(line string) {
		if line == "" {
			return
		}
		a.lines.Add(1)
		ev, err := parser.ParseLine(line, time.Local)
		if err != nil {
			a.bad.Add(1)
			return
		}
		// Detect the rule-set BEFORE the event is recorded, so the very first event naming
		// "me" already lands in the right dataset.
		if raw := mineName(ev); raw != "" && raw != lastMe {
			lastMe = raw
			a.Data.ObserveMe(raw)
		}
		a.Tracker.Apply(id, ev)
		// Weaker hint while "me" is unknown: the realm token shared by the players seen.
		if n++; lastSfx == "" || n%512 == 0 {
			if sfx := a.Tracker.RealmSuffix(); sfx != lastSfx {
				lastSfx = sfx
				a.Data.ObserveSuffix(sfx)
			}
		}
		name := src.ID()
		if current != nil {
			if f := baseName(current.Current()); f != "" {
				name = f
			}
		}
		an.SetFile(name)
		an.Apply(ev)
	})
}

// Start launches every component and returns the UI URL. Cancel ctx to stop; then call Wait.
func (a *App) Start(ctx context.Context) (string, error) {
	addr, err := a.Server.Start(ctx)
	if err != nil {
		return "", err
	}
	a.logf("listening on %s", addr)
	for _, src := range a.Sources {
		src := src
		a.wg.Add(1)
		go func() { defer a.wg.Done(); a.Pump(ctx, src) }()
	}
	a.wg.Add(1)
	go func() { defer a.wg.Done(); a.Data.Run(ctx, 10*time.Second) }() // flushes every open rule-set dataset
	a.startStartupAnalysis(ctx)

	// Screenshot connector: disabled (returns at once) while the reader is the Noop default.
	rd := a.ScreenshotReader
	if rd == nil {
		rd = screenshot.NoopReader{}
	}
	p := &screenshot.Poller{Reader: rd, Sink: a.Tracker, Interval: a.ScreenshotInterval,
		OnError: func(err error) { a.logf("screenshot: %v", err) }}
	a.wg.Add(1)
	go func() { defer a.wg.Done(); p.Run(ctx) }()
	return a.Server.URL(), nil
}

// Wait blocks until all goroutines have stopped (history is flushed by then).
func (a *App) Wait() { a.wg.Wait() }

// WaitForClients returns once at least one UI client has connected and, afterwards, none
// has been connected for grace (or ctx ends). Used in browser mode so closing the tab ends the process.
func (a *App) WaitForClients(ctx context.Context, grace time.Duration) {
	seen := false
	var idleSince time.Time
	tk := time.NewTicker(250 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		if a.Server.Clients() > 0 {
			seen, idleSince = true, time.Time{}
			continue
		}
		if !seen {
			continue
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
		} else if time.Since(idleSince) >= grace {
			return
		}
	}
}

// startStartupAnalysis runs the --analyze-logs / --analyze-file reverse analysis in the background.
func (a *App) startStartupAnalysis(ctx context.Context) {
	a.Runner.SetContext(ctx)
	var paths []string
	if a.cfg.AnalyzeLogs {
		ls, err := bounty.ListLogs(a.cfg.LogsDir, "")
		if err != nil {
			a.logf("analyze-logs: %v", err)
		}
		paths = append(paths, ls...)
	}
	paths = append(paths, a.cfg.AnalyzeFiles...)
	if len(paths) == 0 {
		return
	}
	if _, err := a.Runner.StartPaths(paths); err != nil {
		a.logf("analyze: %v", err)
		return
	}
	a.logf("analyze: scanning %d log file(s) in the background", len(paths))
}

// mineName returns the raw name of the log owner when the event names them (player GUID with
// the "affiliation mine" flag), else "".
//
// MULTI-LOG: with several logs each source has its own "me"; the dataset follows whichever
// source's owner was seen last (the primary source), like the tracker's primary "me".
func mineName(ev parser.Event) string {
	for _, p := range []struct {
		guid, name string
		flags      uint32
	}{{ev.Src.GUID, ev.Src.Name, ev.Src.Flags}, {ev.Dst.GUID, ev.Dst.Name, ev.Dst.Flags}} {
		if p.name != "" && parser.KindOf(p.guid) == parser.KindPlayer && parser.IsMine(p.flags) {
			return p.name
		}
	}
	return ""
}
