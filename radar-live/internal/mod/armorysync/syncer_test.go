package armorysync

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"testing"

	"radarlive/internal/domain"
	"time"
)

type rig struct {
	src   *fakeSource
	seeds *fakeSeeds
	sink  *fakeSink
	clk   *fakeClock
	cfg   Config
	slept []time.Duration
}

func newRig(total, page int) *rig {
	r := &rig{src: &fakeSource{}, seeds: &fakeSeeds{pageSize: page}, sink: newSink(), clk: newClock()}
	for i := 0; i < total; i++ {
		r.seeds.items = append(r.seeds.items, charSeed(i))
	}
	r.cfg.Location = time.UTC
	r.cfg.AuthErr = errAuthFake
	r.cfg.Sleep = func(_ context.Context, d time.Duration) error {
		r.slept = append(r.slept, d)
		r.clk.Advance(d)
		return nil
	}
	return r
}

func (r *rig) syncer() *Syncer { return New(r.src, r.seeds, r.sink, r.clk, r.cfg) }

func TestFullRunBatchesAndOrder(t *testing.T) {
	r := newRig(5000, 250)
	r.cfg.Concurrency = 5
	r.src.onDetail = func(int, string) error {
		time.Sleep(time.Duration(rand.Intn(200)) * time.Microsecond)
		return nil
	}
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusOK || sum.ExitCode() != 0 {
		t.Fatalf("sum=%+v err=%v", sum, err)
	}
	if sum.Enumerated != 5000 || sum.Fetched != 5000 || sum.Stored != 5000 || sum.Failed != 0 {
		t.Fatalf("counters %+v", sum)
	}
	if len(r.sink.chars) != 5000 || len(r.sink.batches) != 60 {
		t.Fatalf("chars=%d batches=%d", len(r.sink.chars), len(r.sink.batches))
	}
	i := 0
	ids := map[string]bool{}
	for _, b := range r.sink.batches {
		if len(b.Characters) > 100 || ids[b.BatchID] || b.RunID != sum.RunID {
			t.Fatalf("bad batch %s len=%d", b.BatchID, len(b.Characters))
		}
		ids[b.BatchID] = true
		for _, c := range b.Characters {
			if c.Name != nameOf(i) {
				t.Fatalf("order: got %s want %s", c.Name, nameOf(i))
			}
			i++
		}
	}
	if last := r.sink.last; last.Status != "ok" || last.FinishedAt == 0 || last.Cursor != "" {
		t.Fatalf("final run %+v", last)
	}
}

func TestSecondRunIsNoOp(t *testing.T) {
	r := newRig(300, 100)
	first, _ := r.syncer().Run(context.Background())
	r.clk.Advance(time.Hour)
	second, err := r.syncer().Run(context.Background())
	if err != nil || second.Status != StatusOK {
		t.Fatalf("%+v %v", second, err)
	}
	if len(r.sink.chars) != 300 || second.Stored != 0 || second.Skipped != 300 {
		t.Fatalf("second run changed data: %+v", second)
	}
	if second.RunID == first.RunID {
		t.Fatal("finished run must not be resumed")
	}
}

func TestBatchIDDeterministic(t *testing.T) {
	a := batchID("r1", "10", 0, 5)
	if a != batchID("r1", "10", 0, 5) || a == batchID("r1", "10", 1, 5) || a == batchID("r2", "10", 0, 5) || a == batchID("r1", "10", 0, 4) {
		t.Fatal("batch id must be a pure function of its inputs")
	}
}

func TestKillThenResume(t *testing.T) {
	r := newRig(1000, 100)
	ctx, cancel := context.WithCancel(context.Background())
	r.cfg.Concurrency = 1
	r.src.onDetail = func(n int, _ string) error {
		if n == 450 {
			cancel()
		}
		return nil
	}
	sum, err := r.syncer().Run(ctx)
	if err != nil || sum.Status != StatusInterrupted || sum.ExitCode() != 1 {
		t.Fatalf("%+v %v", sum, err)
	}
	if r.sink.last.Status != "running" || r.sink.last.FinishedAt != 0 || r.sink.last.Cursor == "" {
		t.Fatalf("checkpoint %+v", r.sink.last)
	}
	saved := r.sink.last.Cursor
	r.clk.Advance(time.Hour)
	src2 := &fakeSource{}
	seeds2 := &fakeSeeds{items: r.seeds.items, pageSize: 100}
	sum2, err := New(src2, seeds2, r.sink, r.clk, r.cfg).Run(context.Background())
	if err != nil || sum2.Status != StatusOK || sum2.RunID != sum.RunID {
		t.Fatalf("resume %+v %v", sum2, err)
	}
	if base, n := parseCursor(saved); seeds2.cursors[0] != base || n != 0 {
		t.Fatalf("resumed at %q, saved %q", seeds2.cursors[0], saved)
	}
	if len(r.sink.chars) != 1000 {
		t.Fatalf("stored %d", len(r.sink.chars))
	}
	if src2.detail > 600 {
		t.Fatalf("refetched finished pages: %d details", src2.detail)
	}
}

