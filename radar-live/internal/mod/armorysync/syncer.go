package armorysync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"radarlive/internal/domain"
)

// Syncer runs the crawl. Build it with New; Run may be called once per process.
type Syncer struct {
	src   Source
	seeds SeedSource
	sink  Sink
	clock Clock
	cfg   Config
}

// New builds a Syncer; cfg zero values take their defaults.
func New(src Source, seeds SeedSource, sink Sink, clock Clock, cfg Config) *Syncer {
	return &Syncer{src: src, seeds: seeds, sink: sink, clock: clock, cfg: cfg.withDefaults()}
}

// runState is the mutable state of one Run.
type runState struct {
	s      *Syncer
	run    domain.ArmoryRun
	start  time.Time
	budget *budget
	note   string
	seen   map[string]bool // characters already queued in this invocation (realm and name keys)
}

// end describes how the crawl loop stopped.
type end struct {
	status string
	err    error
	note   string
}

// Run executes one crawl, resuming an unfinished run of the same local day. The error is non-nil only for
// a failed run (and wraps ErrFatal); partial and interrupted runs are reported through Summary.
func (s *Syncer) Run(ctx context.Context) (Summary, error) {
	st, err := s.begin(ctx)
	if err != nil {
		return Summary{Status: StatusFailed, Note: "cannot start"}, fmt.Errorf("%w: %v", ErrFatal, err)
	}
	e := st.crawl(ctx)
	return st.finish(e)
}

// begin loads the last run and resumes it or starts a new one, then records it as running.
func (s *Syncer) begin(ctx context.Context) (*runState, error) {
	now := s.clock.Now()
	last, found, err := s.sink.LastRun(ctx)
	if err != nil {
		return nil, fmt.Errorf("load last run: %w", err)
	}
	run := domain.ArmoryRun{ID: "r" + strconv.FormatInt(now.UnixMilli(), 10), StartedAt: now.UnixMilli(), Status: domain.ArmoryRunRunning}
	if found && s.resumable(last, now) {
		run = last
	}
	st := &runState{s: s, run: run, start: now, seen: map[string]bool{}, budget: &budget{clock: s.clock, deadline: now.Add(s.cfg.MaxDuration), max: s.cfg.MaxRequests}}
	if err := st.checkpoint(ctx, run.Cursor); err != nil {
		return nil, fmt.Errorf("record run: %w", err)
	}
	return st, nil
}

