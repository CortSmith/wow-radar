package streamapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// fakeParser stands in for logparse. A line is "<guid-suffix> <ms>" (optionally "owner ..." to
// announce a detected ruleset); the line "bad" fails to parse.
type fakeParser struct {
	host *core.Host
	mu   sync.Mutex
	seen []domain.RawLine
}

func (*fakeParser) Name() string { return "fakeparse" }

func (f *fakeParser) Parse(ctx context.Context, event any) error {
	line, ok := event.(domain.RawLine)
	if !ok {
		return nil
	}
	f.mu.Lock()
	f.seen = append(f.seen, line)
	f.mu.Unlock()
	if line.Text == "bad" {
		return errors.New("bad line")
	}
	fields := strings.Fields(line.Text)
	ms, _ := strconv.ParseInt(fields[len(fields)-1], 10, 64)
	ev := domain.Event{Source: line.Source, TimeMs: ms, Name: "SWING_DAMAGE", Ruleset: line.Ruleset,
		Src: domain.EventUnit{GUID: "Player-1-" + fields[0]}, Dst: domain.EventUnit{GUID: "Creature-0-1"}}
	if fields[0] == "owner" {
		ev.Ruleset = domain.RulesetRP
	}
	return f.host.Parsers.Feed(ctx, ev)
}

func (f *fakeParser) lines() []domain.RawLine {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.RawLine(nil), f.seen...)
}

type rig struct {
	host   *core.Host
	mod    *Module
	parser *fakeParser
	srv    *httptest.Server
}

func newRig(t *testing.T, withParser bool, args ...string) *rig {
	t.Helper()
	h := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	m := New()
	if err := h.Register(m); err != nil {
		t.Fatal(err)
	}
	r := &rig{host: h, mod: m}
	if withParser {
		r.parser = &fakeParser{host: h}
		h.Parsers.Add(r.parser)
	}
	if err := h.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	r.srv = httptest.NewServer(h.Routes.Handler())
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) post(t *testing.T, path, ctype, body string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+path, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	return do(t, req)
}

func do(t *testing.T, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return v
}

func (r *rig) sources(t *testing.T) []sourceInfo {
	t.Helper()
	resp, data := do(t, mustGet(r.srv.URL+"/v1/sources"))
	if resp.StatusCode != 200 {
		t.Fatalf("sources status %d", resp.StatusCode)
	}
	return decode[struct{ Sources []sourceInfo }](t, data).Sources
}

func mustGet(url string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	return req
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("condition not reached in time")
	}
}

// openStream starts a chunked stream and returns the pipe to write to and the pending response.
func (r *rig) openStream(t *testing.T, query string) (*io.PipeWriter, chan *http.Response) {
	t.Helper()
	pr, pw := io.Pipe()
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/stream"+query, pr)
	out := make(chan *http.Response, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			close(out)
			return
		}
		out <- resp
	}()
	return pw, out
}

