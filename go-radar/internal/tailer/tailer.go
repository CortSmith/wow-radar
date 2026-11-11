// Package tailer follows the newest WoW combat log in a folder and delivers complete lines.
//
// MULTI-LOG: LogSource is the abstraction the rest of the program consumes. One Follower
// follows "the newest file in one folder". To follow several logs at once (several WoW
// installs / accounts, or the newest file in several folders) create one Follower per
// folder, each with its own ID, run them concurrently and feed every line into the same
// tracker tagged with the Follower's ID (see internal/app).
package tailer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Defaults.
const (
	DefaultPattern   = "WoWCombatLog*.txt"
	DefaultTailBytes = 8 << 20
	DefaultPoll      = 500 * time.Millisecond
	chunk            = 1 << 20
)

// ErrNoLog means the folder holds no matching log file yet.
var ErrNoLog = errors.New("no combat log file found")

// LogSource produces log lines until the context is cancelled.
type LogSource interface {
	// ID tags every line (and therefore every tracker event) from this source.
	ID() string
	// Run delivers complete lines (without line terminator) to handle until ctx ends.
	Run(ctx context.Context, handle func(line string)) error
}

// Options configure a Follower.
type Options struct {
	Dir       string
	Pattern   string        // default DefaultPattern
	FromStart bool          // read the first file from its beginning instead of its last TailBytes
	TailBytes int64         // default DefaultTailBytes
	Poll      time.Duration // default DefaultPoll
	OnFile    func(path string)
	OnError   func(error)
}

// Follower is a LogSource that always follows the most recently modified log in Dir.
// The newest file is re-evaluated on every poll, so rotation is picked up automatically.
type Follower struct {
	id  string
	opt Options

	cur     string
	offset  int64
	pending []byte
	skip    bool // discard bytes up to the first newline (we started mid-line)
	started bool // the first file has been opened (later files are read from their start)

	replayEnd int64 // >0: on the next drain, replay zone/map lines found in bytes [0, replayEnd)
	seen      map[string]int64
	pub       atomic.Value // string; copy of cur safe to read from other goroutines
}

// NewFollower creates a Follower.
func NewFollower(id string, o Options) *Follower {
	if o.Pattern == "" {
		o.Pattern = DefaultPattern
	}
	if o.TailBytes <= 0 {
		o.TailBytes = DefaultTailBytes
	}
	if o.Poll <= 0 {
		o.Poll = DefaultPoll
	}
	return &Follower{id: id, opt: o, seen: map[string]int64{}}
}

// ID implements LogSource.
func (f *Follower) ID() string { return f.id }

// Current returns the file being followed ("" if none).
func (f *Follower) Current() string {
	s, _ := f.pub.Load().(string)
	return s
}

// Newest returns the most recently modified regular file in dir matching pattern
// (ties broken by name).
func Newest(dir, pattern string) (string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var best string
	var bestT time.Time
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if ok, _ := filepath.Match(pattern, e.Name()); !ok {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		mt := info.ModTime()
		if best == "" || mt.After(bestT) || (mt.Equal(bestT) && e.Name() > filepath.Base(best)) {
			best, bestT = filepath.Join(dir, e.Name()), mt
		}
	}
	if best == "" {
		return "", ErrNoLog
	}
	return best, nil
}

