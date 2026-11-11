package bounty

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nearbyradar/internal/parser"
)

// Reverse analysis limits.
const (
	// DefaultPattern matches WoW combat log file names.
	DefaultPattern = "WoWCombatLog*.txt"
	// MaxRequestFiles caps how many file names one request may name.
	MaxRequestFiles = 256

	maxLineBytes     = 1 << 20 // longer lines are skipped, not parsed
	progressEvery    = 4096    // lines between progress callbacks
	cancelCheckEvery = 1024    // lines between context checks
	maxStatusErrors  = 20
)

// ErrBusy is returned when a job is already running.
var ErrBusy = errors.New("an analysis is already running")

// BadFileError means a requested file name is not allowed or not usable.
type BadFileError struct{ Name, Reason string }

func (e *BadFileError) Error() string { return fmt.Sprintf("bad file %q: %s", e.Name, e.Reason) }

// Status is the progress of the current or last analysis job.
type Status struct {
	JobID       string   `json:"jobId"`
	Running     bool     `json:"running"`
	Cancelled   bool     `json:"cancelled"`
	FilesDone   int      `json:"filesDone"`
	FilesTotal  int      `json:"filesTotal"`
	Bytes       int64    `json:"bytes"`
	BytesTotal  int64    `json:"bytesTotal"`
	Deaths      int      `json:"deaths"`    // friendly player deaths found in the scanned files
	NewDeaths   int      `json:"newDeaths"` // of those, not recorded before (idempotency)
	Errors      []string `json:"errors"`
	CurrentFile string   `json:"currentFile"`
	StartedAt   int64    `json:"startedAt"`  // unix ms
	FinishedAt  int64    `json:"finishedAt"` // unix ms, 0 while running
}

// RunnerConfig configures a Runner.
type RunnerConfig struct {
	Dir         string // logs folder: the only place API-named files are read from
	Pattern     string // default DefaultPattern
	Sink        Sink
	Loc         *time.Location // for timestamps without UTC offset (nil = time.Local)
	RealmSuffix func() string  // optional: realm suffix for display names
	Context     context.Context

	beforeFile func() // test hook
}

// Runner runs one background reverse-analysis job at a time.
type Runner struct {
	cfg RunnerConfig

	mu     sync.Mutex
	ctx    context.Context
	status Status
	seq    int
	wg     sync.WaitGroup
}

// NewRunner creates a Runner.
func NewRunner(c RunnerConfig) *Runner {
	if c.Pattern == "" {
		c.Pattern = DefaultPattern
	}
	if c.Context == nil {
		c.Context = context.Background()
	}
	return &Runner{cfg: c, ctx: c.Context, status: Status{Errors: []string{}}}
}

// SetContext replaces the context jobs started from now on run under (cancel it to stop them).
func (r *Runner) SetContext(ctx context.Context) {
	r.mu.Lock()
	r.ctx = ctx
	r.mu.Unlock()
}

// Status returns a copy of the current/last job status.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.status
	s.Errors = append([]string{}, s.Errors...)
	return s
}

// Wait blocks until the running job (if any) has finished.
func (r *Runner) Wait() { r.wg.Wait() }

