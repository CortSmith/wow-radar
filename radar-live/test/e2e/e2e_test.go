// Package e2e is the whole-system smoke test: it builds the real radar-db, radar-ingest and
// radar-feed binaries, starts them on free ports with temporary directories, appends a synthetic
// combat log for the feeder to follow, and checks what the read API serves.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	buildTimeout = 5 * time.Minute
	waitTimeout  = 20 * time.Second
	sessionRel   = "internal/mod/logparse/testdata/session.log"
	wantContacts = 3 // Ann, Cat and Dov are the players in the fixture
)

// lockedBuffer collects a process's output safely.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// repoRoot is the radar-live directory (this file lives in test/e2e).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("not the repository root: %v", err)
	}
	return root
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// buildBinaries compiles the three commands into dir with the same flags as the release build.
func buildBinaries(t *testing.T, root, dir string) {
	t.Helper()
	buildNamed(t, root, dir, "radar-db", "radar-ingest", "radar-feed")
}

// buildNamed compiles the named commands into dir.
func buildNamed(t *testing.T, root, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
		cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-s -w", "-o", filepath.Join(dir, exe(name)), "./cmd/"+name)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("go build %s: %v\n%s", name, err, out)
		}
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// proc is a started binary.
type proc struct {
	cmd *exec.Cmd
	out *lockedBuffer
}

func start(t *testing.T, bin string, env []string, args ...string) *proc {
	t.Helper()
	p := &proc{cmd: exec.Command(bin, args...), out: &lockedBuffer{}}
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
	p.cmd.Env = append(os.Environ(), env...)
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", filepath.Base(bin), err)
	}
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		if t.Failed() {
			t.Logf("--- %s output ---\n%s", filepath.Base(bin), p.out.String())
		}
	})
	return p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func healthy(url string) bool {
	resp, err := http.Get(url + "/healthz")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func getJSON(t *testing.T, url, header, key string, into any) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set(header, key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if into != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(data, into); err != nil {
			t.Fatalf("%s: %v: %s", url, err, data)
		}
	}
	return resp.StatusCode
}

