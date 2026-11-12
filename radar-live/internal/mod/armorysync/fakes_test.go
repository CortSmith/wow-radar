package armorysync

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"radarlive/internal/domain"
)

var errAuthFake = errors.New("fake: key rejected")

// fakeSeeds serves a fixed seed list in pages; its cursor is the index of the next item. Real radar-db
// cursors are opaque, so tests also use a prefix to prove the syncer never interprets them.
type fakeSeeds struct {
	mu       sync.Mutex
	items    []domain.ArmorySeed
	pageSize int
	prefix   string
	cursors  []string
	limits   []int
	err      error
	onPage   func(n int) error
}

func (f *fakeSeeds) Seeds(ctx context.Context, cursor string, limit int) (domain.ArmorySeedPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursors = append(f.cursors, cursor)
	f.limits = append(f.limits, limit)
	if f.err != nil {
		return domain.ArmorySeedPage{}, f.err
	}
	if err := ctx.Err(); err != nil {
		return domain.ArmorySeedPage{}, err
	}
	if f.onPage != nil {
		if err := f.onPage(len(f.cursors)); err != nil {
			return domain.ArmorySeedPage{}, err
		}
	}
	start := 0
	if cursor != "" {
		start, _ = strconv.Atoi(cursor[len(f.prefix):])
	}
	size := f.pageSize
	if size <= 0 {
		size = len(f.items)
	}
	end := min(start+size, len(f.items))
	p := domain.ArmorySeedPage{Items: append([]domain.ArmorySeed(nil), f.items[min(start, end):end]...)}
	if end < len(f.items) {
		p.Next = f.prefix + strconv.Itoa(end)
	}
	return p, nil
}

func charSeed(i int) domain.ArmorySeed {
	return domain.ArmorySeed{Kind: domain.ArmorySeedCharacter, Realm: "Testrealm", Name: nameOf(i)}
}

func guildSeed(realm, guild string) domain.ArmorySeed {
	return domain.ArmorySeed{Kind: domain.ArmorySeedGuild, Realm: realm, Guild: guild}
}

// fakeSource serves details for any character and rosters from a map.
type fakeSource struct {
	mu       sync.Mutex
	detail   int
	onDetail func(n int, name string) error // may block, cancel or fail
	status   map[string]string              // name -> status to return
	rosters  map[string][]CharRef           // "realm/guild" -> members
	rosterEr map[string]error
	rosterOf []string // calls, in order
}

func (f *fakeSource) GuildRoster(ctx context.Context, realm, guild string) ([]CharRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := realm + "/" + guild
	f.rosterOf = append(f.rosterOf, key)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.rosterEr[key]; err != nil {
		return nil, err
	}
	return append([]CharRef(nil), f.rosters[key]...), nil
}

func (f *fakeSource) Character(ctx context.Context, realm, name string) (domain.ArmoryCharacter, error) {
	f.mu.Lock()
	f.detail++
	n, hook, st := f.detail, f.onDetail, f.status[name]
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.ArmoryCharacter{}, err
	}
	if hook != nil {
		if err := hook(n, name); err != nil {
			return domain.ArmoryCharacter{}, err
		}
	}
	if st == "" {
		st = domain.ArmoryStatusOK
	}
	return domain.ArmoryCharacter{Realm: realm, Name: name, Level: 60, Status: st}, nil
}

func nameOf(i int) string { return fmt.Sprintf("Hero%05d", i) }

// fakeSink stores characters by key, dedupes batches by ID and records everything.
type fakeSink struct {
	mu      sync.Mutex
	chars   map[string]domain.ArmoryCharacter
	seen    map[string]bool
	batches []domain.ArmoryBatch
	last    *domain.ArmoryRun
	failN   int // next failN ApplyBatch calls fail
	applies int
	recErr  error
}

func newSink() *fakeSink {
	return &fakeSink{chars: map[string]domain.ArmoryCharacter{}, seen: map[string]bool{}}
}

func (s *fakeSink) ApplyBatch(_ context.Context, b domain.ArmoryBatch) (domain.ArmoryApplyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applies++
	if s.failN > 0 {
		s.failN--
		return domain.ArmoryApplyResult{}, errors.New("sink: 503 degraded")
	}
	if s.seen[b.BatchID] {
		return domain.ArmoryApplyResult{Duplicate: true}, nil
	}
	s.seen[b.BatchID] = true
	s.batches = append(s.batches, b)
	var res domain.ArmoryApplyResult
	for _, c := range b.Characters {
		key := c.Realm + "/" + c.Name
		if _, ok := s.chars[key]; ok {
			res.Skipped++
			continue
		}
		s.chars[key] = c
		res.Stored++
	}
	return res, nil
}

func (s *fakeSink) RecordRun(_ context.Context, r domain.ArmoryRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recErr != nil {
		return s.recErr
	}
	c := r
	s.last = &c
	return nil
}

func (s *fakeSink) LastRun(context.Context) (domain.ArmoryRun, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return domain.ArmoryRun{}, false, nil
	}
	return *s.last, true, nil
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type skipEven struct{}

func (skipEven) Skip(_ context.Context, r CharRef) bool {
	n, _ := strconv.Atoi(r.Name[len("Hero"):])
	return n%2 == 0
}
