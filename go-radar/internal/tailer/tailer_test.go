package tailer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type collector struct {
	mu    sync.Mutex
	lines []string
}

func (c *collector) handle(s string) { c.mu.Lock(); c.lines = append(c.lines, s); c.mu.Unlock() }
func (c *collector) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.lines
	c.lines = nil
	return out
}

func write(t *testing.T, path, s string, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, s string, mod time.Time) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_ = os.Chtimes(path, mod, mod)
}

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func eqLines(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", got, want)
	}
}

func newF(dir string, o Options) *Follower {
	o.Dir = dir
	return NewFollower("src", o)
}

func TestNewestPicksLatestModTime(t *testing.T) {
	dir := t.TempDir()
	if _, err := Newest(dir, DefaultPattern); err != ErrNoLog {
		t.Fatalf("empty dir: %v", err)
	}
	if _, err := Newest(filepath.Join(dir, "missing"), DefaultPattern); err == nil {
		t.Fatal("missing dir must error")
	}
	write(t, filepath.Join(dir, "WoWCombatLog-1.txt"), "a", base)
	write(t, filepath.Join(dir, "WoWCombatLog-2.txt"), "a", base.Add(time.Hour))
	write(t, filepath.Join(dir, "Other.txt"), "a", base.Add(2*time.Hour))
	_ = os.Mkdir(filepath.Join(dir, "WoWCombatLog-dir.txt"), 0o700)
	got, err := Newest(dir, DefaultPattern)
	if err != nil || filepath.Base(got) != "WoWCombatLog-2.txt" {
		t.Fatalf("%q %v", got, err)
	}
	// equal mtimes: name breaks the tie deterministically
	write(t, filepath.Join(dir, "WoWCombatLog-3.txt"), "a", base.Add(time.Hour))
	got, _ = Newest(dir, DefaultPattern)
	if filepath.Base(got) != "WoWCombatLog-3.txt" {
		t.Fatalf("tie: %q", got)
	}
}

func TestFollowAppendAndPartialLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "WoWCombatLog-1.txt")
	write(t, p, "one\ntwo\n", base)
	f := newF(dir, Options{FromStart: true})
	c := &collector{}
	if err := f.Poll(c.handle); err != nil {
		t.Fatal(err)
	}
	eqLines(t, c.take(), "one", "two")
	appendTo(t, p, "thr", base.Add(time.Second))
	_ = f.Poll(c.handle)
	eqLines(t, c.take()) // partial line withheld
	appendTo(t, p, "ee\r\nfour\npart", base.Add(2*time.Second))
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), "three", "four")
	appendTo(t, p, "ial\n", base.Add(3*time.Second))
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), "partial")
	if f.Current() != p {
		t.Fatal("current")
	}
	_ = f.Poll(c.handle) // nothing new
	eqLines(t, c.take())
}

func TestDefaultTailsLargeExistingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "WoWCombatLog-1.txt")
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("line-0123456789\n") // 16 bytes each
	}
	write(t, p, sb.String(), base)
	f := newF(dir, Options{TailBytes: 100}) // starts mid-file
	c := &collector{}
	_ = f.Poll(c.handle)
	got := c.take()
	if len(got) < 5 || len(got) > 7 {
		t.Fatalf("expected ~6 lines, got %d", len(got))
	}
	for _, l := range got {
		if l != "line-0123456789" {
			t.Fatalf("partial first line leaked: %q", l)
		}
	}
	// small file below TailBytes is read completely
	f2 := newF(dir, Options{TailBytes: 1 << 20})
	_ = f2.Poll(c.handle)
	if n := len(c.take()); n != 100 {
		t.Fatalf("got %d", n)
	}
}