func readSummary(t *testing.T, ch chan *http.Response) (int, summary) {
	t.Helper()
	select {
	case resp, ok := <-ch:
		if !ok {
			t.Fatal("stream request failed")
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, decode[summary](t, data)
	case <-time.After(5 * time.Second):
		t.Fatal("no summary")
	}
	return 0, summary{}
}

func TestStreamFeedsLinesAndSummarises(t *testing.T) {
	r := newRig(t, true)
	pw, resp := r.openStream(t, "?source=laptop")
	fmt.Fprint(pw, "ann 1000\n\nbob 2000\r\nbad\nann 3000\n")
	waitUntil(t, func() bool { return len(r.sources(t)) == 1 && r.sources(t)[0].Lines == 4 })
	src := r.sources(t)[0]
	if src.Source != "laptop" || src.LastEvent != 3000 || src.Since == 0 || src.LinesPerSec <= 0 {
		t.Fatalf("source info %+v", src)
	}
	pw.Close()
	status, sum := readSummary(t, resp)
	if status != 200 || sum.Lines != 4 || sum.Events != 3 || sum.Contacts != 2 || sum.Rejected != 1 || sum.Ended != endedEOF || sum.Source != "laptop" {
		t.Fatalf("summary %+v", sum)
	}
	if got := r.sources(t); len(got) != 0 {
		t.Fatalf("source still listed: %+v", got)
	}
}

func TestStreamRulesetOverrideAndDetection(t *testing.T) {
	r := newRig(t, true)
	pw, resp := r.openStream(t, "?source=a&rs=pvp")
	fmt.Fprint(pw, "ann 1000\n")
	waitUntil(t, func() bool { s := r.sources(t); return len(s) == 1 && s[0].Lines == 1 })
	if rs := r.sources(t)[0].RS; rs != "pvp" {
		t.Fatalf("rs %q", rs)
	}
	pw.Close()
	readSummary(t, resp)
	if got := r.parser.lines()[0].Ruleset; got != domain.RulesetPvP {
		t.Fatalf("override not conveyed: %q", got)
	}

	pw, resp = r.openStream(t, "?source=b")
	fmt.Fprint(pw, "owner 1000\n")
	waitUntil(t, func() bool { s := r.sources(t); return len(s) == 1 && s[0].RS == "rp" })
	pw.Close()
	readSummary(t, resp)
}

func TestStreamRejectsBadParameters(t *testing.T) {
	r := newRig(t, true)
	for _, q := range []string{"?source=has%20space", "?rs=nope", "?source=" + strings.Repeat("a", 60)} {
		resp, data := r.post(t, "/v1/stream"+q, "", "x 1\n")
		if resp.StatusCode != 400 || !strings.Contains(string(data), "bad_request") {
			t.Fatalf("%s: %d %s", q, resp.StatusCode, data)
		}
	}
}

func TestStreamDefaultSource(t *testing.T) {
	r := newRig(t, true)
	_, data := r.post(t, "/v1/stream", "", "x 1\n")
	if sum := decode[summary](t, data); sum.Source != defaultSource || sum.Lines != 1 {
		t.Fatalf("%+v", sum)
	}
}

func TestDuplicateSourceAndSourceCap(t *testing.T) {
	r := newRig(t, true, "--max-sources=2")
	pw1, c1 := r.openStream(t, "?source=a")
	pw2, c2 := r.openStream(t, "?source=b")
	waitUntil(t, func() bool { return len(r.sources(t)) == 2 })
	resp, data := r.post(t, "/v1/stream?source=a", "", "x 1\n")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(data), "source_busy") {
		t.Fatalf("dup: %d %s", resp.StatusCode, data)
	}
	resp, data = r.post(t, "/v1/lines?source=c", "", "x 1\n")
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(data), "too_many_sources") {
		t.Fatalf("cap: %d %s", resp.StatusCode, data)
	}
	pw1.Close()
	pw2.Close()
	readSummary(t, c1)
	readSummary(t, c2)
}

func TestStreamIdleTimeout(t *testing.T) {
	r := newRig(t, true, "--stream-idle=150ms")
	pw, resp := r.openStream(t, "?source=quiet")
	fmt.Fprint(pw, "ann 1\n")
	status, sum := readSummary(t, resp)
	if status != 200 || sum.Ended != endedIdle || sum.Lines != 1 {
		t.Fatalf("%d %+v", status, sum)
	}
	pw.Close()
}

func TestStreamByteCap(t *testing.T) {
	r := newRig(t, true, "--stream-max-bytes=20")
	_, data := r.post(t, "/v1/stream", "", strings.Repeat("ann 1000\n", 10))
	sum := decode[summary](t, data)
	if sum.Ended != endedMaxBytes || sum.Lines >= 10 {
		t.Fatalf("%+v", sum)
	}
}

func TestStreamOverlongLineIsRejectedAndStreamContinues(t *testing.T) {
	r := newRig(t, true)
	_, data := r.post(t, "/v1/stream", "", strings.Repeat("a", MaxLineBytes+10)+"\nann 1\n")
	if sum := decode[summary](t, data); sum.Rejected != 1 || sum.Lines != 2 || sum.Events != 1 {
		t.Fatalf("%+v", sum)
	}
}

