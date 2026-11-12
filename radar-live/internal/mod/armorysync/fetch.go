package armorysync

import (
	"context"
	"errors"
	"sync"
	"time"

	"radarlive/internal/domain"
)

// budget hands out request permits and enforces the wall-clock deadline; safe for concurrent use.
type budget struct {
	mu       sync.Mutex
	clock    Clock
	deadline time.Time
	max      int
	used     int
	reason   string
}

// take reserves one request; false means a budget is spent (reason says which).
func (b *budget) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.reason != "":
		return false
	case !b.clock.Now().Before(b.deadline):
		b.reason = "wall-clock budget spent"
	case b.max > 0 && b.used >= b.max:
		b.reason = "request budget spent"
	default:
		b.used++
		return true
	}
	return false
}

func (b *budget) why() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reason
}

type slotState int

const (
	slotNone    slotState = iota // not attempted (budget or cancel)
	slotOK                       // fetched
	slotFailed                   // poison: counted and skipped
	slotInvalid                  // malformed seed refused client-side: counted as skipped
	slotFatal                    // auth
)

type slot struct {
	state slotState
	char  domain.ArmoryCharacter
}

func (s *Syncer) isAuth(err error) bool {
	return errors.Is(err, ErrAuth) || (s.cfg.AuthErr != nil && errors.Is(err, s.cfg.AuthErr))
}

func (s *Syncer) isInvalid(err error) bool {
	return s.cfg.InvalidErr != nil && errors.Is(err, s.cfg.InvalidErr)
}

func (st *runState) fetchOne(ctx context.Context, ref CharRef) slot {
	c, err := st.s.src.Character(ctx, ref.Realm, ref.Name)
	switch {
	case err == nil:
		return slot{state: slotOK, char: c}
	case st.s.isAuth(err):
		return slot{state: slotFatal}
	case ctx.Err() != nil:
		return slot{}
	case st.s.isInvalid(err):
		return slot{state: slotInvalid}
	}
	return slot{state: slotFailed}
}

// fetchPage reads the details of refs with a bounded worker pool. The result keeps page order: it is the
// longest prefix whose fetches were all attempted. complete is false when a budget or cancel cut it short.
func (st *runState) fetchPage(ctx context.Context, refs []CharRef) (chars []domain.ArmoryCharacter, complete bool, fatal bool) {
	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	slots := make([]slot, len(refs))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < st.s.cfg.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				slots[i] = st.fetchOne(fctx, refs[i])
				if slots[i].state == slotFatal {
					cancel()
				}
			}
		}()
	}
dispatch:
	for i := range refs {
		if !st.budget.take() { // permits are taken in page order so the fetched part is always a prefix
			break
		}
		select {
		case jobs <- i:
		case <-fctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	return st.collect(slots)
}

// collect counts the attempted prefix and returns its characters.
func (st *runState) collect(slots []slot) (chars []domain.ArmoryCharacter, complete bool, fatal bool) {
	for _, sl := range slots {
		if sl.state == slotFatal {
			return nil, false, true
		}
	}
	for _, sl := range slots {
		switch sl.state {
		case slotNone:
			return chars, false, false
		case slotFailed:
			st.run.Failed++
		case slotInvalid:
			st.run.Skipped++
		case slotOK:
			st.run.Fetched++
			chars = append(chars, sl.char)
		}
	}
	return chars, true, false
}

// timeUp reports (and remembers) that the wall-clock budget is spent, without using a request permit.
func (b *budget) timeUp() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reason == "" && !b.clock.Now().Before(b.deadline) {
		b.reason = "wall-clock budget spent"
	}
	return b.reason != ""
}