// When the start of a large log is skipped, the zone/map context (MAP_CHANGE, ZONE_CHANGE) from the
// skipped part must still be replayed, in order, before the tail; other skipped lines stay skipped.
func TestSkippedPrefixReplaysMapAndZoneContext(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "WoWCombatLog-1.txt")
	const (
		ts   = "1/1/2026 00:00:00.000-6  "
		zc1  = ts + `ZONE_CHANGE,0,"Dun Morogh",0`
		mc1  = ts + `MAP_CHANGE,1426,"Dun Morogh",-3877.0,-7160.4,1802.0,-3122.9`
		mc2  = ts + `MAP_CHANGE,1455,"Ironforge",-4569.2,-5096.8,-713.5,-1504.2`
		junk = ts + "SWING_DAMAGE,old-skipped-line-padding-padding-padding"
	)
	var sb strings.Builder
	sb.WriteString(junk + "\n" + zc1 + "\n" + mc1 + "\n" + junk + "\n" + mc2 + "\n")
	for i := 0; i < 50; i++ {
		sb.WriteString(junk + "\n")
	}
	tail := ts + "SWING_DAMAGE,tail-line-1\n" + ts + "SWING_DAMAGE,tail-line-2\n"
	sb.WriteString(tail)
	write(t, p, sb.String(), base)

	f := newF(dir, Options{TailBytes: int64(len(tail)) + 20})
	c := &collector{}
	if err := f.Poll(c.handle); err != nil {
		t.Fatal(err)
	}
	got := c.take()
	if len(got) < 5 || got[0] != zc1 || got[1] != mc1 || got[2] != mc2 {
		t.Fatalf("context not replayed first/in order: %q", got)
	}
	for _, l := range got[3:] {
		if strings.Contains(l, "old-skipped") {
			t.Fatalf("skipped non-context line leaked: %q", l)
		}
	}
	if !strings.Contains(got[len(got)-1], "tail-line-2") {
		t.Fatalf("tail missing: %q", got)
	}
	// replay happens once only
	appendTo(t, p, ts+"SWING_DAMAGE,later\n", base.Add(time.Second))
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), ts+"SWING_DAMAGE,later")
}

func TestTailBytesDefault(t *testing.T) {
	f := newF(t.TempDir(), Options{})
	if f.opt.TailBytes != DefaultTailBytes || f.opt.Pattern != DefaultPattern || f.opt.Poll != DefaultPoll {
		t.Fatalf("%+v", f.opt)
	}
}

func TestFromStartReadsWholeFile(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "WoWCombatLog-1.txt"), strings.Repeat("x\n", 1000), base)
	f := newF(dir, Options{FromStart: true, TailBytes: 10})
	c := &collector{}
	_ = f.Poll(c.handle)
	if n := len(c.take()); n != 1000 {
		t.Fatalf("got %d", n)
	}
}

func TestSwitchToNewestFileOnRotation(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "WoWCombatLog-1.txt")
	p2 := filepath.Join(dir, "WoWCombatLog-2.txt")
	write(t, p1, "a1\n", base)
	var switched []string
	f := newF(dir, Options{FromStart: true, OnFile: func(p string) { switched = append(switched, filepath.Base(p)) }})
	c := &collector{}
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), "a1")
	// old file receives a last line and the new file appears in the same interval
	appendTo(t, p1, "a2\n", base.Add(time.Second))
	write(t, p2, "b1\nb2\n", base.Add(2*time.Second))
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), "a2", "b1", "b2") // old file drained, new file read from its start
	if f.Current() != p2 {
		t.Fatal("not switched")
	}
	// writes to the old file are ignored from now on
	appendTo(t, p1, "ignored\n", base.Add(time.Second))
	appendTo(t, p2, "b3\n", base.Add(3*time.Second))
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), "b3")
	if strings.Join(switched, ",") != "WoWCombatLog-1.txt,WoWCombatLog-2.txt" {
		t.Fatalf("switched %v", switched)
	}
}

func TestSwitchBackWhenOlderFileBecomesNewest(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "WoWCombatLog-1.txt")
	p2 := filepath.Join(dir, "WoWCombatLog-2.txt")
	write(t, p1, "a1\n", base)
	write(t, p2, "b1\n", base.Add(time.Second))
	f := newF(dir, Options{FromStart: true})
	c := &collector{}
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), "b1")
	appendTo(t, p1, "a2\n", base.Add(time.Minute)) // file 1 is now the most recently written
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), "a1", "a2") // never followed before: read from its start
}

