package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseFlagsRuleset(t *testing.T) {
	for in, want := range map[string]string{"": "auto", "auto": "auto", "AUTO": "auto", "normal": "normal", "PvP": "pvp", "rp": "rp", "rppvp": "rppvp", "rp-pvp": "rppvp"} {
		args := []string{}
		if in != "" {
			args = []string{"--ruleset", in}
		}
		c, err := ParseFlags(args, env(nil), ".")
		if err != nil || c.Ruleset != want {
			t.Errorf("%q: %q %v", in, c.Ruleset, err)
		}
	}
	if _, err := ParseFlags([]string{"--ruleset", "pve"}, env(nil), "."); err == nil {
		t.Fatal("invalid --ruleset must fail")
	}
}

func rpLog(now time.Time, tag string) string {
	ts := now.UTC().Format("1/2/2006 15:04:05.000") + "+0"
	me := `Player-1-0000ME01,"Mage-Realm-` + tag + `-US",0x511,0x80000000`
	foe := `Player-1-0000F003,"Rogue-Realm-` + tag + `-US",0x548,0x80000000`
	return strings.Join([]string{
		ts + `  COMBAT_LOG_VERSION,22,ADVANCED_LOG_ENABLED,1,BUILD_VERSION,1.60.1,PROJECT_ID,18`,
		ts + `  SPELL_DAMAGE,` + foe + `,` + me + `,9532,"Bolt",0x8,Player-1-0000ME01,0000000000000000,50,67,20,0,47,0,0,0,0,184,184,0,-5300.5,-200.25,1426,0.69,10,17,17,-1,8,0,0,0,nil,nil,nil,ST`,
		ts + `  UNIT_DIED,0000000000000000,nil,0x80000000,0x80000000,Player-1-0000A001,"Vic-Realm-` + tag + `-US",0x518,0x80000000,0`,
	}, "\n") + "\n"
}

func postRuleset(t *testing.T, url, body string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"api/ruleset", strings.NewReader(body))
	req.Header.Set("X-Radar", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

type rsStatus struct {
	Active, Mode string
	Detected     *string
	Rulesets     []struct {
		ID      string
		HasData bool
		Players int
	}
}

func TestRulesetAutoDetectedFromLogAndSeparateDatasets(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "Logs")
	_ = os.Mkdir(logs, 0o700)
	_ = os.WriteFile(filepath.Join(logs, "WoWCombatLog-111026_151053.txt"), []byte(rpLog(time.Now(), "RP")), 0o600)
	cfg := Config{LogsDir: logs, FromStart: true, TailMB: 8, Orientation: "std", HistoryPath: filepath.Join(dir, "h.json"), ExeDir: dir, Ruleset: "auto"}
	a, err := New(cfg, nil, func(f string, args ...any) { _ = fmt.Sprintf(f, args...) })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	url, err := a.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var st rsStatus
	waitFor(t, "rp detected", func() bool {
		st = rsStatus{}
		getJSON(t, url+"api/ruleset", &st)
		return st.Active == "rp" && st.Mode == "auto" && st.Detected != nil && *st.Detected == "rp"
	})
	var hist []struct{ GUID string }
	waitFor(t, "history in rp", func() bool {
		hist = nil
		getJSON(t, url+"api/history", &hist)
		return len(hist) >= 2
	})
	var board struct{ Stats struct{ Deaths int } }
	waitFor(t, "npc death in rp ledger", func() bool {
		getJSON(t, url+"api/bounty", &board)
		return board.Stats.Deaths == 1
	})
	// the live view is not per rule-set: switching keeps the pips
	var live struct {
		Pips []any
		View struct{ Ruleset struct{ Active, Mode string } }
	}
	getJSON(t, url+"api/state", &live)
	n := len(live.Pips)
	if n == 0 || live.View.Ruleset.Active != "rp" {
		t.Fatalf("%+v", live)
	}
	if code := postRuleset(t, url, `{"ruleset":"normal"}`); code != 200 {
		t.Fatalf("%d", code)
	}
	hist = nil
	getJSON(t, url+"api/history", &hist)
	live.Pips = nil
	getJSON(t, url+"api/state", &live)
	if len(hist) != 0 || len(live.Pips) < n || live.View.Ruleset.Active != "normal" {
		t.Fatalf("normal must be empty (hist %d) but radar keeps pips (%d vs %d)", len(hist), len(live.Pips), n)
	}
	getJSON(t, url+"api/bounty", &board)
	if board.Stats.Deaths != 0 {
		t.Fatal("normal ledger must be empty")
	}
	if code := postRuleset(t, url, `{"ruleset":"pve"}`); code != 400 {
		t.Fatalf("%d", code)
	}
	cancel()
	a.Wait()
	for _, f := range []string{"h-rp.json", "h-rp-bounty.json", "radar-settings.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "h-pvp.json")); err == nil {
		t.Error("pvp dataset must not exist: nothing was written there")
	}
}

func TestRulesetFlagForcesManual(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{LogsDir: dir, TailMB: 8, Orientation: "std", HistoryPath: filepath.Join(dir, "h.json"), ExeDir: dir, Ruleset: "rppvp"}
	a, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); a.Wait() }()
	url, _ := a.Start(ctx)
	var st rsStatus
	getJSON(t, url+"api/ruleset", &st)
	if st.Active != "rppvp" || st.Mode != "manual" {
		t.Fatalf("%+v", st)
	}
}

func TestNewRejectsBadRuleset(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(Config{LogsDir: dir, TailMB: 8, Orientation: "std", HistoryPath: filepath.Join(dir, "h.json"), ExeDir: dir, Ruleset: "zzz"}, nil, nil); err == nil {
		t.Fatal("bad ruleset must be an error")
	}
}

func TestRulesetFallsBackToSharedRealmSuffixWhenMeUnknown(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "Logs")
	_ = os.Mkdir(logs, 0o700)
	ts := time.Now().UTC().Format("1/2/2006 15:04:05.000") + "+0"
	lines := []string{ts + `  COMBAT_LOG_VERSION,22,ADVANCED_LOG_ENABLED,1,BUILD_VERSION,1.60.1,PROJECT_ID,18`}
	for i := 0; i < 8; i++ { // nobody carries the "mine" flag: only the shared realm token is known
		lines = append(lines, fmt.Sprintf(`%s  SPELL_DAMAGE,Player-1-0000F%03d,"Foe%c-RPPvP-US",0x548,0x80000000,Player-1-0000A%03d,"Tgt%c-RPPvP-US",0x548,0x80000000,1,"Bolt",0x8,1,1,1`, ts, i, 'a'+i, i, 'a'+i))
	}
	_ = os.WriteFile(filepath.Join(logs, "WoWCombatLog-111026_151053.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	a, _ := New(Config{LogsDir: logs, FromStart: true, TailMB: 8, Orientation: "std", HistoryPath: filepath.Join(dir, "h.json"), ExeDir: dir, Ruleset: "auto"}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); a.Wait() }()
	url, _ := a.Start(ctx)
	var st rsStatus
	waitFor(t, "rppvp hinted", func() bool {
		st = rsStatus{}
		getJSON(t, url+"api/ruleset", &st)
		return st.Active == "rppvp" && st.Mode == "auto"
	})
}