func TestStreamEndsOnShutdown(t *testing.T) {
	r := newRig(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.mod.Start(ctx); err != nil {
		t.Fatal(err)
	}
	pw, resp := r.openStream(t, "?source=live")
	fmt.Fprint(pw, "ann 1\n")
	waitUntil(t, func() bool { s := r.sources(t); return len(s) == 1 && s[0].Lines == 1 })
	cancel()
	_, sum := readSummary(t, resp)
	if sum.Ended != endedShutdown {
		t.Fatalf("%+v", sum)
	}
	pw.Close()
}

func TestStreamWorksWithoutAnyParser(t *testing.T) {
	r := newRig(t, false)
	_, data := r.post(t, "/v1/stream", "", "one\ntwo\n")
	if sum := decode[summary](t, data); sum.Lines != 2 || sum.Events != 0 || sum.Rejected != 0 {
		t.Fatalf("%+v", sum)
	}
}

func TestLinesText(t *testing.T) {
	r := newRig(t, true)
	resp, data := r.post(t, "/v1/lines?source=batch&rs=rp", "text/plain", "ann 1\nbob 2\nbad\n")
	sum := decode[summary](t, data)
	if resp.StatusCode != 200 || sum.Lines != 3 || sum.Events != 2 || sum.Rejected != 1 || sum.Contacts != 2 {
		t.Fatalf("%d %+v", resp.StatusCode, sum)
	}
	if len(r.sources(t)) != 0 {
		t.Fatal("batch session must be closed")
	}
}

func TestLinesJSON(t *testing.T) {
	r := newRig(t, true)
	body := `{"lines":["ann 1","bob 2","` + strings.Repeat("x", MaxLineBytes+1) + `"]}`
	resp, data := r.post(t, "/v1/lines", "application/json; charset=utf-8", body)
	sum := decode[summary](t, data)
	if resp.StatusCode != 200 || sum.Lines != 3 || sum.Events != 2 || sum.Rejected != 1 {
		t.Fatalf("%d %+v", resp.StatusCode, sum)
	}
}

func TestLinesLimits(t *testing.T) {
	r := newRig(t, true, "--lines-max-lines=2", "--lines-max-bytes=64")
	cases := []struct {
		name, ctype, body string
		status            int
	}{
		{"too many text lines", "text/plain", "a 1\nb 2\nc 3\n", 413},
		{"too many json lines", "application/json", `{"lines":["a 1","b 2","c 3"]}`, 413},
		{"too many bytes", "text/plain", strings.Repeat("a", 100), 413},
		{"too many json bytes", "application/json", `{"lines":["` + strings.Repeat("a", 100) + `"]}`, 413},
		{"bad json", "application/json", `{"lines":`, 400},
		{"within limits", "text/plain", "a 1\nb 2\n", 200},
	}
	for _, tc := range cases {
		resp, data := r.post(t, "/v1/lines", tc.ctype, tc.body)
		if resp.StatusCode != tc.status {
			t.Fatalf("%s: %d %s", tc.name, resp.StatusCode, data)
		}
	}
}

func TestLinesBadParametersAndBusy(t *testing.T) {
	r := newRig(t, true)
	if resp, _ := r.post(t, "/v1/lines?rs=zzz", "text/plain", "a 1\n"); resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	pw, c := r.openStream(t, "?source=x")
	waitUntil(t, func() bool { return len(r.sources(t)) == 1 })
	if resp, _ := r.post(t, "/v1/lines?source=x", "text/plain", "a 1\n"); resp.StatusCode != 409 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	pw.Close()
	readSummary(t, c)
}

func TestAuth(t *testing.T) {
	r := newRig(t, true)
	r.host.Auth.Add(core.RoleIngest, core.AuthenticatorFunc(func(q *http.Request) bool { return q.Header.Get("X-Ingest-Key") == "ingest" }))
	r.host.Auth.Add(core.RoleView, core.AuthenticatorFunc(func(q *http.Request) bool { return q.Header.Get("X-View-Key") == "view" }))
	if resp, _ := r.post(t, "/v1/stream", "", "a 1\n"); resp.StatusCode != 401 {
		t.Fatalf("stream without key: %d", resp.StatusCode)
	}
	if resp, _ := r.post(t, "/v1/lines", "", "a 1\n"); resp.StatusCode != 401 {
		t.Fatalf("lines without key: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/lines", strings.NewReader("a 1\n"))
	req.Header.Set("X-Ingest-Key", "ingest")
	if resp, _ := do(t, req); resp.StatusCode != 200 {
		t.Fatalf("lines with key: %d", resp.StatusCode)
	}
	for hdr, want := range map[string]int{"": 401, "X-View-Key": 200, "X-Ingest-Key": 200} {
		req := mustGet(r.srv.URL + "/v1/sources")
		if hdr != "" {
			req.Header.Set(hdr, strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(hdr, "X-"), "-Key")))
		}
		if resp, _ := do(t, req); resp.StatusCode != want {
			t.Fatalf("sources with %q: %d", hdr, resp.StatusCode)
		}
	}
}