func TestTruncationRestartsFromBeginning(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "WoWCombatLog-1.txt")
	write(t, p, "aaaa\nbbbb\ncccc\n", base)
	f := newF(dir, Options{FromStart: true})
	c := &collector{}
	_ = f.Poll(c.handle)
	c.take()
	appendTo(t, p, "dd", base.Add(time.Second)) // partial pending
	_ = f.Poll(c.handle)
	write(t, p, "x\n", base.Add(2*time.Second)) // truncated + rewritten, shorter than offset
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), "x") // stale partial "dd" must not be glued onto the new content
}

func TestNoFileYetThenAppears(t *testing.T) {
	dir := t.TempDir()
	f := newF(dir, Options{FromStart: true})
	c := &collector{}
	if err := f.Poll(c.handle); err != nil {
		t.Fatalf("no file is not an error: %v", err)
	}
	write(t, filepath.Join(dir, "WoWCombatLog-1.txt"), "hello\n", base)
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), "hello")
}

func TestMissingDirIsReportedButRecoverable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "later")
	f := newF(dir, Options{FromStart: true})
	if err := f.Poll(func(string) {}); err == nil {
		t.Fatal("expected error for missing dir")
	}
	_ = os.Mkdir(dir, 0o700)
	write(t, filepath.Join(dir, "WoWCombatLog-1.txt"), "ok\n", base)
	c := &collector{}
	if err := f.Poll(c.handle); err != nil {
		t.Fatal(err)
	}
	eqLines(t, c.take(), "ok")
}

func TestFileVanishingIsTolerated(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "WoWCombatLog-1.txt")
	write(t, p, "a\n", base)
	f := newF(dir, Options{FromStart: true})
	c := &collector{}
	_ = f.Poll(c.handle)
	_ = os.Remove(p)
	if err := f.Poll(c.handle); err != nil {
		t.Fatal(err)
	}
	write(t, p, "b\n", base.Add(time.Second))
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), "a", "b") // same name re-created: read afresh
}

func TestRunStopsOnCancelAndFollows(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "WoWCombatLog-1.txt")
	write(t, p, "first\n", base)
	f := newF(dir, Options{FromStart: true, Poll: 5 * time.Millisecond})
	if f.ID() != "src" {
		t.Fatal("id")
	}
	var src LogSource = f
	c := &collector{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, c.handle) }()
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.lines) >= 1 })
	appendTo(t, p, "second\n", base.Add(time.Second))
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.lines) >= 2 })
	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestRunReportsErrorsViaCallback(t *testing.T) {
	f := newF(filepath.Join(t.TempDir(), "missing"), Options{Poll: 5 * time.Millisecond})
	errs := make(chan error, 4)
	f.opt.OnError = func(err error) {
		select {
		case errs <- err:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.Run(ctx, func(string) {}) }()
	select {
	case <-errs:
	case <-time.After(2 * time.Second):
		t.Fatal("no error reported")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestSwitchingBackResumesWithoutDuplicates(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "WoWCombatLog-1.txt")
	p2 := filepath.Join(dir, "WoWCombatLog-2.txt")
	write(t, p1, "a1\n", base)
	f := newF(dir, Options{FromStart: true})
	c := &collector{}
	_ = f.Poll(c.handle)
	write(t, p2, "b1\n", base.Add(time.Second))
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), "a1", "b1")
	appendTo(t, p1, "a2\n", base.Add(time.Minute))
	_ = f.Poll(c.handle)
	eqLines(t, c.take(), "a2")
}

func TestCurrentIsPublishedBeforeLinesAreDelivered(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "WoWCombatLog-1.txt")
	write(t, p, "one\ntwo\n", time.Now())
	f := NewFollower("x", Options{Dir: dir, FromStart: true})
	var seen []string
	if err := f.Poll(func(string) { seen = append(seen, f.Current()) }); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != p || seen[1] != p {
		t.Fatalf("Current() while handling lines: %q", seen)
	}
}
