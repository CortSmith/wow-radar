// Package app wires parser, tailer, tracker, history, screenshot connector and server together.
package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nearbyradar/internal/ruleset"
)

// DefaultLogsDir is the stock WoW Forever logs folder.
const DefaultLogsDir = `C:\Program Files (x86)\World of Warcraft\_forever_\Logs`

// Config is the complete runtime configuration.
type Config struct {
	LogsDir      string
	FromStart    bool
	TailMB       int
	RealmSuffix  string
	TrackNPCs    bool
	NoApprox     bool
	Orientation  string // "std" or "alt"
	MapsDir      string
	HistoryPath  string
	Browser      bool // skip the native window, open the default browser
	NoUI         bool // headless: serve the API only (smoke tests)
	Debug        bool // verbose logging to stderr (console build)
	KeepAlive    bool // browser mode: do not exit when the last tab closes
	LogFile      string
	Stale        time.Duration
	Expire       time.Duration
	AnalyzeLogs  bool     // reverse-analyze every combat log in LogsDir at startup (background)
	AnalyzeFiles []string // extra explicit files to reverse-analyze at startup (--analyze-file, repeatable)
	Ruleset      string   // "auto" (detect from the log owner's realm) or normal|pvp|rp|rppvp
	ExeDir       string
}

// ParseFlags builds a Config from command-line args and environment (flag > env > default).
// Recognised environment: WOW_LOGS.
func ParseFlags(args []string, getenv func(string) string, exeDir string) (Config, error) {
	c := Config{ExeDir: exeDir}
	fs := flag.NewFlagSet("radar", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // the GUI build has no console; errors are returned instead
	logs := DefaultLogsDir
	if v := getenv("WOW_LOGS"); v != "" {
		logs = v
	}
	fs.StringVar(&c.LogsDir, "logs", logs, "folder holding WoWCombatLog*.txt (env WOW_LOGS)")
	fs.BoolVar(&c.FromStart, "from-start", false, "read the newest log from its beginning instead of its last --tail-mb")
	fs.IntVar(&c.TailMB, "tail-mb", 8, "how much of an existing log to read at startup")
	fs.StringVar(&c.RealmSuffix, "realm-suffix", "", "realm suffix to strip from names, e.g. -PvP-US (default: auto-detect)")
	fs.BoolVar(&c.TrackNPCs, "track-npcs", false, "also track hostile/neutral NPCs and pets (default: players only)")
	fs.BoolVar(&c.NoApprox, "no-approx", false, "disable approximate sightings near me")
	fs.StringVar(&c.Orientation, "orientation", "std", "std (X north/up, Y west/left) or alt (X right, Y up)")
	fs.StringVar(&c.MapsDir, "maps", getenv("WOW_MAPS"), "maps folder with index.json (default: .\\maps, ..\\maps)")
	fs.StringVar(&c.HistoryPath, "history", "", "history JSON file (default: next to the exe, else %APPDATA%\\NearbyRadar)")
	fs.BoolVar(&c.Browser, "browser", false, "open the default browser instead of the native window")
	fs.BoolVar(&c.Debug, "debug", false, "log to stderr (use the console build)")
	fs.BoolVar(&c.NoUI, "no-ui", false, "headless: serve the API only, open no window or browser")
	fs.BoolVar(&c.KeepAlive, "keep-alive", false, "browser mode: keep running after the tab is closed")
	fs.StringVar(&c.LogFile, "log-file", "", "write diagnostics to this file")
	fs.DurationVar(&c.Stale, "stale", 0, "mark pips stale after this long without events (default 5m)")
	fs.BoolVar(&c.AnalyzeLogs, "analyze-logs", false, "at startup, scan every combat log in --logs for bounty kills in the background (safe to repeat)")
	fs.Var((*stringList)(&c.AnalyzeFiles), "analyze-file", "also scan this combat log file for bounty kills at startup (repeatable)")
	fs.DurationVar(&c.Expire, "expire", 0, "remove pips after this long without events (default 15m)")
	fs.StringVar(&c.Ruleset, "ruleset", "auto", "server rule-set whose data is used: auto (detect from the log) | normal | pvp | rp | rppvp")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.Orientation != "std" && c.Orientation != "alt" {
		return c, fmt.Errorf("--orientation must be std or alt, got %q", c.Orientation)
	}
	if strings.EqualFold(strings.TrimSpace(c.Ruleset), "auto") || strings.TrimSpace(c.Ruleset) == "" {
		c.Ruleset = "auto"
	} else if id, ok := ruleset.Parse(c.Ruleset); ok {
		c.Ruleset = string(id)
	} else {
		return c, fmt.Errorf("--ruleset must be auto, normal, pvp, rp or rppvp, got %q", c.Ruleset)
	}
	if c.TailMB < 1 {
		return c, errors.New("--tail-mb must be >= 1")
	}
	if c.Stale > 0 && c.Expire > 0 && c.Expire < c.Stale {
		return c, errors.New("--expire must not be shorter than --stale")
	}
	return c, nil
}

// ResolveHistoryPath picks where the history file lives: an explicit override, else
// radar-history.json next to the exe when that folder is writable, else
// <appData>\NearbyRadar\radar-history.json.
func ResolveHistoryPath(exeDir, override, appData string) string {
	if override != "" {
		return override
	}
	if writable(exeDir) {
		return filepath.Join(exeDir, "radar-history.json")
	}
	return filepath.Join(appData, "NearbyRadar", "radar-history.json")
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".radar-w-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string { return fmt.Sprint([]string(*l)) }

func (l *stringList) Set(v string) error {
	if v == "" {
		return errors.New("empty path")
	}
	*l = append(*l, v)
	return nil
}
