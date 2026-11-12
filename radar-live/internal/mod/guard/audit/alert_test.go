package audit

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		bad  bool
	}{
		{in: `notify-send radar "disk is low"`, want: []string{"notify-send", "radar", "disk is low"}},
		{in: `  a   b  `, want: []string{"a", "b"}},
		{in: `sh -c 'echo $HOME; rm -rf /'`, want: []string{"sh", "-c", "echo $HOME; rm -rf /"}},
		{in: `x "a \"quoted\" \\ b"`, want: []string{"x", `a "quoted" \ b`}},
		{in: `C:\tools\alert.exe --flag`, want: []string{`C:\tools\alert.exe`, "--flag"}},
		{in: `a ""`, want: []string{"a", ""}},
		{in: `a;b|c>d`, want: []string{"a;b|c>d"}},
		{in: `a "b`, bad: true},
		{in: `a 'b`, bad: true},
		{in: ``, want: nil},
	}
	for _, c := range cases {
		got, err := splitArgs(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("%q: want error", c.in)
			}
			continue
		}
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%q = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

type fakeRun struct {
	mu    sync.Mutex
	calls [][]string
	envs  [][]string
	err   error
	block chan struct{}
	ctxs  []context.Context
}

func (f *fakeRun) run(ctx context.Context, argv, env []string) error {
	f.mu.Lock()
	f.calls = append(f.calls, argv)
	f.envs = append(f.envs, env)
	f.ctxs = append(f.ctxs, ctx)
	f.mu.Unlock()
	if f.block != nil {
		<-ctx.Done()
	}
	return f.err
}

func TestAlerterRateLimitsPerKindAndPassesTextWithoutShell(t *testing.T) {
	c := newClock()
	f := &fakeRun{}
	a := newAlerter([]string{"/usr/bin/notify", "{kind}", "--msg={text}"}, f.run, c.now, 5*time.Minute)
	if !a.Fire(AlertBanSurge, "7 bans in 10m", 7) {
		t.Fatal("first alert suppressed")
	}
	if a.Fire(AlertBanSurge, "8 bans", 8) {
		t.Fatal("second ban_surge inside the cooldown fired")
	}
	if !a.Fire(AlertDBDegraded, "db degraded", 1) {
		t.Fatal("other kind blocked by ban_surge cooldown")
	}
	c.advance(5*time.Minute + time.Second)
	if !a.Fire(AlertBanSurge, "again", 9) {
		t.Fatal("alert after the cooldown suppressed")
	}
	a.wait()
	if len(f.calls) != 3 {
		t.Fatalf("calls = %v", f.calls)
	}
	if got := f.calls[0]; !slices.ContainsFunc(f.calls, func(c []string) bool {
		return slices.Equal(c, []string{"/usr/bin/notify", "ban_surge", "--msg=7 bans in 10m"})
	}) {
		t.Fatalf("argv = %q", got)
	}
	env := strings.Join(slices.Concat(f.envs...), "\n")
	for _, want := range []string{"RADAR_ALERT_KIND=ban_surge", "RADAR_ALERT_TEXT=7 bans in 10m", "RADAR_ALERT_COUNT=7"} {
		if !strings.Contains(env, want) {
			t.Fatalf("env lacks %q:\n%s", want, env)
		}
	}
	if got := a.counts(); got[AlertBanSurge] != 2 || got[AlertDBDegraded] != 1 {
		t.Fatalf("counts = %v", got)
	}
}

func TestAlerterChildDoesNotInheritSecretEnvironment(t *testing.T) {
	t.Setenv("RADAR_VIEW_KEYS", "test-key-0000000000000000")
	t.Setenv("RADAR_DB_KEY", "test-key-1111111111111111")
	f := &fakeRun{}
	a := newAlerter([]string{"x"}, f.run, nil, 0)
	a.Fire(AlertDiskLow, "disk", 1)
	a.wait()
	for _, e := range f.envs[0] {
		if strings.Contains(e, "test-key") || strings.HasPrefix(e, "RADAR_VIEW") || strings.HasPrefix(e, "RADAR_DB_KEY") {
			t.Fatalf("child env leaks %q", e)
		}
	}
}

func TestAlerterCommandRunsWithFiveSecondLimitAndFailureIsHarmless(t *testing.T) {
	f := &fakeRun{err: errors.New("exit 1")}
	a := newAlerter([]string{"x"}, f.run, nil, 0)
	a.Fire(AlertRelayDrops, "drops", 100)
	a.wait()
	dl, ok := f.ctxs[0].Deadline()
	if !ok || time.Until(dl) > alertTimeout+time.Second {
		t.Fatalf("deadline = %v, %v", dl, ok)
	}
}

func TestAlerterWithoutCommandDoesNothing(t *testing.T) {
	var a *alerter
	if a.Fire(AlertBanSurge, "x", 1) {
		t.Fatal("nil alerter fired")
	}
	if newAlerter(nil, nil, nil, 0).Fire(AlertBanSurge, "x", 1) {
		t.Fatal("alerter without argv fired")
	}
}

func TestDescribeHidesArguments(t *testing.T) {
	got := describe([]string{`/opt/hooks/notify`, "test-key-0000000000000000"})
	if strings.Contains(got, "test-key") || !strings.Contains(got, "notify") {
		t.Fatalf("describe = %q", got)
	}
}