func TestNewDayStartsNewRun(t *testing.T) {
	r := newRig(500, 100)
	ctx, cancel := context.WithCancel(context.Background())
	r.cfg.Concurrency = 1
	r.src.onDetail = func(n int, _ string) error {
		if n == 150 {
			cancel()
		}
		return nil
	}
	first, _ := r.syncer().Run(ctx)
	r.clk.Advance(25 * time.Hour)
	r.seeds = &fakeSeeds{items: r.seeds.items, pageSize: 100}
	second, _ := r.syncer().Run(context.Background())
	if second.RunID == first.RunID || r.seeds.cursors[0] != "" {
		t.Fatalf("expected a fresh run: %+v cursors=%v", second, r.seeds.cursors)
	}
}

func TestPoisonAndHiddenCharacters(t *testing.T) {
	r := newRig(100, 50)
	r.src.status = map[string]string{nameOf(7): "hidden", nameOf(8): "notfound"}
	r.src.onDetail = func(_ int, name string) error {
		if name == nameOf(3) {
			return errors.New("bad json")
		}
		return nil
	}
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusPartial || sum.ExitCode() != 1 {
		t.Fatalf("%+v %v", sum, err)
	}
	if sum.Failed != 1 || sum.Stored != 99 || len(r.sink.chars) != 99 {
		t.Fatalf("%+v", sum)
	}
	if r.sink.chars["Testrealm/"+nameOf(7)].Status != "hidden" || r.sink.chars["Testrealm/"+nameOf(8)].Status != "notfound" {
		t.Fatal("hidden/notfound must be stored as such")
	}
}

func TestAuthErrorIsFatal(t *testing.T) {
	r := newRig(100, 50)
	r.src.onDetail = func(n int, _ string) error {
		if n == 10 {
			return errAuthFake
		}
		return nil
	}
	sum, err := r.syncer().Run(context.Background())
	if err == nil || !errors.Is(err, ErrFatal) || sum.Status != StatusFailed || sum.ExitCode() != 2 {
		t.Fatalf("%+v %v", sum, err)
	}
	if r.sink.last.Status != "failed" || r.sink.last.FinishedAt == 0 {
		t.Fatalf("%+v", r.sink.last)
	}
}

func TestAuthErrorOnSeedsIsFatal(t *testing.T) {
	r := newRig(100, 50)
	r.seeds.err = errAuthFake
	sum, err := r.syncer().Run(context.Background())
	if !errors.Is(err, ErrFatal) || sum.ExitCode() != 2 {
		t.Fatalf("%+v %v", sum, err)
	}
}

func TestSeedFailureIsPartial(t *testing.T) {
	r := newRig(100, 50)
	r.cfg.Attempts = 2
	r.seeds.err = errors.New("boom")
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusPartial || sum.Note == "" {
		t.Fatalf("%+v %v", sum, err)
	}
}

func TestWallClockBudget(t *testing.T) {
	r := newRig(1000, 25)
	r.cfg.Concurrency = 1
	r.cfg.MaxDuration = time.Hour
	r.src.onDetail = func(int, string) error { r.clk.Advance(time.Minute); return nil }
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusPartial {
		t.Fatalf("%+v %v", sum, err)
	}
	if sum.Fetched > 61 || sum.Fetched < 59 || sum.Duration > 61*time.Minute {
		t.Fatalf("budget not honoured: %+v", sum)
	}
	if r.sink.last.Cursor == "" {
		t.Fatal("cursor must be kept")
	}
}

func TestRequestBudget(t *testing.T) {
	r := newRig(1000, 100)
	r.cfg.MaxRequests = 50
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusPartial {
		t.Fatalf("%+v %v", sum, err)
	}
	if got := r.src.detail; got != 50 {
		t.Fatalf("requests=%d, want exactly 50", got)
	}
	if sum.Stored != len(r.sink.chars) || sum.Stored != r.src.detail {
		t.Fatalf("fetched prefix must be stored: %+v detail=%d", sum, r.src.detail)
	}
}

func TestSinkRetriedThenSucceeds(t *testing.T) {
	r := newRig(100, 100)
	r.cfg.Backoff = time.Second
	r.sink.failN = 2
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusOK || sum.Stored != 100 {
		t.Fatalf("%+v %v", sum, err)
	}
	if len(r.slept) != 2 || r.slept[1] <= r.slept[0] {
		t.Fatalf("backoff %v", r.slept)
	}
}

func TestSinkGivesUpIsPartial(t *testing.T) {
	r := newRig(300, 100)
	r.cfg.Attempts = 3
	r.sink.failN = 1000
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusPartial || sum.Stored != 0 {
		t.Fatalf("%+v %v", sum, err)
	}
	if r.sink.applies != 3 {
		t.Fatalf("attempts=%d", r.sink.applies)
	}
}

