package tailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, text string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

type collector struct{ lines []string }

func (c *collector) handle(l string) { c.lines = append(c.lines, l) }

func (c *collector) take() []string {
	out := c.lines
	c.lines = nil
	return out
}

func poll(t *testing.T, f *Follower, c *collector) []string {
	t.Helper()
	if err := f.Poll(c.handle); err != nil {
		t.Fatal(err)
	}
	return c.take()
}

// base is in the future so that files touched by appendTo (mtime = now) never outrank the ones given base+N.
var base = time.Now().Add(time.Hour).Truncate(time.Second)

func TestNewestPicksLatestMatchingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Newest(dir, DefaultPattern); !errors.Is(err, ErrNoLog) {
		t.Fatalf("empty dir: %v", err)
	}
	write(t, filepath.Join(dir, "WoWCombatLog-1.txt"), "a\n", base)
	write(t, filepath.Join(dir, "WoWCombatLog-2.txt"), "b\n", base.Add(time.Hour))
	write(t, filepath.Join(dir, "other.txt"), "c\n", base.Add(2*time.Hour))
	if err := os.Mkdir(filepath.Join(dir, "WoWCombatLog-dir.txt"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := Newest(dir, DefaultPattern)
	if err != nil || filepath.Base(got) != "WoWCombatLog-2.txt" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestNewestTieBrokenByName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "WoWCombatLog-a.txt"), "x", base)
	write(t, filepath.Join(dir, "WoWCombatLog-b.txt"), "x", base)
	got, _ := Newest(dir, DefaultPattern)
	if filepath.Base(got) != "WoWCombatLog-b.txt" {
		t.Fatalf("got %q", got)
	}
}

func TestNewestMissingDir(t *testing.T) {
	if _, err := Newest(filepath.Join(t.TempDir(), "nope"), DefaultPattern); err == nil || errors.Is(err, ErrNoLog) {
		t.Fatalf("want a read error, got %v", err)
	}
}

func TestStartsAtEndAndReplaysContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WoWCombatLog-1.txt")
	zone1 := `11/5/2026 20:00:00.100-5  ZONE_CHANGE,1,"One",0`
	zone2 := `11/5/2026 20:00:02.100-5  ZONE_CHANGE,2,"Two",0`
	mapc := `11/5/2026 20:00:02.200-5  MAP_CHANGE,2,"Two",1,2,3,4`
	write(t, path, zone1+"\nold line\n"+zone2+"\n"+mapc+"\nmore old\n", base)
	f := NewFollower(Options{Dir: dir})
	c := &collector{}
	got := poll(t, f, c)
	if want := []string{zone2, mapc}; !reflect.DeepEqual(got, want) {
		t.Fatalf("context replay = %q, want %q", got, want)
	}
	appendTo(t, path, "new 1\nnew 2\n")
	if got := poll(t, f, c); !reflect.DeepEqual(got, []string{"new 1", "new 2"}) {
		t.Fatalf("new lines = %q", got)
	}
	if poll(t, f, c) != nil {
		t.Fatal("nothing new expected")
	}
}

func TestStartAtEndOfEmptyFileHasNoContext(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "WoWCombatLog-1.txt"), "", base)
	f := NewFollower(Options{Dir: dir})
	if got := poll(t, f, &collector{}); got != nil {
		t.Fatalf("got %q", got)
	}
}

func TestFromStartReadsWholeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WoWCombatLog-1.txt")
	write(t, path, "a\nb\r\nc\n", base)
	f := NewFollower(Options{Dir: dir, FromStart: true})
	if got := poll(t, f, &collector{}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("got %q", got)
	}
}

func TestPartialLinesWaitForNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WoWCombatLog-1.txt")
	write(t, path, "", base)
	f := NewFollower(Options{Dir: dir})
	c := &collector{}
	poll(t, f, c)
	appendTo(t, path, "half")
	if got := poll(t, f, c); got != nil {
		t.Fatalf("partial line delivered: %q", got)
	}
	appendTo(t, path, " done\nnext")
	if got := poll(t, f, c); !reflect.DeepEqual(got, []string{"half done"}) {
		t.Fatalf("got %q", got)
	}
	appendTo(t, path, "\n")
	if got := poll(t, f, c); !reflect.DeepEqual(got, []string{"next"}) {
		t.Fatalf("got %q", got)
	}
}

