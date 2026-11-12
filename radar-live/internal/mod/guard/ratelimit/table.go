package ratelimit

import (
	"hash/maphash"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// class is a route class with its own budget.
type class uint8

// Route classes. Keep numClasses last.
const (
	classPage     class = iota // the web page and its static assets
	classSweep                 // the sweep query the page polls
	classSearch                // contacts, trail, bounty, charts: the scrapable queries
	classWrite                 // the internal write API
	classIngest                // /v1/stream and /v1/lines
	classAuthFail              // failed authentication attempts per client address
	numClasses
)

var classNames = [numClasses]string{"page", "sweep", "search", "write", "ingest", "authfail"}

func (c class) String() string {
	if c < numClasses {
		return classNames[c]
	}
	return "unknown"
}

// Identity kinds inside the table.
const (
	kindIP  uint8 = 0
	kindKey uint8 = 1
)

// budget is a token bucket's parameters: refill rate in tokens per second and capacity.
type budget struct{ rate, burst float64 }

// entryKey identifies one bucket. It is a fixed-size comparable value, so tracking an identity
// never allocates: id is the masked client address or the SHA-256-derived key id.
type entryKey struct {
	class class
	kind  uint8
	id    [16]byte
}

// entry is the state of one bucket.
type entry struct {
	tokens float64
	last   int64 // unix nanoseconds of the last refill
}

// decision is the outcome of one take.
type decision struct {
	allowed   bool
	remaining int           // whole tokens left after this request
	retry     time.Duration // when denied: time until one token is available
	burst     int
}

const (
	defaultShards = 64
	// overflowFactor sizes the shared bucket used when the table is full: it admits this many times
	// one identity's budget, for ALL untracked identities together.
	overflowFactor = 10
	maxSweepEvery  = time.Second
)

// shard is one lock-striped slice of the table.
type shard struct {
	mu        sync.Mutex
	m         map[entryKey]entry
	lastSweep int64
}

// sharedBucket is the fail-safe bucket for identities that cannot be tracked.
type sharedBucket struct {
	mu sync.Mutex
	e  entry
	ok bool
}

// table is a bounded, sharded set of token buckets. Memory is fixed: at most maxEntries buckets are
// tracked (rounded down to a multiple of the shard count); when a shard is full, expired buckets are
// evicted first and, failing that, new identities share one bucket per class and kind instead of
// allocating, so a flood of distinct addresses can neither grow the heap nor evade the limits.
type table struct {
	seed       maphash.Seed
	shards     []*shard
	shardCap   int
	ttl        int64
	sweepEvery int64
	overflow   [int(numClasses) * 2]sharedBucket
	overHits   atomic.Uint64
}

func newTable(maxEntries int, ttl time.Duration) *table {
	return newTableShards(maxEntries, ttl, defaultShards)
}

func newTableShards(maxEntries int, ttl time.Duration, shards int) *table {
	if shards > maxEntries {
		shards = maxEntries
	}
	if shards < 1 {
		shards = 1
	}
	t := &table{seed: maphash.MakeSeed(), shardCap: max(1, maxEntries/shards), ttl: int64(ttl)}
	t.sweepEvery = min(int64(maxSweepEvery), t.ttl/4)
	t.shards = make([]*shard, shards)
	for i := range t.shards {
		t.shards[i] = &shard{m: make(map[entryKey]entry)}
	}
	return t
}

// Len returns the number of tracked buckets.
func (t *table) Len() int {
	n := 0
	for _, s := range t.shards {
		s.mu.Lock()
		n += len(s.m)
		s.mu.Unlock()
	}
	return n
}

// overflowHits counts decisions made by the shared fail-safe buckets.
func (t *table) overflowHits() uint64 { return t.overHits.Load() }

func (t *table) shardFor(k entryKey) *shard {
	var h maphash.Hash
	h.SetSeed(t.seed)
	_ = h.WriteByte(byte(k.class))
	_ = h.WriteByte(k.kind)
	_, _ = h.Write(k.id[:])
	return t.shards[h.Sum64()%uint64(len(t.shards))]
}

// take refills the bucket of k and, when consume is true, removes one token. With consume false it
// only reports whether a token is available and never creates an entry.
func (t *table) take(k entryKey, b budget, now time.Time, consume bool) decision {
	s := t.shardFor(k)
	ns := now.UnixNano()
	s.mu.Lock()
	e, found := s.m[k]
	if !found {
		if !consume {
			s.mu.Unlock()
			return decision{allowed: true, remaining: int(b.burst), burst: int(b.burst)}
		}
		if len(s.m) >= t.shardCap {
			t.sweep(s, ns)
		}
		if len(s.m) >= t.shardCap {
			s.mu.Unlock()
			return t.takeOverflow(k, b, ns)
		}
		e = entry{tokens: b.burst, last: ns}
	}
	d := advance(&e, b, ns, consume)
	if consume || found {
		s.m[k] = e
	}
	s.mu.Unlock()
	return d
}

// sweep drops buckets idle for longer than the TTL; it runs at most once per sweepEvery per shard.
// The caller holds s.mu.
func (t *table) sweep(s *shard, ns int64) {
	if ns-s.lastSweep < t.sweepEvery && s.lastSweep != 0 {
		return
	}
	s.lastSweep = ns
	for k, e := range s.m {
		if ns-e.last > t.ttl {
			delete(s.m, k)
		}
	}
}

func (t *table) takeOverflow(k entryKey, b budget, ns int64) decision {
	t.overHits.Add(1)
	sb := &t.overflow[int(k.class)*2+int(k.kind&1)]
	sb.mu.Lock()
	defer sb.mu.Unlock()
	big := budget{rate: b.rate * overflowFactor, burst: b.burst * overflowFactor}
	if !sb.ok {
		sb.e, sb.ok = entry{tokens: big.burst, last: ns}, true
	}
	return advance(&sb.e, big, ns, true)
}

// advance refills e to ns (never going backwards) and takes a token when consume is set.
func advance(e *entry, b budget, ns int64, consume bool) decision {
	if elapsed := ns - e.last; elapsed > 0 {
		e.tokens = math.Min(b.burst, e.tokens+float64(elapsed)/1e9*b.rate)
		e.last = ns
	}
	d := decision{burst: int(b.burst)}
	if e.tokens >= 1 {
		d.allowed = true
		if consume {
			e.tokens--
		}
		d.remaining = int(e.tokens)
		return d
	}
	secs := (1 - e.tokens) / b.rate
	d.retry = time.Duration(math.Ceil(secs)) * time.Second
	return d
}
