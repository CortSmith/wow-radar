// Package tailer follows the newest WoW combat log in a folder and delivers complete lines.
//
// A Follower re-evaluates "the newest matching file" on every poll, so a rotation is picked up
// without a restart. It survives truncation, half-written lines and a missing folder. It is a
// rewrite of the idea in go-radar's tailer, reduced to what the feeder needs.
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
	// DefaultPattern matches the combat logs WoW writes.
	DefaultPattern = "WoWCombatLog*.txt"
	// DefaultPoll is how often the folder and the current file are checked.
	DefaultPoll = 500 * time.Millisecond

	chunkBytes = 1 << 20
)

// ErrNoLog means the folder holds no matching log file yet.
var ErrNoLog = errors.New("no combat log file found")

var (
	mapChangeTag  = []byte("  MAP_CHANGE,")
	zoneChangeTag = []byte("  ZONE_CHANGE,")
)

// Options configure a Follower.
type Options struct {
	Dir       string
	Pattern   string        // default DefaultPattern
	FromStart bool          // read the first file from its beginning instead of from its end
	Poll      time.Duration // default DefaultPoll
	OnFile    func(path string)
	OnError   func(error)
}

// Follower follows the most recently modified log in Dir. It is not safe for concurrent
// Poll calls; Current may be read from any goroutine.
type Follower struct {
	opt     Options
	cur     string
	offset  int64
	pending []byte
	polled  bool // the first poll has happened (only a file seen then is entered at its end)
	seen    map[string]int64
	pub     atomic.Value // string copy of cur
}

// NewFollower creates a Follower.
func NewFollower(o Options) *Follower {
	if o.Pattern == "" {
		o.Pattern = DefaultPattern
	}
	if o.Poll <= 0 {
		o.Poll = DefaultPoll
	}
	return &Follower{opt: o, seen: map[string]int64{}}
}

// Current returns the file being followed ("" if none).
func (f *Follower) Current() string {
	s, _ := f.pub.Load().(string)
	return s
}

// Newest returns the most recently modified regular file in dir matching pattern
// (ties broken by name, the later name wins).
func Newest(dir, pattern string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var best string
	var bestTime time.Time
	for _, e := range entries {
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
		if best == "" || mt.After(bestTime) || (mt.Equal(bestTime) && e.Name() > filepath.Base(best)) {
			best, bestTime = filepath.Join(dir, e.Name()), mt
		}
	}
	if best == "" {
		return "", ErrNoLog
	}
	return best, nil
}

// Run polls until ctx ends and returns ctx.Err(). Poll errors go to Options.OnError and never stop it.
func (f *Follower) Run(ctx context.Context, handle func(string)) error {
	tick := time.NewTicker(f.opt.Poll)
	defer tick.Stop()
	for {
		if err := f.Poll(handle); err != nil && f.opt.OnError != nil {
			f.opt.OnError(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// Poll performs one iteration: pick the newest file, drain the current one, switch if needed.
func (f *Follower) Poll(handle func(string)) error {
	defer func() { f.pub.Store(f.cur) }()
	first := !f.polled
	f.polled = true
	newest, err := Newest(f.opt.Dir, f.opt.Pattern)
	if errors.Is(err, ErrNoLog) {
		f.cur, f.offset, f.pending = "", 0, nil
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
		f.open(newest, first, handle)
	}
	if err := f.drain(handle); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// open selects path as the current file and decides where to start reading. A file present at
// the first poll is entered at its end (after replaying its latest zone and map lines); every
// other file is read from its start, or resumed where it was left.
func (f *Follower) open(path string, first bool, handle func(string)) {
	f.cur, f.pending, f.offset = path, nil, 0
	f.pub.Store(path)
	if f.opt.OnFile != nil {
		f.opt.OnFile(path)
	}
	if off, ok := f.seen[path]; ok {
		f.offset = off
		return
	}
	if !first || f.opt.FromStart {
		return
	}
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	f.offset = st.Size()
	replayContext(path, st.Size(), handle)
}

// replayContext delivers the last ZONE_CHANGE and the last MAP_CHANGE line found in the first
// size bytes of path (zone line first). Without them the chart a player is standing in is unknown.
func replayContext(path string, size int64, handle func(string)) {
	fh, err := os.Open(path)
	if err != nil {
		return
	}
	defer fh.Close()
	var lastZone, lastMap string
	scan := func(line []byte) {
		switch {
		case bytes.Contains(line, zoneChangeTag):
			lastZone = strings.TrimRight(string(line), "\r")
		case bytes.Contains(line, mapChangeTag):
			lastMap = strings.TrimRight(string(line), "\r")
		}
	}
	var carry []byte
	buf := make([]byte, chunkBytes)
	for off := int64(0); off < size; {
		want := min(int64(len(buf)), size-off)
		n, err := fh.ReadAt(buf[:want], off)
		off += int64(n)
		carry = splitLines(append(carry, buf[:n]...), scan)
		if err != nil {
			break
		}
	}
	for _, l := range []string{lastZone, lastMap} {
		if l != "" {
			handle(l)
		}
	}
}

// splitLines calls each for every complete line in data and returns the unfinished tail.
func splitLines(data []byte, each func([]byte)) []byte {
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return append([]byte(nil), data...)
		}
		each(data[:i])
		data = data[i+1:]
	}
}

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
	if st.Size() < f.offset { // truncated or rewritten: start over, drop any half line
		f.offset, f.pending = 0, nil
	}
	buf := make([]byte, chunkBytes)
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

// feed splits data into lines, keeping an unfinished last line for the next read.
func (f *Follower) feed(data []byte, handle func(string)) {
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
