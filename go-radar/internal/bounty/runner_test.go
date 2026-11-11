package bounty

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLog(t *testing.T, dir, name string, mod time.Time, lines ...string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\r\n")+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

func newRunner(t *testing.T, dir string, st Sink) *Runner {
	t.Helper()
	return NewRunner(RunnerConfig{Dir: dir, Sink: st, Loc: time.UTC})
}

var fight = []string{dmg(1, foe2, vic), dmg(2, foe1, vic), died(3, vic, 0), "not a log line", ""}

func TestListLogsOldestFirstByModTime(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeLog(t, dir, "WoWCombatLog-020126_000000.txt", now.Add(-1*time.Hour))
	writeLog(t, dir, "WoWCombatLog-110126_000000.txt", now.Add(-3*time.Hour))
	writeLog(t, dir, "WoWCombatLog-050126_000000.txt", now.Add(-2*time.Hour))
	writeLog(t, dir, "Other.log", now)
	_ = os.Mkdir(filepath.Join(dir, "WoWCombatLog-dir.txt"), 0o700)
	got, err := ListLogs(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range got {
		names = append(names, filepath.Base(p))
	}
	want := "WoWCombatLog-110126_000000.txt,WoWCombatLog-050126_000000.txt,WoWCombatLog-020126_000000.txt"
	if strings.Join(names, ",") != want {
		t.Fatalf("%v", names)
	}
	if _, err := ListLogs(filepath.Join(dir, "missing"), ""); err == nil {
		t.Fatal("missing dir must error")
	}
}

func TestRunnerAnalyzesAllFilesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeLog(t, dir, "WoWCombatLog-a.txt", now.Add(-2*time.Hour), fight...)
	writeLog(t, dir, "WoWCombatLog-b.txt", now.Add(-1*time.Hour), fight...) // same lines, other file: distinct deaths
	st := New("")
	r := newRunner(t, dir, st)
	id, err := r.Start(nil)
	if err != nil || id == "" {
		t.Fatalf("%q %v", id, err)
	}
	r.Wait()
	s := r.Status()
	if s.Running || s.FilesDone != 2 || s.FilesTotal != 2 || s.Deaths != 2 || s.NewDeaths != 2 || len(s.Errors) != 0 || s.JobID != id {
		t.Fatalf("%+v", s)
	}
	if s.Bytes <= 0 || s.Bytes != s.BytesTotal {
		t.Fatalf("bytes %d/%d", s.Bytes, s.BytesTotal)
	}
	rows := st.Leaderboard(5)
	if len(rows) != 2 || rows[0].GUID != foe1GUID || rows[0].KillingBlows != 2 || rows[1].Assists != 2 {
		t.Fatalf("%+v", rows)
	}
	// re-run: nothing changes
	id2, err := r.Start(nil)
	if err != nil || id2 == id {
		t.Fatalf("new job id expected: %q %v", id2, err)
	}
	r.Wait()
	s = r.Status()
	if s.Deaths != 2 || s.NewDeaths != 0 {
		t.Fatalf("re-analysis must be idempotent: %+v", s)
	}
	if rows2 := st.Leaderboard(5); rows2[0].KillingBlows != 2 || rows2[1].Assists != 2 {
		t.Fatalf("%+v", rows2)
	}
}

func TestRunnerSelectedFilesOnly(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeLog(t, dir, "WoWCombatLog-a.txt", now, fight...)
	writeLog(t, dir, "WoWCombatLog-b.txt", now, fight...)
	st := New("")
	r := newRunner(t, dir, st)
	if _, err := r.Start([]string{"WoWCombatLog-b.txt"}); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if s := r.Status(); s.FilesTotal != 1 || s.Deaths != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestRunnerRejectsBadNames(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	now := time.Now()
	writeLog(t, dir, "WoWCombatLog-a.txt", now, fight...)
	writeLog(t, outside, "WoWCombatLog-evil.txt", now, fight...)
	writeLog(t, dir, "notes.txt", now, fight...)
	st := New("")
	r := newRunner(t, dir, st)
	bad := []string{
		"../" + filepath.Base(outside) + "/WoWCombatLog-evil.txt",
		`..\` + filepath.Base(outside) + `\WoWCombatLog-evil.txt`,
		filepath.Join(outside, "WoWCombatLog-evil.txt"), // absolute
		"sub/WoWCombatLog-a.txt",
		`sub\WoWCombatLog-a.txt`,
		"C:WoWCombatLog-a.txt",
		"..",
		".",
		"",
		"notes.txt",                // not a combat log name
		"WoWCombatLog-missing.txt", // does not exist
		"WoWCombatLog-a.txt\x00.png",
		"WoWCombatLog-*.txt",
	}
	for _, name := range bad {
		_, err := r.Start([]string{name})
		var be *BadFileError
		if !errors.As(err, &be) {
			t.Errorf("%q: want BadFileError, got %v", name, err)
		}
	}
	if s := r.Status(); s.Running || s.JobID != "" {
		t.Fatalf("no job should have started: %+v", s)
	}
	if st.Stats().Deaths != 0 {
		t.Fatal("nothing may be read")
	}
	if _, err := r.Start([]string{"WoWCombatLog-a.txt", "../x"}); err == nil {
		t.Fatal("one bad name rejects the whole request")
	}
	// too many names
	many := make([]string, MaxRequestFiles+1)
	for i := range many {
		many[i] = "WoWCombatLog-a.txt"
	}
	if _, err := r.Start(many); err == nil {
		t.Fatal("too many files")
	}
}

func TestRunnerRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	target := writeLog(t, outside, "secret.txt", time.Now(), fight...)
	if err := os.Symlink(target, filepath.Join(dir, "WoWCombatLog-link.txt")); err != nil {
		t.Skip("symlinks unavailable: ", err)
	}
	r := newRunner(t, dir, New(""))
	if _, err := r.Start([]string{"WoWCombatLog-link.txt"}); err == nil {
		t.Fatal("symlink pointing outside the logs dir must be rejected")
	}
}

func TestRunnerBusy(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "WoWCombatLog-a.txt", time.Now(), fight...)
	release := make(chan struct{})
	r := NewRunner(RunnerConfig{Dir: dir, Sink: New(""), Loc: time.UTC, beforeFile: func() { <-release }})
	if _, err := r.Start(nil); err != nil {
		t.Fatal(err)
	}
	if !r.Status().Running {
		t.Fatal("should be running")
	}
	if _, err := r.Start(nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	close(release)
	r.Wait()
	if r.Status().Running {
		t.Fatal("finished")
	}
	if _, err := r.Start(nil); err != nil { // can start again
		t.Fatal(err)
	}
	r.Wait()
}

func TestRunnerCancelViaContext(t *testing.T) {
	dir := t.TempDir()
	// a big-ish log so cancellation lands mid-file
	lines := make([]string, 0, 60000)
	for i := 0; i < 20000; i++ {
		lines = append(lines, dmg(1, foe1, vic), died(2, vic, 0), pk(3, foe1, vic))
	}
	writeLog(t, dir, "WoWCombatLog-a.txt", time.Now(), lines...)
	writeLog(t, dir, "WoWCombatLog-b.txt", time.Now(), lines...)
	ctx, cancel := context.WithCancel(context.Background())
	r := NewRunner(RunnerConfig{Dir: dir, Sink: New(""), Loc: time.UTC, Context: ctx})
	cancel() // already cancelled before the job starts reading
	if _, err := r.Start(nil); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	s := r.Status()
	if s.Running || s.FilesDone >= 2 || !s.Cancelled {
		t.Fatalf("%+v", s)
	}
}

func TestRunnerReportsUnreadableFileAndContinues(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	a := writeLog(t, dir, "WoWCombatLog-a.txt", now.Add(-time.Hour), fight...)
	writeLog(t, dir, "WoWCombatLog-b.txt", now, fight...)
	st := New("")
	r := newRunner(t, dir, st)
	// file vanishes between listing and reading
	r2 := NewRunner(RunnerConfig{Dir: dir, Sink: st, Loc: time.UTC, beforeFile: func() { _ = os.Remove(a) }})
	if _, err := r2.Start(nil); err != nil {
		t.Fatal(err)
	}
	r2.Wait()
	s := r2.Status()
	if len(s.Errors) != 1 || s.FilesDone != 2 || s.Deaths != 1 {
		t.Fatalf("%+v", s)
	}
	_ = r
}

func TestAnalyzeFileToleratesHugeLinesAndBadBytes(t *testing.T) {
	dir := t.TempDir()
	huge := "11/10/2026 16:00:00.000+0  " + strings.Repeat("x", 3<<20) // 3 MB line, over the line limit
	p := writeLog(t, dir, "WoWCombatLog-a.txt", time.Now(), huge, "\xff\xfe\x00 binary junk", dmg(1, foe1, vic), died(2, vic, 0))
	st := New("")
	an := NewAnalyzer(st, Options{})
	an.SetFile("WoWCombatLog-a.txt")
	var lastBytes int64
	n, err := AnalyzeFile(context.Background(), p, an, time.UTC, func(b int64) { lastBytes = b })
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || st.Stats().Deaths != 1 {
		t.Fatalf("lines=%d %+v", n, st.Stats())
	}
	fi, _ := os.Stat(p)
	if lastBytes != fi.Size() {
		t.Fatalf("progress %d of %d", lastBytes, fi.Size())
	}
	if _, err := AnalyzeFile(context.Background(), filepath.Join(dir, "none"), an, time.UTC, nil); err == nil {
		t.Fatal("missing file")
	}
}

func TestAnalyzeFileFinalLineWithoutNewline(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "WoWCombatLog-a.txt")
	_ = os.WriteFile(p, []byte(dmg(1, foe1, vic)+"\n"+died(2, vic, 0)), 0o600)
	st := New("")
	an := NewAnalyzer(st, Options{})
	an.SetFile("a")
	if _, err := AnalyzeFile(context.Background(), p, an, time.UTC, nil); err != nil {
		t.Fatal(err)
	}
	if st.Stats().Deaths != 1 {
		t.Fatal("last line without newline lost")
	}
}

func TestRunnerStartPathsAllowsExplicitFiles(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	p := writeLog(t, other, "custom-name.txt", time.Now(), fight...)
	st := New("")
	r := newRunner(t, dir, st)
	if _, err := r.StartPaths([]string{p}); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if s := r.Status(); s.FilesTotal != 1 || s.Deaths != 1 || len(s.Errors) != 0 {
		t.Fatalf("%+v", s)
	}
	if _, err := r.StartPaths(nil); err == nil {
		t.Fatal("empty list")
	}
}

func TestRunnerEmptyDirIsNotAnError(t *testing.T) {
	r := newRunner(t, t.TempDir(), New(""))
	if _, err := r.Start(nil); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if s := r.Status(); s.FilesTotal != 0 || s.Running {
		t.Fatalf("%+v", s)
	}
	r2 := newRunner(t, filepath.Join(t.TempDir(), "missing"), New(""))
	if _, err := r2.Start(nil); err == nil {
		t.Fatal("missing logs dir should fail the request")
	}
}

func TestRunnerUsesRealmSuffixFunc(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "WoWCombatLog-a.txt", time.Now(), fight...)
	st := New("")
	r := NewRunner(RunnerConfig{Dir: dir, Sink: st, Loc: time.UTC, RealmSuffix: func() string { return "-Realm-PvP-US" }})
	_, _ = r.Start(nil)
	r.Wait()
	if rows := st.Leaderboard(1); rows[0].Name != "Fone" {
		t.Fatalf("%+v", rows)
	}
}
