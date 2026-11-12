package audit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Alert kinds.
const (
	AlertBanSurge    = "ban_surge"
	AlertDBDegraded  = "db_degraded"
	AlertDiskLow     = "disk_low"
	AlertRelayDrops  = "relay_drops"
	alertTimeout     = 5 * time.Second
	defaultCooldown  = 5 * time.Minute
	alertEnvKind     = "RADAR_ALERT_KIND"
	alertEnvText     = "RADAR_ALERT_TEXT"
	alertEnvCount    = "RADAR_ALERT_COUNT"
	placeholderKind  = "{kind}"
	placeholderText  = "{text}"
	placeholderCount = "{count}"
)

// Runner starts a program (no shell) with exactly the given environment and waits for it. The
// context carries the 5 second limit.
type Runner func(ctx context.Context, argv, env []string) error

// execRunner is the real Runner.
func execRunner(ctx context.Context, argv, env []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

// splitArgs splits a command line into arguments without a shell: whitespace separates, single
// quotes take everything literally, double quotes allow \" and \\ , a backslash elsewhere is literal
// (Windows paths). There is no expansion of variables, globs, pipes or redirections.
func splitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inArg := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
			inArg = true
		case c == '"':
			inArg = true
			closed := false
			for i++; i < len(s); i++ {
				if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
					i++
					cur.WriteByte(s[i])
					continue
				}
				if s[i] == '"' {
					closed = true
					break
				}
				cur.WriteByte(s[i])
			}
			if !closed {
				return nil, errors.New("unterminated double quote")
			}
		default:
			cur.WriteByte(c)
			inArg = true
		}
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}

// alerter runs the configured command when a threshold trips, at most once per cooldown per kind.
type alerter struct {
	argv     []string
	run      Runner
	now      func() time.Time
	cooldown time.Duration
	baseEnv  []string

	mu    sync.Mutex
	last  map[string]time.Time
	fired map[string]int64
	wg    sync.WaitGroup
}

func newAlerter(argv []string, run Runner, now func() time.Time, cooldown time.Duration) *alerter {
	if run == nil {
		run = execRunner
	}
	if now == nil {
		now = time.Now
	}
	if cooldown <= 0 {
		cooldown = defaultCooldown
	}
	return &alerter{argv: argv, run: run, now: now, cooldown: cooldown, baseEnv: minimalEnv(),
		last: map[string]time.Time{}, fired: map[string]int64{}}
}

// minimalEnv is all the child inherits: enough to find programs, nothing that could hold a secret.
func minimalEnv() []string {
	var env []string
	keys := []string{"PATH"}
	if runtime.GOOS == "windows" {
		keys = append(keys, "SystemRoot", "ComSpec", "PATHEXT")
	}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Fire starts the command for kind unless one ran within the cooldown. text must not contain secrets
// (callers pass fixed wording and numbers). It never blocks: the command runs in the background.
func (a *alerter) Fire(kind, text string, count int) bool {
	if a == nil || len(a.argv) == 0 {
		return false
	}
	now := a.now()
	a.mu.Lock()
	if prev, ok := a.last[kind]; ok && now.Sub(prev) < a.cooldown && !now.Before(prev) {
		a.mu.Unlock()
		return false
	}
	a.last[kind] = now
	a.fired[kind]++
	a.mu.Unlock()

	argv := make([]string, len(a.argv))
	repl := strings.NewReplacer(placeholderKind, kind, placeholderText, text, placeholderCount, strconv.Itoa(count))
	for i, arg := range a.argv {
		argv[i] = repl.Replace(arg)
	}
	env := append(append([]string(nil), a.baseEnv...), alertEnvKind+"="+kind, alertEnvText+"="+text, alertEnvCount+"="+strconv.Itoa(count))
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), alertTimeout)
		defer cancel()
		_ = a.run(ctx, argv, env) // the failure itself is reported by the caller's hook via Wait in tests only
	}()
	return true
}

// counts returns how many times each kind fired.
func (a *alerter) counts() map[string]int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]int64, len(a.fired))
	for k, v := range a.fired {
		out[k] = v
	}
	return out
}

// wait blocks until running commands finish (tests and shutdown).
func (a *alerter) wait() { a.wg.Wait() }

// describe names the program for logs without arguments, which may hold a webhook token.
func describe(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	name := argv[0]
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	return fmt.Sprintf("%s (+%d args)", name, len(argv)-1)
}