// Run polls until ctx is cancelled. Errors are reported through Options.OnError and never stop it.
func (f *Follower) Run(ctx context.Context, handle func(string)) error {
	tk := time.NewTicker(f.opt.Poll)
	defer tk.Stop()
	for {
		if err := f.Poll(handle); err != nil && f.opt.OnError != nil {
			f.opt.OnError(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tk.C:
		}
	}
}

// Poll performs one iteration: pick the newest file, drain the current one, switch if needed.
func (f *Follower) Poll(handle func(string)) error {
	defer func() { f.pub.Store(f.cur) }()
	newest, err := Newest(f.opt.Dir, f.opt.Pattern)
	if errors.Is(err, ErrNoLog) {
		f.cur, f.offset, f.pending = "", 0, nil
		delete(f.seen, "")
		return nil
	}
	if err != nil {
		return err
	}
	if f.cur != "" && newest != f.cur {
		// finish the file we are leaving, then forget its partial line
		if err := f.drain(handle); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		f.seen[f.cur] = f.offset
		f.cur, f.pending = "", nil
	}
	if f.cur == "" {
		f.open(newest)
	}
	if err := f.drain(handle); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// open selects path as the current file and decides where to start reading.
func (f *Follower) open(path string) {
	f.cur, f.pending, f.skip = path, nil, false
	f.pub.Store(path) // visible to line handlers (the bounty analyzer names deaths by log file)
	if f.opt.OnFile != nil {
		f.opt.OnFile(path)
	}
	if off, ok := f.seen[path]; ok {
		f.offset = off // resume a file we followed before (no duplicate lines)
		return
	}
	f.offset = 0
	if !f.started && !f.opt.FromStart {
		if st, err := os.Stat(path); err == nil && st.Size() > f.opt.TailBytes {
			f.offset = st.Size() - f.opt.TailBytes
			f.skip = true
			f.replayEnd = f.offset // zone/map context before this point is replayed on the first drain
		}
	}
	f.started = true
}

// replayContext feeds the ZONE_CHANGE / MAP_CHANGE lines found in the skipped start of the file
// (bytes [0, end)) to handle, in file order. Without them a map's name and world rectangle are
// unknown when the current map was entered before the tail window. Other skipped lines stay skipped.
func (f *Follower) replayContext(fh *os.File, end int64, handle func(string)) {
	var pending []byte
	buf := make([]byte, chunk)
	for off := int64(0); off < end; {
		want := int64(len(buf))
		if end-off < want {
			want = end - off
		}
		n, err := fh.ReadAt(buf[:want], off)
		if n > 0 {
			off += int64(n)
			data := buf[:n]
			for len(data) > 0 {
				i := bytes.IndexByte(data, '\n')
				if i < 0 {
					pending = append(pending, data...)
					break
				}
				line := data[:i]
				if len(pending) > 0 {
					pending = append(pending, line...)
					line = pending
				}
				if bytes.Contains(line, mapChangeTag) || bytes.Contains(line, zoneChangeTag) {
					handle(strings.TrimRight(string(line), "\r"))
				}
				pending = pending[:0]
				data = data[i+1:]
			}
		}
		if err != nil {
			break
		}
	}
}

var (
	mapChangeTag  = []byte("  MAP_CHANGE,")
	zoneChangeTag = []byte("  ZONE_CHANGE,")
)

// drain reads everything appended to the current file since the last read.
func (f *Follower) drain(handle func(string)) error {
	fh, err := os.Open(f.cur)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			f.cur, f.offset, f.pending = "", 0, nil
		}
		return err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return err
	}
	if st.Size() < f.offset { // truncated / rewritten: start over, drop any half line
		f.offset, f.pending, f.skip, f.replayEnd = 0, nil, false, 0
	}
	if f.replayEnd > 0 {
		end := f.replayEnd
		f.replayEnd = 0
		f.replayContext(fh, end, handle)
	}
	buf := make([]byte, chunk)
	for {
		n, err := fh.ReadAt(buf, f.offset)
		if n > 0 {
			f.offset += int64(n)
			f.feed(buf[:n], handle)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", f.cur, err)
		}
	}
}

func (f *Follower) feed(data []byte, handle func(string)) {
	if f.skip {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return
		}
		data, f.skip = data[i+1:], false
	}
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			f.pending = append(f.pending, data...)
			return
		}
		line := data[:i]
		if len(f.pending) > 0 {
			f.pending = append(f.pending, line...)
			line = f.pending
		}
		handle(strings.TrimRight(string(line), "\r"))
		f.pending = f.pending[:0]
		data = data[i+1:]
	}
}