func TestInitFailsOnDuplicateSetting(t *testing.T) {
	h := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	_ = h.Config.Declare(core.Setting{Key: SettingMaxSources, Kind: core.KindInt})
	if err := New().Init(h); err == nil {
		t.Fatal("expected error")
	}
}

func TestInitFailsOnRouteConflict(t *testing.T) {
	h := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	_ = h.Routes.HandleFunc(http.MethodGet, "/v1/sources", func(http.ResponseWriter, *http.Request) {})
	if err := New().Init(h); err == nil {
		t.Fatal("expected error")
	}
}

func TestObserverIgnoresOtherEventsAndUnknownSources(t *testing.T) {
	r := newRig(t, false)
	o := observer{r.mod}
	if err := o.Parse(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if err := o.Parse(context.Background(), domain.Event{Source: "ghost"}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionPlayerSetIsBounded(t *testing.T) {
	s := &session{players: map[string]struct{}{}}
	for i := 0; i < maxTrackedContacts+50; i++ {
		s.observe(domain.Event{Src: domain.EventUnit{GUID: fmt.Sprintf("Player-1-%d", i)}})
	}
	if s.contacts() != maxTrackedContacts {
		t.Fatalf("contacts %d", s.contacts())
	}
}

func TestEndReason(t *testing.T) {
	live, stopped := context.Background(), func() context.Context { c, f := context.WithCancel(context.Background()); f(); return c }()
	if endReason(io.EOF, live) != endedEOF || endReason(errors.New("x"), live) != endedError || endReason(errors.New("x"), stopped) != endedShutdown {
		t.Fatal("endReason mismatch")
	}
}

func TestRegistryCloseIgnoresReplacedSession(t *testing.T) {
	reg := newRegistry(2)
	a, _ := reg.open("a", "", keyID{}, time.Now())
	reg.close(a)
	b, _ := reg.open("a", "", keyID{}, time.Now())
	reg.close(a)
	if reg.get("a") != b {
		t.Fatal("stale close removed the new session")
	}
}

func TestRefusedStreamsAreAuditedAndSessionsGaugeCounts(t *testing.T) {
	r := newRig(t, true, "--max-sources=1")
	pw, c := r.openStream(t, "?source=a")
	waitUntil(t, func() bool { return len(r.sources(t)) == 1 })
	if v, _ := r.host.Metrics.Value("radar_stream_sessions"); v != 1 {
		t.Fatalf("radar_stream_sessions = %v, want 1", v)
	}
	r.post(t, "/v1/stream?source=a&key=test-key-0000000000000000", "", "x 1\n")
	r.post(t, "/v1/lines?source=c", "", "x 1\n")
	pw.Close()
	readSummary(t, c)
	var got []core.AuditEvent
	r.host.Audit.Observe(func(ev core.AuditEvent) { got = append(got, ev) })
	if len(got) != 2 || got[0].Event != "stream_refused" || got[0].Reason != "source_busy" || got[1].Reason != "too_many_sources" ||
		got[0].Class != "ingest" || got[0].IP == "" {
		t.Fatalf("events = %+v", got)
	}
	for _, ev := range got {
		if strings.Contains(ev.Reason+ev.IP+ev.Class, "test-key") {
			t.Fatal("query leaked into an event")
		}
	}
}