func TestTruncationRestartsFromTop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WoWCombatLog-1.txt")
	write(t, path, "", base)
	f := NewFollower(Options{Dir: dir})
	c := &collector{}
	poll(t, f, c)
	appendTo(t, path, "long line number one\nlong line number two\n")
	poll(t, f, c)
	write(t, path, "fresh\n", base)
	if got := poll(t, f, c); !reflect.DeepEqual(got, []string{"fresh"}) {
		t.Fatalf("after truncation: %q", got)
	}
}

func TestRotationFinishesOldFileThenReadsNewFromStart(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "WoWCombatLog-1.txt")
	write(t, first, "", base)
	var opened []string
	f := NewFollower(Options{Dir: dir, OnFile: func(p string) { opened = append(opened, filepath.Base(p)) }})
	c := &collector{}
	poll(t, f, c)
	appendTo(t, first, "tail of first\ndangling")
	second := filepath.Join(dir, "WoWCombatLog-2.txt")
	write(t, second, "s1\ns2\n", base.Add(time.Hour))
	got := poll(t, f, c)
	if want := []string{"tail of first", "s1", "s2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if want := []string{"WoWCombatLog-1.txt", "WoWCombatLog-2.txt"}; !reflect.DeepEqual(opened, want) {
		t.Fatalf("opened %q", opened)
	}
	if filepath.Base(f.Current()) != "WoWCombatLog-2.txt" {
		t.Fatalf("current = %q", f.Current())
	}
}

func TestNoFileThenFileAppears(t *testing.T) {
	dir := t.TempDir()
	f := NewFollower(Options{Dir: dir})
	c := &collector{}
	if got := poll(t, f, c); got != nil || f.Current() != "" {
		t.Fatalf("got %q current %q", got, f.Current())
	}
	write(t, filepath.Join(dir, "WoWCombatLog-1.txt"), "early\n", base)
	// Only a file that existed on the very first poll is entered at its end; a log created
	// later is new content and is read from the top.
	if got := poll(t, f, c); !reflect.DeepEqual(got, []string{"early"}) {
		t.Fatalf("got %q", got)
	}
}

func TestReturningToEarlierFileDoesNotRepeatLines(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "WoWCombatLog-1.txt")
	b := filepath.Join(dir, "WoWCombatLog-2.txt")
	write(t, a, "", base)
	f := NewFollower(Options{Dir: dir})
	c := &collector{}
	poll(t, f, c)
	appendTo(t, a, "a1\n")
	write(t, b, "b1\n", base.Add(time.Hour))
	poll(t, f, c) // a1, b1
	appendTo(t, a, "a2\n")
	if err := os.Chtimes(a, base.Add(2*time.Hour), base.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := poll(t, f, c); !reflect.DeepEqual(got, []string{"a2"}) {
		t.Fatalf("got %q", got)
	}
}

func TestRunReportsErrorsAndStopsOnCancel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	errs := make(chan error, 4)
	f := NewFollower(Options{Dir: dir, Poll: time.Millisecond, OnError: func(err error) {
		select {
		case errs <- err:
		default:
		}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx, func(string) {}) }()
	select {
	case <-errs:
	case <-time.After(5 * time.Second):
		t.Fatal("no error reported")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
}

func TestRunDeliversLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WoWCombatLog-1.txt")
	write(t, path, "", base)
	f := NewFollower(Options{Dir: dir, Poll: time.Millisecond})
	got := make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.Run(ctx, func(l string) { got <- l }) }()
	time.Sleep(50 * time.Millisecond)
	appendTo(t, path, "hello\n")
	select {
	case l := <-got:
		if l != "hello" {
			t.Fatalf("got %q", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("line not delivered")
	}
}

func TestLongFileCrossesChunkBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WoWCombatLog-1.txt")
	line := strings.Repeat("x", 1000)
	var sb strings.Builder
	const count = 3000 // about 3 MB, several chunks
	for i := 0; i < count; i++ {
		sb.WriteString(line + "\n")
	}
	write(t, path, sb.String(), base)
	f := NewFollower(Options{Dir: dir, FromStart: true})
	got := poll(t, f, &collector{})
	if len(got) != count || got[count-1] != line {
		t.Fatalf("got %d lines", len(got))
	}
}
