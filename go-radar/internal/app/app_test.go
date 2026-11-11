package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nearbyradar/internal/clock"
	"nearbyradar/internal/screenshot"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParseFlagsDefaultsAndEnv(t *testing.T) {
	c, err := ParseFlags(nil, env(nil), `C:\app`)
	if err != nil {
		t.Fatal(err)
	}
	if c.LogsDir != DefaultLogsDir || c.TailMB != 8 || c.FromStart || c.TrackNPCs || c.Orientation != "std" || c.Browser || c.Debug {
		t.Fatalf("%+v", c)
	}
	c, _ = ParseFlags(nil, env(map[string]string{"WOW_LOGS": `D:\logs`}), `C:\app`)
	if c.LogsDir != `D:\logs` {
		t.Fatalf("env: %q", c.LogsDir)
	}
	c, _ = ParseFlags([]string{"--logs", `E:\x`}, env(map[string]string{"WOW_LOGS": `D:\logs`}), `C:\app`)
	if c.LogsDir != `E:\x` {
		t.Fatalf("flag beats env: %q", c.LogsDir)
	}
}

func TestParseFlagsAll(t *testing.T) {
	c, err := ParseFlags([]string{"--from-start", "--tail-mb", "2", "--realm-suffix", "-PvP-EU", "--track-npcs", "--no-approx",
		"--orientation", "alt", "--maps", "M", "--history", "H.json", "--browser", "--no-ui", "--debug", "--keep-alive", "--log-file", "x.log",
		"--stale", "1m", "--expire", "2m", "--analyze-logs", "--analyze-file", "a.txt", "--analyze-file", "b.txt"}, env(nil), `C:\app`)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{LogsDir: DefaultLogsDir, FromStart: true, TailMB: 2, RealmSuffix: "-PvP-EU", TrackNPCs: true, NoApprox: true,
		Orientation: "alt", MapsDir: "M", HistoryPath: "H.json", Browser: true, NoUI: true, Debug: true, KeepAlive: true, LogFile: "x.log",
		Stale: time.Minute, Expire: 2 * time.Minute, ExeDir: `C:\app`,
		AnalyzeLogs: true, AnalyzeFiles: []string{"a.txt", "b.txt"}, Ruleset: "auto"}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("\n got %+v\nwant %+v", c, want)
	}
}

func TestParseFlagsErrors(t *testing.T) {
	for _, args := range [][]string{{"--bogus"}, {"--orientation", "sideways"}, {"--tail-mb", "0"}, {"--stale", "5m", "--expire", "1m"}} {
		if _, err := ParseFlags(args, env(nil), "."); err == nil {
			t.Errorf("%v should fail", args)
		}
	}
}

func TestResolveHistoryPath(t *testing.T) {
	dir := t.TempDir()
	if p := ResolveHistoryPath(dir, "", `C:\ad`); p != filepath.Join(dir, "radar-history.json") {
		t.Fatalf("next to exe: %q", p)
	}
	if p := ResolveHistoryPath(filepath.Join(dir, "missing"), "", dir); p != filepath.Join(dir, "NearbyRadar", "radar-history.json") {
		t.Fatalf("fallback: %q", p)
	}
	if p := ResolveHistoryPath(dir, "custom.json", dir); p != "custom.json" {
		t.Fatalf("override: %q", p)
	}
}

func syntheticLog(now time.Time) string {
	ts := now.UTC().Format("1/2/2006 15:04:05.000") + "+0"
	return strings.Join([]string{
		ts + `  COMBAT_LOG_VERSION,22,ADVANCED_LOG_ENABLED,1,BUILD_VERSION,1.60.1,PROJECT_ID,18`,
		ts + `  ZONE_CHANGE,0,"Dun Morogh",0`,
		ts + `  MAP_CHANGE,1426,"Dun Morogh",-3877.083252,-7160.416504,1802.083252,-3122.916504`,
		ts + `  SPELL_CAST_SUCCESS,Player-1-0000ME01,"Mage-Realm-PvP-US",0x511,0x80000000,0000000000000000,nil,0x80000000,0x80000000,2963,"Bolt",0x1,Player-1-0000ME01,0000000000000000,67,67,20,0,47,0,0,0,0,184,184,0,-5300.5,-200.25,1426,0.69,10`,
		ts + `  SPELL_DAMAGE,Player-1-0000F003,"Rogue-Realm-PvP-US",0x548,0x80000000,Player-1-0000ME01,"Mage-Realm-PvP-US",0x511,0x80000000,9532,"Bolt",0x8,Player-1-0000ME01,0000000000000000,50,67,20,0,47,0,0,0,0,184,184,0,-5300.5,-200.25,1426,0.69,10,17,17,-1,8,0,0,0,nil,nil,nil,ST`,
		"garbage line",
		"",
	}, "\n")
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
}

type fakeShot struct{ n atomic.Int32 }

func (f *fakeShot) Read(context.Context) (screenshot.Observation, error) {
	f.n.Add(1)
	return screenshot.Observation{HasZone: true, Zone: "Ironforge", HasPos: true, X: 1, Y: 2, HasMap: true, MapID: 1455}, nil
}

