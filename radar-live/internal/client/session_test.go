package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/feed"
)

const testPoll = 5 * time.Millisecond

// fakeUploader drains the queue like the real streamer would and lets a test report events.
type fakeUploader struct {
	cfg    feed.StreamerConfig
	mu     sync.Mutex
	lines  []string
	runErr error
	done   chan struct{}
}

func (u *fakeUploader) Run(ctx context.Context, q *feed.Queue) error {
	defer close(u.done)
	if u.runErr != nil {
		return u.runErr
	}
	for {
		line, err := q.Pop(ctx)
		if err != nil {
			return err
		}
		u.mu.Lock()
		u.lines = append(u.lines, line)
		u.mu.Unlock()
	}
}

func (u *fakeUploader) got() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.lines...)
}

func (u *fakeUploader) emit(e feed.Event) { u.cfg.Observe(e) }

type sessionRig struct {
	session  *Session
	clock    *fakeClock
	uploader *fakeUploader
}

func newRig(t *testing.T, runErr error) *sessionRig {
	t.Helper()
	rig := &sessionRig{clock: newFakeClock()}
	rig.session = NewSession(SessionOptions{
		Now:  rig.clock.Now,
		Poll: testPoll,
		NewUploader: func(cfg feed.StreamerConfig) Uploader {
			rig.uploader = &fakeUploader{cfg: cfg, runErr: runErr, done: make(chan struct{})}
			return rig.uploader
		},
	})
	t.Cleanup(func() { rig.session.Stop() })
	return rig
}

func writeLog(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendLog(t *testing.T, path, text string) {
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

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (r *sessionRig) start(t *testing.T, s Settings) {
	t.Helper()
	s.Server = "http://127.0.0.1:1"
	if err := r.session.Start(StartConfig{Settings: s, Key: fakeKey, Source: "test-pc"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the log to be opened", func() bool { return r.session.Status().File != "" })
}

func (r *sessionRig) waitLines(t *testing.T, want ...string) {
	t.Helper()
	eventually(t, "uploaded lines "+strings.Join(want, "|"), func() bool {
		return strings.Join(r.uploader.got(), "|") == strings.Join(want, "|")
	})
}

func TestSessionStartsAtTheEndOfTheFile(t *testing.T) {
	rig := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "WoWCombatLog.txt")
	writeLog(t, path, "old 1\nold 2\n")
	rig.start(t, Settings{LogPath: path})
	appendLog(t, path, "new 1\nnew 2\n")
	rig.waitLines(t, "new 1", "new 2")
}

func TestSessionIncludeExistingSendsTheWholeFileFirst(t *testing.T) {
	rig := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "WoWCombatLog.txt")
	writeLog(t, path, "old 1\nold 2\n")
	rig.start(t, Settings{LogPath: path, IncludeExisting: true})
	appendLog(t, path, "new 1\n")
	rig.waitLines(t, "old 1", "old 2", "new 1")
}

func TestSessionHoldsBackAPartialLine(t *testing.T) {
	rig := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "WoWCombatLog.txt")
	writeLog(t, path, "")
	rig.start(t, Settings{LogPath: path})
	appendLog(t, path, "first\nsecond-par")
	rig.waitLines(t, "first")
	time.Sleep(10 * testPoll)
	if got := rig.uploader.got(); len(got) != 1 {
		t.Fatalf("partial line was sent: %v", got)
	}
	appendLog(t, path, "t\n")
	rig.waitLines(t, "first", "second-part")
}

func TestSessionSurvivesTruncation(t *testing.T) {
	rig := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "WoWCombatLog.txt")
	writeLog(t, path, "a long line that will vanish\n")
	rig.start(t, Settings{LogPath: path})
	appendLog(t, path, "before\n")
	rig.waitLines(t, "before")
	writeLog(t, path, "after\n")
	rig.waitLines(t, "before", "after")
}

func TestSessionFollowsTheNewestLogInAFolder(t *testing.T) {
	rig := newRig(t, nil)
	dir := t.TempDir()
	first := filepath.Join(dir, "WoWCombatLog-1.txt")
	writeLog(t, first, "old\n")
	rig.start(t, Settings{LogPath: dir, FollowNewest: true})
	appendLog(t, first, "one\n")
	rig.waitLines(t, "one")

	second := filepath.Join(dir, "WoWCombatLog-2.txt")
	writeLog(t, second, "two\n")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(second, later, later); err != nil {
		t.Fatal(err)
	}
	rig.waitLines(t, "one", "two")
	if got := filepath.Base(rig.session.Status().File); got != "WoWCombatLog-2.txt" {
		t.Fatalf("following %q", got)
	}
}

func TestSessionFollowNewestAcceptsAFileInsideTheFolder(t *testing.T) {
	rig := newRig(t, nil)
	dir := t.TempDir()
	file := filepath.Join(dir, "WoWCombatLog-1.txt")
	writeLog(t, file, "")
	rig.start(t, Settings{LogPath: file, FollowNewest: true})
	appendLog(t, file, "x\n")
	rig.waitLines(t, "x")
}

