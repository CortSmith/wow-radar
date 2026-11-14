package streamapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/keyring"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func i64(v int64) *int64 { return &v }

// fakeRing stands in for the keys module (modules must not import each other): an authenticator that
// identifies keyring entries, refuses disabled ones with 403 and serves the core.KeyLookup.
type fakeRing struct {
	mu      sync.Mutex
	entries []keyring.Entry
}

func (f *fakeRing) set(entries []keyring.Entry) {
	f.mu.Lock()
	f.entries = entries
	f.mu.Unlock()
}

func (f *fakeRing) find(id string) (keyring.Entry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.entries {
		if e.ID == id {
			return e, true
		}
	}
	return keyring.Entry{}, false
}

func (e fakeEntry) active() bool {
	return !e.Disabled && (e.ExpiresAt == nil || e.ExpiresAt.After(time.Now()))
}

type fakeEntry struct{ keyring.Entry }

func (f *fakeRing) Authenticate(r *http.Request) bool { _, ok := f.Identify(r); return ok }

func (f *fakeRing) Identify(r *http.Request) (core.KeyInfo, bool) {
	e, ok := f.find(keyring.IDFromKey(r.Header.Get("X-Ingest-Key")))
	if !ok || !(fakeEntry{e}).active() {
		return core.KeyInfo{}, false
	}
	return core.KeyInfo{ID: e.ID, FromKeyring: true, Limits: core.KeyLimits(e.Limits)}, true
}

func (f *fakeRing) Reject(r *http.Request) (int, string, string, bool) {
	if e, ok := f.find(keyring.IDFromKey(r.Header.Get("X-Ingest-Key"))); ok && !(fakeEntry{e}).active() {
		return http.StatusForbidden, "key_disabled", "key is disabled", true
	}
	return 0, "", "", false
}

func (f *fakeRing) lookup(id string) (core.KeyInfo, bool) {
	e, ok := f.find(id)
	if !ok {
		return core.KeyInfo{}, false
	}
	return core.KeyInfo{ID: e.ID, FromKeyring: true, Limits: core.KeyLimits(e.Limits)}, (fakeEntry{e}).active()
}

// ringRig is a streamapi module with the keys module (keyring file, fast poll) and a fake clock.
type ringRig struct {
	*rig
	clock *fakeClock
	ring  *fakeRing
	logs  *lockedBuf
}

// save replaces the keyring content; the open streams see it at their next tick.
func (r *ringRig) save(t *testing.T, entries ...keyring.Entry) {
	t.Helper()
	r.ring.set(entries)
}

func ringEntry(key string, l keyring.Limits) keyring.Entry {
	e := keyring.NewEntry(key, "player@example.com", time.Now())
	e.Limits = l
	return e
}

func newRingRig(t *testing.T, entries []keyring.Entry, args ...string) *ringRig {
	t.Helper()
	logs := &lockedBuf{}
	h := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	m := New()
	m.keyTick = 20 * time.Millisecond
	if err := h.Register(m); err != nil {
		t.Fatal(err)
	}
	h.Parsers.Add(&fakeParser{host: h})
	ring := &fakeRing{entries: entries}
	h.Auth.Add(core.RoleIngest, ring)
	h.Auth.SetKeyLookup(ring.lookup)
	rr := &ringRig{rig: &rig{host: h, mod: m}, logs: logs, ring: ring}
	if err := h.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	if err := h.Hooks.RunStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Hooks.RunStop(context.Background()) })
	rr.clock = &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m.now = rr.clock.now
	m.sleep = func(_, _ context.Context, d time.Duration) error {
		rr.clock.mu.Lock()
		rr.clock.t = rr.clock.t.Add(d)
		rr.clock.slept += d
		rr.clock.mu.Unlock()
		return nil
	}
	rr.srv = httptest.NewServer(h.Routes.Handler())
	t.Cleanup(rr.srv.Close)
	return rr
}

// slept runs one stream of n lines with key and returns how long the limiter made it wait.
func (r *ringRig) slept(t *testing.T, key, source string, n int) time.Duration {
	t.Helper()
	before := r.clock.total()
	resp, data := r.withKey(t, key, http.MethodPost, "/v1/stream?source="+source, lines(n))
	if resp.StatusCode != 200 {
		t.Fatalf("%s: %d %s", source, resp.StatusCode, data)
	}
	return r.clock.total() - before
}

func (r *ringRig) keyState(id string) (lineRate int, tokens float64, streams int, ok bool) {
	r.mod.registry()
	r.mod.reg.mu.Lock()
	defer r.mod.reg.mu.Unlock()
	for k, ks := range r.mod.reg.keys {
		if hexOf(k) == id {
			ks.lim.mu.Lock()
			defer ks.lim.mu.Unlock()
			if ks.lim.lines == nil {
				return ks.cur.lineRate, 0, ks.streams, true
			}
			return ks.cur.lineRate, ks.lim.lines.tokens, ks.streams, true
		}
	}
	return 0, 0, 0, false
}

