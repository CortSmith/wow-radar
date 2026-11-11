package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nearbyradar/internal/bounty"
)

func bountyLog(sec0 int) string {
	ts := func(s int) string { return fmt.Sprintf("11/10/2026 16:00:%02d.000+0  ", s) }
	const victim = `Player-1-0000A001,"Vic-Realm-PvP-US",0x518,0x80000000`
	const foeA = `Player-1-0000E001,"Fone-Realm-PvP-US",0x548,0x80000000`
	const foeB = `Player-1-0000E002,"Ftwo-Realm-PvP-US",0x548,0x80000000`
	hit := func(s int, src string) string {
		return ts(s) + `SPELL_DAMAGE,` + src + `,` + victim + `,133,"Fireball",0x4,100,0,4,0,0,0,nil,nil,nil`
	}
	return strings.Join([]string{
		ts(sec0) + `COMBAT_LOG_VERSION,22,ADVANCED_LOG_ENABLED,1,BUILD_VERSION,1.60.1,PROJECT_ID,18`,
		hit(sec0+1, foeB), hit(sec0+2, foeA),
		ts(sec0+3) + `UNIT_DIED,0000000000000000,nil,0x80000000,0x80000000,` + victim + `,0`,
	}, "\n") + "\n"
}

type bountyRows struct {
	Rows []struct {
		GUID         string
		Name         string
		KillingBlows int
		Assists      int
		Bounty       int
	}
	Stats struct{ Deaths int }
}

func postAnalyze(t *testing.T, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"api/bounty/analyze", strings.NewReader(body))
	req.Header.Set("X-Radar", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func waitIdle(t *testing.T, url string) bounty.Status {
	t.Helper()
	var st bounty.Status
	waitFor(t, "analysis done", func() bool {
		st = bounty.Status{}
		getJSON(t, url+"api/bounty/analyze", &st)
		return st.JobID != "" && !st.Running
	})
	return st
}

func TestParseFlagsAnalyzeDefaultsOff(t *testing.T) {
	c, err := ParseFlags(nil, env(nil), ".")
	if err != nil || c.AnalyzeLogs || len(c.AnalyzeFiles) != 0 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestBountyEndToEndLiveAndReverse(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "Logs")
	_ = os.Mkdir(logs, 0o700)
	old := filepath.Join(logs, "WoWCombatLog-100126_000000.txt")
	_ = os.WriteFile(old, []byte(bountyLog(0)), 0o600)
	_ = os.Chtimes(old, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))
	cur := filepath.Join(logs, "WoWCombatLog-111026_151053.txt")
	_ = os.WriteFile(cur, []byte(bountyLog(10)), 0o600)
	hp := filepath.Join(dir, "h.json")
	cfg := Config{LogsDir: logs, FromStart: true, TailMB: 8, Orientation: "std", HistoryPath: hp, ExeDir: dir}

	a, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	url, err := a.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rows bountyRows
	waitFor(t, "live bounty", func() bool {
		rows = bountyRows{}
		getJSON(t, url+"api/bounty", &rows)
		return rows.Stats.Deaths == 1
	})
	if len(rows.Rows) != 2 || rows.Rows[0].GUID != "Player-1-0000E001" || rows.Rows[0].KillingBlows != 1 || rows.Rows[1].Assists != 1 {
		t.Fatalf("%+v", rows)
	}
	// reverse analysis over both files: the live death in the newest file is not double counted
	code, body := postAnalyze(t, url, "")
	if code != 202 {
		t.Fatalf("%d %s", code, body)
	}
	st := waitIdle(t, url)
	if st.FilesTotal != 2 || st.FilesDone != 2 || st.Deaths != 2 || st.NewDeaths != 1 || len(st.Errors) != 0 {
		t.Fatalf("%+v", st)
	}
	rows = bountyRows{}
	getJSON(t, url+"api/bounty", &rows)
	if rows.Stats.Deaths != 2 || rows.Rows[0].KillingBlows != 2 || rows.Rows[0].Bounty != 20 || rows.Rows[1].Assists != 2 {
		t.Fatalf("%+v", rows)
	}
	// run again: still 2
	if code, body := postAnalyze(t, url, ""); code != 202 {
		t.Fatalf("%d %s", code, body)
	}
	if st := waitIdle(t, url); st.NewDeaths != 0 || st.Deaths != 2 {
		t.Fatalf("%+v", st)
	}
	// traversal attempts are refused and read nothing
	for _, f := range []string{`../x.txt`, `..\x.txt`, filepath.Join(dir, "h.json"), "WoWCombatLog-111026_151053.txt/../../h.json"} {
		b, _ := json.Marshal(map[string]any{"files": []string{f}})
		if code, body := postAnalyze(t, url, string(b)); code != 400 {
			t.Errorf("%q -> %d %s", f, code, body)
		}
	}
	cancel()
	a.Wait()

	// the ledger is persisted next to the history file and survives a restart without double counting
	bp := bounty.PathFor(filepath.Join(dir, "h-pvp.json")) // per-ruleset ledger (default ruleset: pvp)
	if _, err := os.Stat(bp); err != nil {
		t.Fatalf("bounty file not written: %v", err)
	}
	cfg2 := cfg
	cfg2.AnalyzeLogs = true
	a2, err := New(cfg2, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer func() { cancel2(); a2.Wait() }()
	url2, err := a2.Start(ctx2)
	if err != nil {
		t.Fatal(err)
	}
	st = waitIdle(t, url2) // --analyze-logs started a background job at startup
	if st.NewDeaths != 0 || st.Deaths != 2 {
		t.Fatalf("%+v", st)
	}
	rows = bountyRows{}
	getJSON(t, url2+"api/bounty", &rows)
	if rows.Stats.Deaths != 2 || rows.Rows[0].KillingBlows != 2 {
		t.Fatalf("%+v", rows)
	}
}

func TestAnalyzeFileFlagReadsExplicitPath(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "Logs")
	_ = os.Mkdir(logs, 0o700)
	extra := filepath.Join(dir, "elsewhere.txt")
	_ = os.WriteFile(extra, []byte(bountyLog(0)), 0o600)
	cfg := Config{LogsDir: logs, TailMB: 8, Orientation: "std", HistoryPath: filepath.Join(dir, "h.json"), ExeDir: dir, AnalyzeFiles: []string{extra}}
	a, _ := New(cfg, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); a.Wait() }()
	url, err := a.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st := waitIdle(t, url)
	if st.FilesTotal != 1 || st.NewDeaths != 1 {
		t.Fatalf("%+v", st)
	}
	// but the API cannot name that file
	if code, _ := postAnalyze(t, url, `{"files":["elsewhere.txt"]}`); code != 400 {
		t.Fatalf("API must only read the logs folder, got %d", code)
	}
}