func TestSessionFollowsOneFileWhoseNameHasGlobCharacters(t *testing.T) {
	rig := newRig(t, nil)
	dir := t.TempDir()
	odd := filepath.Join(dir, "my [log] file.txt")
	writeLog(t, odd, "")
	other := filepath.Join(dir, "my l file.txt")
	writeLog(t, other, "")
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(other, later, later) // newer, but not the chosen file
	rig.start(t, Settings{LogPath: odd})
	appendLog(t, other, "wrong\n")
	appendLog(t, odd, "right\n")
	rig.waitLines(t, "right")
}

func TestSessionMissingFolderIsANoteNotACrash(t *testing.T) {
	rig := newRig(t, nil)
	missing := filepath.Join(t.TempDir(), "nope")
	if err := rig.session.Start(StartConfig{Settings: Settings{Server: "http://127.0.0.1:1", LogPath: missing, FollowNewest: true}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a note about the folder", func() bool { return rig.session.Status().Note != "" })
	if st := rig.session.Status(); st.State != StateWaiting {
		t.Fatalf("state = %s", st.State)
	}
}

func TestSessionPassesConfigurationToTheUploader(t *testing.T) {
	rig := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "log.txt")
	writeLog(t, path, "")
	rig.start(t, Settings{LogPath: path, Ruleset: "RP"})
	cfg := rig.uploader.cfg
	if cfg.URL != "http://127.0.0.1:1" || cfg.Key != fakeKey || cfg.Source != "test-pc" || cfg.Ruleset != "rp" || cfg.MaxBytesPerSec <= 0 {
		t.Fatalf("uploader config = %+v", cfg)
	}
}

func TestSessionStatusFollowsUploaderEvents(t *testing.T) {
	rig := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "log.txt")
	writeLog(t, path, "")
	rig.start(t, Settings{LogPath: path})
	if st := rig.session.Status(); st.State != StateWaiting || !st.Running {
		t.Fatalf("after start: %+v", st)
	}
	rig.uploader.emit(feed.Event{Kind: feed.EventConnecting})
	rig.uploader.emit(feed.Event{Kind: feed.EventSent, Lines: 10, Bytes: 1000})
	if st := rig.session.Status(); st.State != StateUploading || st.LinesSent != 10 {
		t.Fatalf("uploading: %+v", st)
	}
	rig.uploader.emit(feed.Event{Kind: feed.EventEnded, Status: 503, Err: errors.New("x")})
	rig.uploader.emit(feed.Event{Kind: feed.EventBackoff, Wait: 8 * time.Second})
	st := rig.session.Status()
	if st.State != StateError || st.RetryInSec != 8 || !strings.Contains(st.Error, "503") {
		t.Fatalf("error: %+v", st)
	}
	rig.uploader.emit(feed.Event{Kind: feed.EventConnecting})
	rig.uploader.emit(feed.Event{Kind: feed.EventSent, Lines: 1, Bytes: 10})
	if st := rig.session.Status(); st.State != StateUploading || st.Error != "" || st.LinesSent != 11 {
		t.Fatalf("recovered: %+v", st)
	}
}

func TestSessionQueuedLinesAppearInStatus(t *testing.T) {
	rig := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "log.txt")
	writeLog(t, path, "")
	rig.start(t, Settings{LogPath: path})
	appendLog(t, path, "a\nb\n")
	rig.waitLines(t, "a", "b") // drained by the fake, so nothing stays queued
	if q := rig.session.Status().Queued; q != 0 {
		t.Fatalf("queued = %d", q)
	}
}

func TestSessionStopGoesIdleAndStopsTheUploader(t *testing.T) {
	rig := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "log.txt")
	writeLog(t, path, "")
	rig.start(t, Settings{LogPath: path})
	rig.uploader.emit(feed.Event{Kind: feed.EventSent, Lines: 2, Bytes: 20})
	st := rig.session.Stop()
	if st.State != StateIdle || st.Running || st.LinesSent != 2 {
		t.Fatalf("after stop: %+v", st)
	}
	select {
	case <-rig.uploader.done:
	default:
		t.Fatal("uploader still running after Stop")
	}
	if again := rig.session.Stop(); again.State != StateIdle {
		t.Fatalf("second stop: %+v", again)
	}
}

func TestSessionRefusesASecondStart(t *testing.T) {
	rig := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "log.txt")
	writeLog(t, path, "")
	rig.start(t, Settings{LogPath: path})
	err := rig.session.Start(StartConfig{Settings: Settings{Server: "http://127.0.0.1:1", LogPath: path}})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v", err)
	}
}

func TestSessionStartValidatesSettings(t *testing.T) {
	rig := newRig(t, nil)
	if err := rig.session.Start(StartConfig{Settings: Settings{Server: "http://127.0.0.1:1"}}); err == nil {
		t.Fatal("start without a log path succeeded")
	}
	if err := rig.session.Start(StartConfig{Settings: Settings{Server: "nope", LogPath: "x"}}); err == nil {
		t.Fatal("start with a bad server succeeded")
	}
	if st := rig.session.Status(); st.Running {
		t.Fatalf("session running after failed starts: %+v", st)
	}
}