func hexOf(k keyID) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(k)*2)
	for _, b := range k {
		out = append(out, digits[b>>4], digits[b&15])
	}
	return string(out)
}

func TestKeyringLimitsOverrideTheGlobalKeyDefaults(t *testing.T) {
	slow := ringEntry("kslow-0123456789", keyring.Limits{LineRate: i64(100), LineBurst: i64(100)})
	fast := ringEntry("kfast-0123456789", keyring.Limits{LineRate: i64(10_000), LineBurst: i64(10_000)})
	r := newRingRig(t, []keyring.Entry{slow, fast},
		"--stream-line-rate=0", "--stream-byte-rate=0", "--stream-byte-burst=0",
		"--stream-key-line-rate=50", "--stream-key-line-burst=50", "--stream-key-byte-rate=0", "--stream-rate-patience=1h")
	// An env style key (accepted by a plain authenticator) keeps the global 50/s.
	acceptKeys(r.rig)

	if got := r.slept(t, "kslow-0123456789", "a", 300); got < 1900*time.Millisecond || got > 2100*time.Millisecond {
		t.Fatalf("slow key waited %v, want about 2s (100/s, burst 100)", got)
	}
	if got := r.slept(t, "kfast-0123456789", "b", 300); got != 0 {
		t.Fatalf("fast key waited %v", got)
	}
	if got := r.slept(t, "kenv", "c", 300); got < 4900*time.Millisecond || got > 5100*time.Millisecond {
		t.Fatalf("env key waited %v, want about 5s (global 50/s, burst 50)", got)
	}
}

func TestKeyringLimitChangeAppliesLiveWithoutRefill(t *testing.T) {
	const key = "klive-0123456789"
	id := keyring.IDFromKey(key)
	r := newRingRig(t, []keyring.Entry{ringEntry(key, keyring.Limits{LineRate: i64(100), LineBurst: i64(100)})},
		"--stream-line-rate=0", "--stream-byte-rate=0", "--stream-key-byte-rate=0", "--stream-rate-patience=1h")

	pw, resp := r.openStreamKey(t, key, "?source=live")
	_, _ = io.WriteString(pw, lines(100))
	waitUntil(t, func() bool { s := r.sources(t); return len(s) == 1 && s[0].Lines >= 100 })
	if _, tokens, _, _ := r.keyState(id); tokens > 1 {
		t.Fatalf("bucket should be empty, has %v", tokens)
	}
	// Raise the limit: the open stream's bucket is rebuilt in place and is NOT refilled.
	r.save(t, ringEntry(key, keyring.Limits{LineRate: i64(5000), LineBurst: i64(5000)}))
	waitUntil(t, func() bool { rate, _, _, _ := r.keyState(id); return rate == 5000 })
	if _, tokens, _, _ := r.keyState(id); tokens > 1 {
		t.Fatalf("raised limit refilled the bucket: %v tokens", tokens)
	}
	r.save(t, ringEntry(key, keyring.Limits{LineRate: i64(10), LineBurst: i64(10)}))
	waitUntil(t, func() bool { rate, _, _, _ := r.keyState(id); return rate == 10 })
	pw.Close()
	if _, sum := readSummary(t, resp); sum.Ended != endedEOF {
		t.Fatalf("%+v", sum)
	}
}

func TestKeyringBurstDropClampsTokens(t *testing.T) {
	const key = "kclamp-0123456789"
	id := keyring.IDFromKey(key)
	r := newRingRig(t, []keyring.Entry{ringEntry(key, keyring.Limits{LineRate: i64(100), LineBurst: i64(100)})},
		"--stream-line-rate=0", "--stream-byte-rate=0", "--stream-key-byte-rate=0")
	pw, resp := r.openStreamKey(t, key, "?source=clamp")
	waitUntil(t, func() bool { _, tokens, _, ok := r.keyState(id); return ok && tokens == 100 })
	r.save(t, ringEntry(key, keyring.Limits{LineRate: i64(20), LineBurst: i64(20)}))
	waitUntil(t, func() bool { _, tokens, _, _ := r.keyState(id); return tokens == 20 })
	pw.Close()
	readSummary(t, resp)
}