func TestOldHistoryFileLoadsWithoutBountyFile(t *testing.T) {
	dir := t.TempDir()
	hp := filepath.Join(dir, "h.json")
	_ = os.WriteFile(hp, []byte(`{"version":1,"entries":{"Player-1-X":{"guid":"Player-1-X","name":"X","firstSeen":"2026-01-01T00:00:00Z","lastSeen":"2026-01-01T00:00:00Z","events":3}}}`), 0o600)
	var msgs []string
	a, err := New(Config{LogsDir: dir, TailMB: 8, Orientation: "std", HistoryPath: hp, ExeDir: dir}, nil, func(f string, args ...any) { msgs = append(msgs, fmt.Sprintf(f, args...)) })
	if err != nil || strings.Contains(strings.Join(msgs, "|"), "history") || strings.Contains(strings.Join(msgs, "|"), "bounty") {
		t.Fatalf("%v %v", err, msgs)
	}
	if len(a.History.Snapshot()) != 1 || len(a.Bounty.Leaderboard(5)) != 0 {
		t.Fatal("old history must load; bounty starts empty")
	}
}

func TestCorruptBountyFileDoesNotPreventStart(t *testing.T) {
	dir := t.TempDir()
	hp := filepath.Join(dir, "h.json")
	_ = os.WriteFile(bounty.PathFor(hp), []byte("{oops"), 0o600)
	var msgs []string
	a, err := New(Config{LogsDir: dir, TailMB: 8, Orientation: "std", HistoryPath: hp, ExeDir: dir}, nil, func(f string, args ...any) { msgs = append(msgs, f) })
	if err != nil || a == nil || len(msgs) == 0 {
		t.Fatalf("%v %v", err, msgs)
	}
}