// resumable: unfinished and started on the same local day as now.
func (s *Syncer) resumable(last domain.ArmoryRun, now time.Time) bool {
	if last.Status != domain.ArmoryRunRunning || last.FinishedAt != 0 {
		return false
	}
	y1, m1, d1 := time.UnixMilli(last.StartedAt).In(s.cfg.Location).Date()
	y2, m2, d2 := now.In(s.cfg.Location).Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

// stopped explains an early stop: cancel first, else whichever budget ran out.
func (st *runState) stopped(ctx context.Context) end {
	if ctx.Err() != nil {
		return end{status: StatusInterrupted, note: "cancelled"}
	}
	return end{status: StatusPartial, note: st.budget.why()}
}

// filter drops characters the Skipper says are up to date, counting them.
func (st *runState) filter(ctx context.Context, refs []CharRef) []CharRef {
	if st.s.cfg.Skipper == nil {
		return refs
	}
	out := make([]CharRef, 0, len(refs))
	for _, r := range refs {
		if st.s.cfg.Skipper.Skip(ctx, r) {
			st.run.Skipped++
			continue
		}
		out = append(out, r)
	}
	return out
}

// applyPage sends chars in batches and checkpoints after each. Only the last batch moves the cursor to next.
func (st *runState) applyPage(ctx context.Context, chars []domain.ArmoryCharacter, cursor, next string) *end {
	size := st.s.cfg.BatchSize
	for i, idx := 0, 0; i < len(chars); i, idx = i+size, idx+1 {
		part := chars[i:min(i+size, len(chars))]
		after := cursor
		if i+size >= len(chars) {
			after = next
		}
		b := domain.ArmoryBatch{BatchID: batchID(st.run.ID, cursor, idx, len(part)), RunID: st.run.ID, Characters: part, Cursor: after}
		if e := st.applyBatch(ctx, b); e != nil {
			return e
		}
	}
	if len(chars) == 0 && next != cursor {
		if err := st.checkpoint(ctx, next); err != nil {
			return &end{status: StatusPartial, note: "checkpoint failed: " + err.Error()}
		}
	}
	return nil
}

// applyBatch stores one batch (retrying), then checkpoints. The sink calls ignore cancellation so a batch
// already in flight completes before the run stops.
func (st *runState) applyBatch(ctx context.Context, b domain.ArmoryBatch) *end {
	var res domain.ArmoryApplyResult
	err := st.retry(ctx, func(c context.Context) (err error) {
		res, err = st.s.sink.ApplyBatch(c, b)
		return err
	})
	if err != nil {
		return st.sinkEnd(ctx, "batch failed", err)
	}
	st.run.Stored += res.Stored
	st.run.Skipped += res.Skipped
	if err := st.checkpoint(ctx, b.Cursor); err != nil {
		return st.sinkEnd(ctx, "checkpoint failed", err)
	}
	return nil
}

func (st *runState) sinkEnd(ctx context.Context, what string, err error) *end {
	if ctx.Err() != nil {
		return &end{status: StatusInterrupted, note: "cancelled"}
	}
	return &end{status: StatusPartial, note: what + ": " + err.Error()}
}

// retry runs a sink call up to Attempts times with doubling backoff; it stops early when ctx is cancelled.
// The call itself ignores cancellation so a batch already in flight completes.
func (st *runState) retry(ctx context.Context, fn func(context.Context) error) error {
	return st.retryWith(ctx, context.WithoutCancel(ctx), fn)
}

// retryWith is retry with an explicit context for the calls.
func (st *runState) retryWith(ctx, call context.Context, fn func(context.Context) error) error {
	detached := call
	delay := st.s.cfg.Backoff
	var err error
	for attempt := 1; attempt <= st.s.cfg.Attempts; attempt++ {
		if err = fn(detached); err == nil {
			return nil
		}
		if attempt == st.s.cfg.Attempts || ctx.Err() != nil {
			break
		}
		if serr := st.s.cfg.Sleep(ctx, delay); serr != nil {
			return serr
		}
		delay *= 2
	}
	return err
}

// checkpoint records the running state with the given cursor.
func (st *runState) checkpoint(ctx context.Context, cursor string) error {
	st.run.Cursor = cursor
	st.run.Status = domain.ArmoryRunRunning
	st.run.FinishedAt = 0
	r := st.run
	return st.retry(ctx, func(c context.Context) error { return st.s.sink.RecordRun(c, r) })
}

// finish records the final state and builds the Summary.
func (st *runState) finish(e end) (Summary, error) {
	run := st.run
	status := e.status
	switch status {
	case StatusInterrupted:
		run.Status, run.FinishedAt = domain.ArmoryRunRunning, 0
	default:
		run.Status, run.FinishedAt = status, st.s.clock.Now().UnixMilli()
		if status == StatusOK && run.Failed > 0 {
			run.Status, status = domain.ArmoryRunPartial, StatusPartial
		}
	}
	if status == StatusOK {
		run.Cursor = ""
	}
	if err := st.s.sink.RecordRun(context.WithoutCancel(context.Background()), run); err != nil {
		e.note = "final record failed: " + err.Error()
		if status == StatusOK {
			status = StatusPartial
		}
	}
	sum := st.summary(run, status, e.note)
	if e.err != nil {
		return sum, fmt.Errorf("%w: %w", ErrFatal, e.err)
	}
	return sum, nil
}

func (st *runState) summary(run domain.ArmoryRun, status, note string) Summary {
	return Summary{
		RunID: run.ID, Status: status, Enumerated: run.Enumerated, Fetched: run.Fetched, Stored: run.Stored,
		Skipped: run.Skipped, Failed: run.Failed, Duration: st.s.clock.Now().Sub(st.start), Cursor: run.Cursor, Note: note,
	}
}

// batchID is a pure function of the run, the page cursor, the batch index and its size, so a replay of the
// same page after a crash sends the same ids (the size keeps a short prefix batch distinct from a full one).
func batchID(runID, cursor string, index, count int) string {
	sum := sha256.Sum256([]byte(runID + "|" + cursor + "|" + strconv.Itoa(index) + "|" + strconv.Itoa(count)))
	return hex.EncodeToString(sum[:16])
}