var stampRE = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{4}) (\d{1,2}):(\d{2}):(\d{2})\.(\d{3})(?:-\d+)?  `)

// rewriteStamps moves the log's timestamps so that the last parseable line is "now", keeping the
// gaps between lines. Rewritten lines carry an explicit UTC offset; lines that do not start
// with a valid timestamp (the fixture's garbage lines) are left alone.
func rewriteStamps(session string, now time.Time) string {
	type stamped struct {
		face time.Time
		rest string
	}
	lines := strings.Split(strings.TrimRight(session, "\n"), "\n")
	parsed := make([]*stamped, len(lines))
	var last time.Time
	for i, l := range lines {
		g := stampRE.FindStringSubmatch(l)
		if g == nil {
			continue
		}
		n := func(k int) int { v, _ := strconv.Atoi(g[k]); return v }
		if n(1) < 1 || n(1) > 12 {
			continue
		}
		face := time.Date(n(3), time.Month(n(1)), n(2), n(4), n(5), n(6), n(7)*int(time.Millisecond), time.UTC)
		parsed[i] = &stamped{face: face, rest: l[len(g[0]):]}
		last = face
	}
	shift := now.Add(-time.Second).Sub(last)
	var sb strings.Builder
	for i, l := range lines {
		if p := parsed[i]; p != nil {
			l = p.face.Add(shift).UTC().Format("1/2/2006 15:04:05.000") + "+0  " + p.rest
		}
		sb.WriteString(l + "\n")
	}
	return sb.String()
}

func TestRewriteStamps(t *testing.T) {
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	in := "11/5/2026 20:00:00.000-5  A,1\nnot a log line\n99/5/2026 20:00:12.000-5  B\n11/5/2026 20:00:10.500  C,2\n"
	want := []string{
		"1/2/2030 03:03:53.500+0  A,1",
		"not a log line",
		"99/5/2026 20:00:12.000-5  B",
		"1/2/2030 03:04:04.000+0  C,2",
	}
	got := strings.Split(strings.TrimRight(rewriteStamps(in, now), "\n"), "\n")
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// rssKB reads a process's resident set from /proc (Linux only).
func rssKB(pid int) (int64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	for _, l := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(l, "VmRSS:"); ok {
			kb, err := strconv.ParseInt(strings.Fields(rest)[0], 10, 64)
			return kb, err == nil
		}
	}
	return 0, false
}

type contactRow struct {
	GUID string `json:"guid"`
	Name string `json:"name"`
}

func TestFeederToPageEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds three binaries; skipped with -short")
	}
	root := repoRoot(t)
	bin := t.TempDir()
	buildBinaries(t, root, bin)

	keygen := func() string {
		out, err := exec.Command(filepath.Join(bin, exe("radar-db")), "keygen").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	ingestKey, viewKey := keygen(), keygen()
	dbAddr, dbInternalAddr, ingestAddr := freeAddr(t), freeAddr(t), freeAddr(t)
	data := t.TempDir()
	logs := t.TempDir()

	db := start(t, filepath.Join(bin, exe("radar-db")), []string{"RADAR_INGEST_KEYS=" + ingestKey, "RADAR_VIEW_KEYS=" + viewKey},
		"--listen", dbAddr, "--internal-listen", dbInternalAddr, "--db-path", filepath.Join(data, "radar-live.db"),
	)
	waitFor(t, "radar-db", func() bool { return healthy("http://" + dbAddr) })

	ingest := start(t, filepath.Join(bin, exe("radar-ingest")), []string{"RADAR_DB_KEY=" + ingestKey, "RADAR_INGEST_KEYS=" + ingestKey},
		"--listen", ingestAddr, "--db-url", "http://"+dbInternalAddr,
		"--contactx-flush", "200ms", "--bountyx-flush", "200ms",
		"--logparse-allow-replay", "true") // the fixture log is old
	waitFor(t, "radar-ingest", func() bool { return healthy("http://" + ingestAddr) })

	logPath := filepath.Join(logs, "WoWCombatLog-110526_200000.txt")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	feeder := start(t, filepath.Join(bin, exe("radar-feed")), []string{"RADAR_FEED_KEY=" + ingestKey},
		"--logs", logs, "--ingest-url", "http://"+ingestAddr, "--source", "e2e", "--poll", "50ms", "--max-bytes-per-sec", "0")
	// The feeder starts at the end of the log, so the lines must be appended after it opened the file.
	waitFor(t, "feeder to open the log", func() bool { return strings.Contains(feeder.out.String(), "following log") })

	session, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(sessionRel)))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(rewriteStamps(string(session), time.Now())); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	var sweep struct {
		Now      int64        `json:"now"`
		Contacts []contactRow `json:"contacts"`
	}
	sweepURL := "http://" + dbAddr + "/api/v1/sweep?rs=pvp&scope=all"
	waitFor(t, "contacts in the sweep", func() bool {
		sweep.Contacts = nil
		return getJSON(t, sweepURL, "X-View-Key", viewKey, &sweep) == http.StatusOK && len(sweep.Contacts) >= wantContacts
	})
	guids := map[string]bool{}
	for _, c := range sweep.Contacts {
		guids[c.GUID] = true
	}
	for _, g := range []string{"Player-1-0A", "Player-1-0B", "Player-1-0C"} {
		if !guids[g] {
			t.Errorf("sweep lacks %s: %+v", g, sweep.Contacts)
		}
	}

	var status struct {
		Rulesets []struct {
			ID       string `json:"id"`
			Contacts int    `json:"contacts"`
		} `json:"rulesets"`
	}
	if code := getJSON(t, "http://"+dbAddr+"/api/v1/status", "X-View-Key", viewKey, &status); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	pvp := 0
	for _, r := range status.Rulesets {
		if r.ID == "pvp" {
			pvp = r.Contacts
		}
	}
	if pvp < wantContacts {
		t.Errorf("status pvp contacts = %d, want >= %d (%+v)", pvp, wantContacts, status.Rulesets)
	}

	// The keys are enforced end to end.
	if code := getJSON(t, sweepURL, "", "", nil); code != http.StatusUnauthorized {
		t.Errorf("sweep without a key: %d, want 401", code)
	}
	if code := getJSON(t, sweepURL, "X-View-Key", ingestKey, nil); code != http.StatusUnauthorized {
		t.Errorf("sweep with an ingest key: %d, want 401", code)
	}

	// The page itself is served.
	resp, err := http.Get("http://" + dbAddr + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Contains(bytes.ToLower(page), []byte("<html")) {
		t.Errorf("page: %d %.80s", resp.StatusCode, page)
	}

	assertListenerSplit(t, dbAddr, dbInternalAddr, ingestKey, viewKey)

	for name, p := range map[string]*proc{"radar-db": db, "radar-ingest": ingest, "radar-feed": feeder} {
		if kb, ok := rssKB(p.cmd.Process.Pid); ok {
			t.Logf("RSS after load: %s %d KiB", name, kb)
		} else {
			t.Logf("RSS after load: %s not measured on %s", name, runtime.GOOS)
		}
	}
}

// assertListenerSplit proves the write API exists only on the internal listener and the page and
// read API only on the public one.
func assertListenerSplit(t *testing.T, publicAddr, internalAddr, ingestKey, viewKey string) {
	t.Helper()
	status := func(method, addr, path string, headers ...string) int {
		req, err := http.NewRequest(method, "http://"+addr+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := status("GET", publicAddr, "/internal/v1/ping"); code != http.StatusNotFound {
		t.Errorf("public GET /internal/v1/ping = %d, want 404", code)
	}
	if code := status("POST", publicAddr, "/internal/v1/ping", "X-Ingest-Key", ingestKey); code >= 200 && code < 300 {
		t.Errorf("public POST /internal/v1/ping = %d, must not succeed", code)
	}
	if code := status("POST", internalAddr, "/internal/v1/ping", "X-Ingest-Key", ingestKey); code != http.StatusBadRequest {
		t.Errorf("internal ping with an empty body = %d, want 400 (route reachable)", code)
	}
	for _, path := range []string{"/", "/api/v1/status"} {
		if code := status("GET", internalAddr, path, "X-View-Key", viewKey); code != http.StatusNotFound {
			t.Errorf("internal listener GET %s = %d, want 404", path, code)
		}
	}
}