func TestSessionUploaderFailureBecomesAnError(t *testing.T) {
	rig := newRig(t, errors.New("ingest url is unusable"))
	path := filepath.Join(t.TempDir(), "log.txt")
	writeLog(t, path, "")
	rig.start(t, Settings{LogPath: path})
	eventually(t, "error state", func() bool { return rig.session.Status().State == StateError })
	if st := rig.session.Status(); !strings.Contains(st.Error, "unusable") {
		t.Fatalf("status = %+v", st)
	}
}

func TestSessionCanBeRestartedWithFreshCounters(t *testing.T) {
	rig := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "log.txt")
	writeLog(t, path, "")
	rig.start(t, Settings{LogPath: path})
	rig.uploader.emit(feed.Event{Kind: feed.EventSent, Lines: 4, Bytes: 40})
	rig.session.Stop()
	rig.start(t, Settings{LogPath: path})
	if st := rig.session.Status(); st.LinesSent != 0 || !st.Running {
		t.Fatalf("status = %+v", st)
	}
}

// With the real streamer: a wrong key is explained, and the key is nowhere in the status.
func TestSessionWithRealStreamerExplainsARejectedKey(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Ingest-Key")
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	session := NewSession(SessionOptions{Poll: testPoll})
	defer session.Stop()
	path := filepath.Join(t.TempDir(), "log.txt")
	writeLog(t, path, "")
	err := session.Start(StartConfig{Settings: Settings{Server: srv.URL, LogPath: path}, Key: fakeKey, Source: "pc"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "log opened", func() bool { return session.Status().File != "" })
	appendLog(t, path, "line\n")
	eventually(t, "401 explained", func() bool { return strings.Contains(session.Status().Error, "rejected the ingest key") })
	if gotKey != fakeKey {
		t.Fatalf("server saw key %q", gotKey)
	}
	data, _ := json.Marshal(session.Status())
	if strings.Contains(string(data), fakeKey) {
		t.Fatalf("status leaks the key: %s", data)
	}
}

const authMessage = "The server rejected the ingest key (401). The upload stopped; fix the key and click Start again."

// A 401 stops the upload for good (one request, no retry loop), shows the error state, and
// Start works again without restarting the app.
func TestSessionStopsOnA401AndCanBeStartedAgain(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	count := func() int { mu.Lock(); defer mu.Unlock(); return hits }
	session := NewSession(SessionOptions{Poll: testPoll})
	defer session.Stop()
	path := filepath.Join(t.TempDir(), "log.txt")
	writeLog(t, path, "")
	cfg := StartConfig{Settings: Settings{Server: srv.URL, LogPath: path}, Key: " " + fakeKey + "\n", Source: "pc"}
	if err := session.Start(cfg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "log opened", func() bool { return session.Status().File != "" })
	appendLog(t, path, "line\n")
	eventually(t, "auth error", func() bool { return session.Status().Error == authMessage })
	st := session.Status()
	if st.State != StateError || st.Running || st.RetryInSec != 0 {
		t.Fatalf("status = %+v", st)
	}
	time.Sleep(100 * time.Millisecond)
	if count() != 1 {
		t.Fatalf("server was contacted %d times, want 1", count())
	}
	if err := session.Start(cfg); err != nil {
		t.Fatalf("Start after a 401: %v", err)
	}
	eventually(t, "log reopened", func() bool { return session.Status().File != "" })
	appendLog(t, path, "line two\n")
	eventually(t, "second attempt", func() bool { return count() == 2 && session.Status().Error == authMessage })
	if st := session.Status(); st.State != StateError || st.Running {
		t.Fatalf("after the second 401: %+v", st)
	}
}

func TestSessionTrimsTheKeyItIsGiven(t *testing.T) {
	rig := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "WoWCombatLog.txt")
	writeLog(t, path, "")
	s := Settings{LogPath: path, Server: "http://127.0.0.1:1"}
	if err := rig.session.Start(StartConfig{Settings: s, Key: "\t" + fakeKey + " \r\n", Source: "pc"}); err != nil {
		t.Fatal(err)
	}
	if rig.uploader.cfg.Key != fakeKey {
		t.Fatalf("uploader got key %q", rig.uploader.cfg.Key)
	}
}

func TestEscapeGlob(t *testing.T) {
	tests := map[string]string{"plain.txt": "plain.txt", "a[1].txt": "a[[]1].txt", "a*b?": "a[*]b[?]"}
	for in, want := range tests {
		if got := escapeGlob(in); got != want {
			t.Errorf("escapeGlob(%q) = %q, want %q", in, got, want)
		}
		if ok, err := filepath.Match(escapeGlob(in), in); err != nil || !ok {
			t.Errorf("escaped pattern for %q does not match it: %v %v", in, ok, err)
		}
	}
}