// ListLogs returns the files in dir matching pattern, oldest to newest by modification time
// (ties by name). The result is absolute-joined with dir.
func ListLogs(dir, pattern string) ([]string, error) {
	if pattern == "" {
		pattern = DefaultPattern
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type f struct {
		p string
		t time.Time
	}
	var fs []f
	for _, e := range ents {
		if ok, _ := filepath.Match(pattern, e.Name()); !ok || e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		fs = append(fs, f{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	sort.Slice(fs, func(i, j int) bool {
		if !fs[i].t.Equal(fs[j].t) {
			return fs[i].t.Before(fs[j].t)
		}
		return fs[i].p < fs[j].p
	})
	out := make([]string, len(fs))
	for i, x := range fs {
		out[i] = x.p
	}
	return out, nil
}

// Start begins a background job over files of the logs folder. names == nil analyses every
// combat log in the folder, oldest to newest. Named files must be plain file names inside
// the folder (no separators, no traversal); anything else is rejected with a *BadFileError.
// It returns ErrBusy if a job is running.
func (r *Runner) Start(names []string) (string, error) {
	r.mu.Lock()
	if r.status.Running {
		r.mu.Unlock()
		return "", ErrBusy
	}
	r.mu.Unlock()
	var paths []string
	var err error
	if names == nil {
		paths, err = ListLogs(r.cfg.Dir, r.cfg.Pattern)
	} else {
		paths, err = r.resolve(names)
	}
	if err != nil {
		return "", err
	}
	return r.begin(paths)
}

// StartPaths starts a job over explicit, already trusted paths (the --analyze-file arguments).
func (r *Runner) StartPaths(paths []string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("no files to analyze")
	}
	return r.begin(paths)
}

func (r *Runner) begin(paths []string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.Running {
		return "", ErrBusy
	}
	r.seq++
	id := strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(r.seq)
	var total int64
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	r.status = Status{JobID: id, Running: true, FilesTotal: len(paths), BytesTotal: total,
		Errors: []string{}, StartedAt: time.Now().UnixMilli()}
	ctx := r.ctx
	r.wg.Add(1)
	go func() { defer r.wg.Done(); r.run(ctx, paths) }()
	return id, nil
}

func (r *Runner) update(f func(*Status)) {
	r.mu.Lock()
	f(&r.status)
	r.mu.Unlock()
}

func (r *Runner) run(ctx context.Context, paths []string) {
	var baseBytes int64
	var deaths, fresh int
	suffix := ""
	if r.cfg.RealmSuffix != nil {
		suffix = r.cfg.RealmSuffix()
	}
	for _, p := range paths {
		if ctx.Err() != nil {
			r.update(func(s *Status) { s.Cancelled = true })
			break
		}
		if r.cfg.beforeFile != nil {
			r.cfg.beforeFile()
		}
		name := filepath.Base(p)
		var size int64
		if fi, err := os.Stat(p); err == nil {
			size = fi.Size()
		}
		r.update(func(s *Status) { s.CurrentFile = name })
		// A fresh analyzer per file: the sliding windows never leak between files, while the
		// shared sink keeps the results (and de-duplicates by death id).
		an := NewAnalyzer(r.cfg.Sink, Options{RealmSuffix: suffix})
		an.SetFile(name)
		_, err := AnalyzeFile(ctx, p, an, r.cfg.Loc, func(b int64) {
			st := an.Stats()
			r.update(func(s *Status) {
				s.Bytes = baseBytes + b
				s.Deaths = deaths + st.Deaths
				s.NewDeaths = fresh + st.Recorded
			})
		})
		st := an.Stats()
		deaths += st.Deaths
		fresh += st.Recorded
		baseBytes += size
		cancelled := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		r.update(func(s *Status) {
			s.Deaths, s.NewDeaths, s.Bytes = deaths, fresh, baseBytes
			if cancelled {
				s.Cancelled = true
				return
			}
			s.FilesDone++
			if err != nil && len(s.Errors) < maxStatusErrors {
				s.Errors = append(s.Errors, name+": "+err.Error())
			}
		})
		if cancelled {
			break
		}
	}
	r.update(func(s *Status) {
		s.Running = false
		s.CurrentFile = ""
		s.FinishedAt = time.Now().UnixMilli()
	})
}

// resolve validates API-supplied names and returns full paths, oldest to newest.
func (r *Runner) resolve(names []string) ([]string, error) {
	if len(names) > MaxRequestFiles {
		return nil, &BadFileError{Name: fmt.Sprintf("%d files", len(names)), Reason: "too many files in one request"}
	}
	root, err := filepath.EvalSymlinks(r.cfg.Dir)
	if err != nil {
		return nil, err
	}
	root, _ = filepath.Abs(root)
	seen := map[string]bool{}
	type f struct {
		p string
		t time.Time
	}
	var fs []f
	for _, n := range names {
		bad := func(why string) error { return &BadFileError{Name: n, Reason: why} }
		switch {
		case n == "" || n == "." || n == "..":
			return nil, bad("not a file name")
		case strings.ContainsAny(n, "/\\:*?\"<>|\x00"):
			return nil, bad("must be a plain file name inside the logs folder")
		case filepath.Base(n) != n:
			return nil, bad("must be a plain file name inside the logs folder")
		}
		if ok, _ := filepath.Match(r.cfg.Pattern, n); !ok {
			return nil, bad("not a combat log file name")
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		full := filepath.Join(r.cfg.Dir, n)
		res, err := filepath.EvalSymlinks(full)
		if err != nil {
			return nil, bad("file not found")
		}
		res, _ = filepath.Abs(res)
		if rel, err := filepath.Rel(root, res); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return nil, bad("outside the logs folder")
		}
		fi, err := os.Stat(res)
		if err != nil || !fi.Mode().IsRegular() {
			return nil, bad("not a regular file")
		}
		fs = append(fs, f{full, fi.ModTime()})
	}
	sort.Slice(fs, func(i, j int) bool {
		if !fs[i].t.Equal(fs[j].t) {
			return fs[i].t.Before(fs[j].t)
		}
		return fs[i].p < fs[j].p
	})
	out := make([]string, len(fs))
	for i, x := range fs {
		out[i] = x.p
	}
	return out, nil
}

// AnalyzeFile streams one log file line by line through the parser into an. Memory use is
// bounded (lines over 1 MiB are skipped); progress receives the bytes consumed so far every
// few thousand lines and once at the end. It returns the number of lines read, and ctx's error
// if cancelled.
func AnalyzeFile(ctx context.Context, path string, an *Analyzer, loc *time.Location, progress func(bytes int64)) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if loc == nil {
		loc = time.Local
	}
	br := bufio.NewReaderSize(f, maxLineBytes)
	var bytes int64
	lines := 0
	skipping := false
	report := func() {
		if progress != nil {
			progress(bytes)
		}
	}
	for {
		chunk, err := br.ReadSlice('\n')
		bytes += int64(len(chunk))
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			skipping = true // over-long line: drop everything up to its newline
			continue
		case err != nil && !errors.Is(err, io.EOF):
			return lines, err
		}
		if skipping {
			skipping = false
			lines++
		} else if len(chunk) > 0 {
			lines++
			if line := strings.TrimRight(string(chunk), "\r\n"); line != "" {
				if ev, perr := parser.ParseLine(line, loc); perr == nil {
					an.Apply(ev)
				}
			}
		}
		if err != nil { // EOF
			break
		}
		if lines%cancelCheckEvery == 0 {
			if cerr := ctx.Err(); cerr != nil {
				report()
				return lines, cerr
			}
		}
		if lines%progressEvery == 0 {
			report()
		}
	}
	report()
	return lines, ctx.Err()
}