func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "Logs")
	_ = os.Mkdir(logs, 0o700)
	if err := os.WriteFile(filepath.Join(logs, "WoWCombatLog-111026_151053.txt"), []byte(syntheticLog(time.Now())), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{LogsDir: logs, FromStart: true, TailMB: 8, Orientation: "std", HistoryPath: filepath.Join(dir, "h.json"), ExeDir: dir}
	var logged []string
	a, err := New(cfg, clock.Real{}, func(f string, args ...any) { logged = append(logged, fmt.Sprintf(f, args...)) })
	if err != nil {
		t.Fatal(err)
	}
	shot := &fakeShot{}
	a.ScreenshotReader, a.ScreenshotInterval = shot, 20*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	url, err := a.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Zone  string
		MapID int
		Pips  []struct {
			GUID, Name, Reaction, State, PosKind string
			X                                    float64
			DamageTotal                          float64
		}
		View struct{ LogFile string }
	}
	waitFor(t, "pips", func() bool {
		st.Pips = nil
		getJSON(t, url+"api/state", &st)
		return len(st.Pips) >= 2 && st.Zone == "Ironforge" // screenshot observation overrides zone
	})
	var foe, me int = -1, -1
	for i, p := range st.Pips {
		switch p.GUID {
		case "Player-1-0000F003":
			foe = i
		case "Player-1-0000ME01":
			me = i
		}
	}
	if foe < 0 || me < 0 {
		t.Fatalf("pips %+v", st.Pips)
	}
	if st.Pips[foe].Reaction != "hostile" || st.Pips[foe].DamageTotal != 17 || st.Pips[foe].Name != "Rogue Realm PvP US" {
		t.Fatalf("foe %+v", st.Pips[foe])
	}
	if st.Pips[me].PosKind != "exact" || st.Pips[me].X != 1 { // screenshot moved me
		t.Fatalf("me %+v", st.Pips[me])
	}
	if st.View.LogFile != "WoWCombatLog-111026_151053.txt" {
		t.Fatalf("log file %q", st.View.LogFile)
	}
	var hist []struct {
		GUID            string
		AvgDamagePerHit float64
	}
	getJSON(t, url+"api/history", &hist)
	found := false
	for _, h := range hist {
		if h.GUID == "Player-1-0000F003" && h.AvgDamagePerHit == 17 {
			found = true
		}
	}
	if !found {
		t.Fatalf("history %+v", hist)
	}
	cancel()
	a.Wait()
	if _, err := os.Stat(filepath.Join(dir, "h-pvp.json")); err != nil {
		t.Fatalf("history not flushed on shutdown: %v", err)
	}
	if shot.n.Load() == 0 {
		t.Fatal("screenshot poller never ran")
	}
	st1, bad := a.Counters()
	if st1 < 5 || bad < 1 {
		t.Fatalf("counters lines=%d bad=%d", st1, bad)
	}
}

func TestStartWithMissingLogsDirKeepsRunning(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{LogsDir: filepath.Join(dir, "nope"), TailMB: 8, Orientation: "std", HistoryPath: filepath.Join(dir, "h.json"), ExeDir: dir}
	a, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	url, err := a.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var st struct{ Pips []any }
	getJSON(t, url+"api/state", &st)
	if st.Pips == nil || len(st.Pips) != 0 {
		t.Fatalf("%+v", st)
	}
	cancel()
	a.Wait()
}

func TestCorruptHistoryDoesNotPreventStart(t *testing.T) {
	dir := t.TempDir()
	hp := filepath.Join(dir, "h.json")
	_ = os.WriteFile(hp, []byte("{oops"), 0o600)
	var msgs []string
	a, err := New(Config{LogsDir: dir, TailMB: 8, Orientation: "std", HistoryPath: hp, ExeDir: dir}, nil, func(f string, args ...any) { msgs = append(msgs, f) })
	if err != nil || a == nil {
		t.Fatalf("%v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("corruption should be logged")
	}
}

func TestWaitForClientsReturnsAfterTabCloses(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(Config{LogsDir: dir, TailMB: 8, Orientation: "std", HistoryPath: filepath.Join(dir, "h.json"), ExeDir: dir}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); a.Wait() }()
	url, err := a.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { a.WaitForClients(ctx, 300*time.Millisecond); close(done) }()
	select {
	case <-done:
		t.Fatal("must not return before any client connected")
	case <-time.After(600 * time.Millisecond):
	}
	cctx, ccancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(cctx, "GET", url+"api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "client", func() bool { return a.Server.Clients() == 1 })
	time.Sleep(500 * time.Millisecond) // connected: still running
	select {
	case <-done:
		t.Fatal("returned while a client was connected")
	default:
	}
	ccancel()
	resp.Body.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not return after the client left")
	}
}

func TestWaitForClientsCancel(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(Config{LogsDir: dir, TailMB: 8, Orientation: "std", HistoryPath: filepath.Join(dir, "h.json"), ExeDir: dir}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.WaitForClients(ctx, time.Hour); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx cancel ignored")
	}
}

func TestBaseName(t *testing.T) {
	for in, want := range map[string]string{`C:\a\b.txt`: "b.txt", "x/y/z": "z", "plain": "plain", "": ""} {
		if got := baseName(in); got != want {
			t.Errorf("%q -> %q", in, got)
		}
	}
}