func TestSkipperSkipsKnown(t *testing.T) {
	r := newRig(100, 100)
	r.cfg.Skipper = skipEven{}
	sum, _ := r.syncer().Run(context.Background())
	if sum.Skipped != 50 || sum.Fetched != 50 || r.src.detail != 50 || len(r.sink.chars) != 50 {
		t.Fatalf("%+v detail=%d", sum, r.src.detail)
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	r := newRig(60, 60)
	r.cfg.Concurrency = 3
	var mu sync.Mutex
	var cur, peak int
	r.src.onDetail = func(int, string) error {
		mu.Lock()
		cur++
		peak = max(peak, cur)
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		cur--
		mu.Unlock()
		return nil
	}
	if _, err := r.syncer().Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if peak > 3 || peak < 2 {
		t.Fatalf("peak concurrency %d", peak)
	}
}

func TestCheckpointFailureIsNotOK(t *testing.T) {
	r := newRig(100, 100)
	r.sink.recErr = errors.New("db down")
	r.cfg.Attempts = 2
	sum, _ := r.syncer().Run(context.Background())
	if sum.Status == StatusOK {
		t.Fatalf("a run that cannot checkpoint must not be ok: %+v", sum)
	}
}

func TestExitCodes(t *testing.T) {
	for st, want := range map[string]int{StatusOK: 0, StatusPartial: 1, StatusInterrupted: 1, StatusFailed: 2, "": 2} {
		if got := (Summary{Status: st}).ExitCode(); got != want {
			t.Errorf("%q -> %d want %d", st, got, want)
		}
	}
}

type errSink struct{ *fakeSink }

func (errSink) LastRun(context.Context) (domain.ArmoryRun, bool, error) {
	return domain.ArmoryRun{}, false, errors.New("db down")
}

func TestLastRunErrorIsFatal(t *testing.T) {
	r := newRig(10, 10)
	sum, err := New(r.src, r.seeds, errSink{r.sink}, r.clk, r.cfg).Run(context.Background())
	if !errors.Is(err, ErrFatal) || sum.ExitCode() != 2 {
		t.Fatalf("%+v %v", sum, err)
	}
}

func TestStoppedByBudgetAndCancel(t *testing.T) {
	r := newRig(1000, 10)
	r.cfg.MaxRequests = 1
	sum, _ := r.syncer().Run(context.Background())
	if sum.Status != StatusPartial || sum.Note == "" {
		t.Fatalf("%+v", sum)
	}
	r2 := newRig(10, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sum, _ = r2.syncer().Run(ctx)
	if sum.Status != StatusInterrupted {
		t.Fatalf("%+v", sum)
	}
}

func TestSeedErrorAfterCancelIsInterrupted(t *testing.T) {
	r := newRig(10, 10)
	ctx, cancel := context.WithCancel(context.Background())
	r.seeds.err = context.Canceled
	cancel()
	if sum, _ := r.syncer().Run(ctx); sum.Status != StatusInterrupted {
		t.Fatalf("%+v", sum)
	}
}

func TestSinkFailureDuringCancelIsInterrupted(t *testing.T) {
	r := newRig(10, 10)
	ctx, cancel := context.WithCancel(context.Background())
	r.src.onDetail = func(n int, _ string) error {
		if n == 5 {
			cancel()
			r.sink.mu.Lock()
			r.sink.failN = 100
			r.sink.mu.Unlock()
		}
		return nil
	}
	sum, _ := r.syncer().Run(ctx)
	if sum.Status != StatusInterrupted {
		t.Fatalf("%+v", sum)
	}
}

func TestFinalRecordFailureDowngradesOK(t *testing.T) {
	r := newRig(10, 10)
	r.cfg.Attempts = 1
	fs := &flakySink{fakeSink: r.sink}
	sum, _ := New(r.src, r.seeds, fs, r.clk, r.cfg).Run(context.Background())
	if sum.Status != StatusPartial || sum.Note == "" {
		t.Fatalf("%+v", sum)
	}
}

// flakySink fails the RecordRun that carries a final (non-running) status.
type flakySink struct{ *fakeSink }

func (f *flakySink) RecordRun(ctx context.Context, r domain.ArmoryRun) error {
	if r.Status != domain.ArmoryRunRunning {
		return errors.New("db down")
	}
	return f.fakeSink.RecordRun(ctx, r)
}

func TestEmptyPageAdvancesCursor(t *testing.T) {
	r := newRig(100, 50)
	r.cfg.Skipper = skipAll{}
	sum, _ := r.syncer().Run(context.Background())
	if sum.Status != StatusOK || sum.Skipped != 100 || r.src.detail != 0 {
		t.Fatalf("%+v", sum)
	}
}

type skipAll struct{}

func (skipAll) Skip(context.Context, CharRef) bool { return true }

func TestDefaultsAndSleep(t *testing.T) {
	c := (Config{BatchSize: 1000}).withDefaults()
	if c.MaxDuration != 6*time.Hour || c.Concurrency != 3 || c.BatchSize != 100 || c.Attempts != 4 || c.Location == nil || c.Sleep == nil {
		t.Fatalf("%+v", c)
	}
	if err := sleepCtx(context.Background(), time.Microsecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); err == nil {
		t.Fatal("cancelled sleep must fail")
	}
}

func TestBudgetStaysSpent(t *testing.T) {
	b := &budget{clock: newClock(), deadline: newClock().Now(), max: 1}
	if b.take() || b.take() || b.why() == "" {
		t.Fatal("expired deadline must refuse every permit")
	}
}