func TestBlockedKeyEndsOpenStreamsAndCannotReconnect(t *testing.T) {
	const key = "kblock-0123456789"
	for name, change := range map[string]func(r *ringRig){
		"disabled": func(r *ringRig) {
			e := ringEntry(key, keyring.Limits{})
			e.Disabled = true
			r.save(t, e)
		},
		"expired": func(r *ringRig) {
			e := ringEntry(key, keyring.Limits{})
			past := time.Now().Add(-time.Hour)
			e.ExpiresAt = &past
			r.save(t, e)
		},
		"removed": func(r *ringRig) { r.save(t) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRingRig(t, []keyring.Entry{ringEntry(key, keyring.Limits{})}, "--stream-max-per-key=1")
			pw, resp := r.openStreamKey(t, key, "?source=one")
			_, _ = io.WriteString(pw, lines(5))
			waitUntil(t, func() bool { s := r.sources(t); return len(s) == 1 && s[0].Lines == 5 })
			start := time.Now()
			change(r)
			status, sum := readSummary(t, resp)
			if status != 200 || sum.Ended != endedBlocked || sum.Lines != 5 {
				t.Fatalf("%d %+v", status, sum)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("blocked stream took %v to end", d)
			}
			pw.Close()
			waitUntil(t, func() bool { return len(r.sources(t)) == 0 })
			if _, _, streams, _ := r.keyState(keyring.IDFromKey(key)); streams != 0 {
				t.Fatalf("slot not freed: %d streams", streams)
			}
			want := http.StatusForbidden
			if name == "removed" {
				want = http.StatusUnauthorized
			}
			resp2, data := r.withKey(t, key, http.MethodPost, "/v1/stream?source=two", lines(1))
			if resp2.StatusCode != want {
				t.Fatalf("reconnect: %d %s", resp2.StatusCode, data)
			}
		})
	}
}

func TestEnvKeyStreamIsNeverBlocked(t *testing.T) {
	r := newRingRig(t, nil)
	acceptKeys(r.rig)
	pw, resp := r.openStreamKey(t, "kenv", "?source=env")
	_, _ = io.WriteString(pw, lines(3))
	waitUntil(t, func() bool { s := r.sources(t); return len(s) == 1 && s[0].Lines == 3 })
	time.Sleep(100 * time.Millisecond) // several ticks
	pw.Close()
	if _, sum := readSummary(t, resp); sum.Ended != endedEOF {
		t.Fatalf("%+v", sum)
	}
}

func TestKeyringMaxStreamsOverride(t *testing.T) {
	one := ringEntry("kone-0123456789", keyring.Limits{MaxStreams: i64(1)})
	many := ringEntry("kmany-0123456789", keyring.Limits{MaxStreams: i64(3)})
	r := newRingRig(t, []keyring.Entry{one, many}, "--stream-max-per-key=2")
	// A refused stream answers at once; an accepted one stays open and answers nothing yet.
	open := func(key, src string) (*io.PipeWriter, int) {
		pw, resp := r.openStreamKey(t, key, "?source="+src)
		select {
		case rs := <-resp:
			defer rs.Body.Close()
			return pw, rs.StatusCode
		case <-time.After(300 * time.Millisecond):
			return pw, http.StatusOK
		}
	}
	pw1, st := open("kone-0123456789", "o1")
	if st != 200 {
		t.Fatal(st)
	}
	if _, st := open("kone-0123456789", "o2"); st != http.StatusTooManyRequests {
		t.Fatalf("second stream of a max-1 key: %d", st)
	}
	var pws []*io.PipeWriter
	for i, src := range []string{"m1", "m2", "m3"} {
		pw, st := open("kmany-0123456789", src)
		if st != 200 {
			t.Fatalf("stream %d of a max-3 key: %d", i, st)
		}
		pws = append(pws, pw)
	}
	if _, st := open("kmany-0123456789", "m4"); st != http.StatusTooManyRequests {
		t.Fatalf("fourth stream of a max-3 key: %d", st)
	}
	pw1.Close()
	for _, pw := range pws {
		pw.Close()
	}
}

func TestBlockedSummaryAndLogsHoldNoKeyMaterial(t *testing.T) {
	const key = "ksecret-0123456789"
	r := newRingRig(t, []keyring.Entry{ringEntry(key, keyring.Limits{})})
	pw, resp := r.openStreamKey(t, key, "?source=pii")
	_, _ = io.WriteString(pw, lines(2))
	waitUntil(t, func() bool { s := r.sources(t); return len(s) == 1 && s[0].Lines == 2 })
	e := ringEntry(key, keyring.Limits{})
	e.Disabled = true
	r.save(t, e)
	rs := <-resp
	raw, _ := io.ReadAll(rs.Body)
	rs.Body.Close()
	pw.Close()
	_, sources := do(t, mustGet(r.srv.URL+"/v1/sources"))
	var out strings.Builder
	out.Write(raw)
	out.Write(sources)
	out.WriteString(r.logs.String())
	for _, bad := range []string{key, keyring.IDFromKey(key), keyring.HashKey(key), "player@example.com"} {
		if strings.Contains(out.String(), bad) {
			t.Fatalf("output leaks %q: %s", bad, out.String())
		}
	}
	var sum summary
	if err := json.Unmarshal(raw, &sum); err != nil || sum.Ended != endedBlocked {
		t.Fatalf("%s %v", raw, err)
	}
}

// openStreamKey is openStream with an ingest key header.
func (r *rig) openStreamKey(t *testing.T, key, query string) (*io.PipeWriter, chan *http.Response) {
	t.Helper()
	pr, pw := io.Pipe()
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/stream"+query, pr)
	req.Header.Set("X-Ingest-Key", key)
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
