package streamapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/keyring"
)

type usageRig struct {
	*rig
	clock *fakeClock
	path  string
	logs  *lockedBuf
}

func newUsageRig(t *testing.T, path string, start time.Time) *usageRig {
	t.Helper()
	logs := &lockedBuf{}
	h := core.NewHost(core.WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	m := New()
	clock := &fakeClock{t: start}
	m.now = clock.now
	m.usageEvery = time.Hour // tests flush by hand or at Stop
	if err := h.Register(m); err != nil {
		t.Fatal(err)
	}
	h.Parsers.Add(&fakeParser{host: h})
	acceptKeys(&rig{host: h})
	if err := h.Config.Load([]string{"--keyusage-file=" + path, "--stream-line-rate=0", "--stream-byte-rate=0",
		"--stream-key-line-rate=0", "--stream-key-byte-rate=0"}, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := &usageRig{rig: &rig{host: h, mod: m}, clock: clock, path: path, logs: logs}
	r.srv = httptest.NewServer(h.Routes.Handler())
	t.Cleanup(r.srv.Close)
	return r
}

func (r *usageRig) stop(t *testing.T) {
	t.Helper()
	if err := r.mod.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func readUsageFile(t *testing.T, path string) usageDoc {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d usageDoc
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	return d
}

var noon = time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local)

func TestUsageCountsPerKeyAndFlushes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyusage.json")
	r := newUsageRig(t, path, noon)
	r.withKey(t, "k1", http.MethodPost, "/v1/stream?source=a", lines(10)) // 10 * 6 bytes
	r.withKey(t, "k1", http.MethodPost, "/v1/lines?source=b", lines(5))
	r.withKey(t, "k2", http.MethodPost, "/v1/stream?source=c", lines(3))
	if err := r.mod.usage.flush(); err != nil {
		t.Fatal(err)
	}
	d := readUsageFile(t, path)
	if d.Version != 1 || len(d.Keys) != 2 {
		t.Fatalf("%+v", d)
	}
	v := d.Keys[keyring.IDFromKey("k1")]
	if v.LinesToday != 15 || v.BytesToday != 90 || !v.LastSeen.Equal(noon) {
		t.Fatalf("k1 %+v", v)
	}
	if v := d.Keys[keyring.IDFromKey("k2")]; v.LinesToday != 3 {
		t.Fatalf("k2 %+v", v)
	}
	// Nothing changed: no write.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := r.mod.usage.flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("unchanged counters were written again")
	}
}

func TestUsageDayRolloverResets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.json")
	r := newUsageRig(t, path, noon)
	r.withKey(t, "k1", http.MethodPost, "/v1/lines", lines(4))
	r.clock.advance(13 * time.Hour) // next local day
	r.withKey(t, "k1", http.MethodPost, "/v1/lines", lines(2))
	_ = r.mod.usage.flush()
	if v := readUsageFile(t, path).Keys[keyring.IDFromKey("k1")]; v.LinesToday != 2 {
		t.Fatalf("%+v", v)
	}
	r.clock.advance(24 * time.Hour) // idle key still resets
	_ = r.mod.usage.flush()
	if v := readUsageFile(t, path).Keys[keyring.IDFromKey("k1")]; v.LinesToday != 0 || v.BytesToday != 0 {
		t.Fatalf("%+v", v)
	}
}

func TestUsageShutdownFlushAndRestartReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.json")
	r := newUsageRig(t, path, noon)
	r.withKey(t, "k1", http.MethodPost, "/v1/lines", lines(7))
	r.stop(t) // the flusher writes once at shutdown
	if v := readUsageFile(t, path).Keys[keyring.IDFromKey("k1")]; v.LinesToday != 7 {
		t.Fatalf("%+v", v)
	}
	r2 := newUsageRig(t, path, noon.Add(time.Hour))
	r2.withKey(t, "k1", http.MethodPost, "/v1/lines", lines(3))
	r2.stop(t)
	if v := readUsageFile(t, path).Keys[keyring.IDFromKey("k1")]; v.LinesToday != 10 {
		t.Fatalf("same-day counters lost: %+v", v)
	}
	// Next day: last seen is kept, counts start at zero.
	r3 := newUsageRig(t, path, noon.Add(30*time.Hour))
	r3.withKey(t, "k1", http.MethodPost, "/v1/lines", lines(1))
	r3.stop(t)
	if v := readUsageFile(t, path).Keys[keyring.IDFromKey("k1")]; v.LinesToday != 1 {
		t.Fatalf("%+v", v)
	}
}

func TestUsageInvalidFileIgnoredAndOldDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := newUsageTracker(path, func() time.Time { return noon })
	if len(u.entries) != 0 {
		t.Fatal("invalid file must start empty")
	}
	old := noon.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	fresh := noon.Add(-time.Hour).Format(time.RFC3339)
	doc := `{"version":1,"keys":{"aa":{"lastSeen":"` + old + `","linesToday":1,"bytesToday":1},"bb":{"lastSeen":"` + fresh + `","linesToday":2,"bytesToday":3}}}`
	_ = os.WriteFile(path, []byte(doc), 0o600)
	u = newUsageTracker(path, func() time.Time { return noon })
	if len(u.entries) != 1 || u.entries["bb"].lines.Load() != 2 {
		t.Fatalf("%v", u.entries)
	}
}

func TestUsageEvictsLeastRecentlySeen(t *testing.T) {
	now := noon
	u := newUsageTracker(filepath.Join(t.TempDir(), "u.json"), func() time.Time { now = now.Add(time.Second); return now })
	for i := 0; i < maxUsageKeys+10; i++ {
		u.touch(string(rune('a'+i%26)) + strings.Repeat("x", i/26) + "-" + time.Duration(i).String())
	}
	if len(u.entries) != maxUsageKeys {
		t.Fatalf("%d entries", len(u.entries))
	}
	// The first ten (oldest) are gone.
	if _, ok := u.entries["a-0s"]; ok {
		t.Fatal("oldest entry survived")
	}
}

func TestUsageOffByDefault(t *testing.T) {
	r := newRig(t, true)
	if r.mod.usage != nil {
		t.Fatal("tracker without a setting")
	}
}

func TestUsageFileHoldsOnlyIdsTimesAndCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.json")
	r := newUsageRig(t, path, noon)
	const key = "kSECRET-0123456789"
	r.withKey(t, key, http.MethodPost, "/v1/stream?source=my-secret-source", "player@example.com 1\nbad\n")
	r.stop(t)
	data, _ := os.ReadFile(path)
	all := string(data) + r.logs.String()
	e := keyring.NewEntry(key, "player@example.com", noon)
	for _, bad := range []string{key, e.Hash, "@", "my-secret-source", "player", "127.0.0.1"} {
		if bad != "" && strings.Contains(all, bad) {
			t.Fatalf("%q leaked into:\n%s", bad, all)
		}
	}
	if !strings.Contains(string(data), e.ID) {
		t.Fatal("key id missing")
	}
}
