package abuse

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"time"

	"radarlive/internal/netx"
)

// Defaults of the lockout policy.
const (
	DefaultMaxFailures = 5
	DefaultWindow      = 10 * time.Minute
	DefaultBaseBan     = time.Minute
	DefaultMaxBan      = 24 * time.Hour
	DefaultMaxEntries  = 10000
)

// Config is the lockout policy: MaxFailures failed key attempts within Window ban the address for
// BaseBan; every further ban doubles the duration up to MaxBan. At most MaxEntries addresses are tracked.
type Config struct {
	MaxFailures int
	Window      time.Duration
	BaseBan     time.Duration
	MaxBan      time.Duration
	MaxEntries  int
	// BanLoopback allows banning loopback addresses. Off by default: behind a same-host tunnel whose
	// proxy is not trusted every visitor looks like 127.0.0.1, and one guesser would lock everybody out.
	BanLoopback bool
}

// DefaultConfig returns the default policy.
func DefaultConfig() Config {
	return Config{MaxFailures: DefaultMaxFailures, Window: DefaultWindow, BaseBan: DefaultBaseBan,
		MaxBan: DefaultMaxBan, MaxEntries: DefaultMaxEntries}
}

// Validate rejects nonsensical values (the host then refuses to start instead of running unprotected).
func (c Config) Validate() error {
	switch {
	case c.MaxFailures < 1:
		return fmt.Errorf("abuse: max failures must be at least 1, got %d", c.MaxFailures)
	case c.Window <= 0:
		return fmt.Errorf("abuse: window must be positive, got %v", c.Window)
	case c.BaseBan <= 0:
		return fmt.Errorf("abuse: base ban must be positive, got %v", c.BaseBan)
	case c.MaxBan < c.BaseBan:
		return fmt.Errorf("abuse: max ban %v is shorter than base ban %v", c.MaxBan, c.BaseBan)
	case c.MaxEntries < 1:
		return fmt.Errorf("abuse: max entries must be at least 1, got %d", c.MaxEntries)
	}
	return nil
}

// Ban is one active ban as listed and persisted.
type Ban struct {
	IP      netip.Addr
	Until   time.Time
	Strikes int
}

// ErrExempt is returned when asked to ban an address the policy never bans.
var ErrExempt = errors.New("loopback addresses are exempt from bans")

type entry struct {
	fails       int
	windowStart time.Time
	strikes     int
	banUntil    time.Time
	lastSeen    time.Time
}

// Table tracks failed attempts and bans per client address (IPv6 aggregated to the /64). Memory is
// bounded by Config.MaxEntries. All methods are safe for concurrent use; time comes from now so
// tests can use a fake clock.
type Table struct {
	cfg   Config
	now   func() time.Time
	mu    sync.Mutex
	m     map[netip.Addr]*entry
	dirty bool
}

// NewTable creates a table; a nil now means time.Now.
func NewTable(cfg Config, now func() time.Time) *Table {
	if now == nil {
		now = time.Now
	}
	return &Table{cfg: cfg, now: now, m: map[netip.Addr]*entry{}}
}

func (t *Table) exempt(ip netip.Addr) bool { return ip.IsLoopback() && !t.cfg.BanLoopback }

// banFor is the ban duration after the given number of strikes: BaseBan doubled per extra strike, capped.
func (t *Table) banFor(strikes int) time.Duration {
	d := t.cfg.BaseBan
	for i := 1; i < strikes; i++ {
		if d >= t.cfg.MaxBan/2 {
			return t.cfg.MaxBan
		}
		d *= 2
	}
	return min(d, t.cfg.MaxBan)
}

// forgetAt is when an entry stops mattering: after its ban ends it is remembered for one more ban
// length (so retrying right after a ban escalates) or, with no ban, until its failure window ends.
func (t *Table) forgetAt(e *entry) time.Time {
	if !e.banUntil.IsZero() {
		return e.banUntil.Add(t.banFor(max(e.strikes, 1)))
	}
	return e.windowStart.Add(t.cfg.Window)
}

func banned(e *entry, now time.Time) bool { return now.Before(e.banUntil) }

// Banned reports whether ip is banned right now and until when.
func (t *Table) Banned(ip netip.Addr) (time.Time, bool) {
	if !ip.IsValid() || t.exempt(ip) {
		return time.Time{}, false
	}
	ip = netx.Aggregate(ip)
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[ip]
	if !ok || !banned(e, t.now()) {
		return time.Time{}, false
	}
	return e.banUntil, true
}

// Fail records one failed key attempt from ip and reports whether it triggered a ban.
func (t *Table) Fail(ip netip.Addr) bool {
	if !ip.IsValid() || t.exempt(ip) {
		return false
	}
	ip = netx.Aggregate(ip)
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entry(ip, now)
	if e == nil {
		return false // table full of active bans: the rate limiter's auth-fail budget still applies
	}
	e.lastSeen = now
	if banned(e, now) {
		return false
	}
	if e.fails == 0 || now.Sub(e.windowStart) > t.cfg.Window {
		e.fails, e.windowStart = 0, now
	}
	e.fails++
	if e.fails < t.cfg.MaxFailures {
		return false
	}
	e.fails = 0
	e.strikes++
	e.banUntil = now.Add(t.banFor(e.strikes))
	t.dirty = true
	return true
}

// entry returns the record of ip, creating it (and making room) when needed; nil when full of bans.
func (t *Table) entry(ip netip.Addr, now time.Time) *entry {
	if e, ok := t.m[ip]; ok {
		if now.After(t.forgetAt(e)) {
			*e = entry{}
		}
		return e
	}
	if len(t.m) >= t.cfg.MaxEntries {
		t.pruneLocked(now)
	}
	if len(t.m) >= t.cfg.MaxEntries && !t.evictOne(now) {
		return nil
	}
	e := &entry{}
	t.m[ip] = e
	return e
}

// evictOne drops one entry that is not under an active ban (map order is random, so this is cheap
// and does not favour any address).
func (t *Table) evictOne(now time.Time) bool {
	for k, e := range t.m {
		if !banned(e, now) {
			delete(t.m, k)
			return true
		}
	}
	return false
}

func (t *Table) pruneLocked(now time.Time) {
	for k, e := range t.m {
		if now.After(t.forgetAt(e)) {
			delete(t.m, k)
		}
	}
}

// Prune forgets expired entries.
func (t *Table) Prune() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(t.now())
}

// Len is the number of tracked addresses.
func (t *Table) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.m)
}

// Ban bans ip for d (an operator action). Strikes are untouched.
func (t *Table) Ban(ip netip.Addr, d time.Duration) error {
	if !ip.IsValid() {
		return errors.New("invalid address")
	}
	if t.exempt(ip) {
		return ErrExempt
	}
	ip = netx.Aggregate(ip)
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entry(ip, now)
	if e == nil {
		return errors.New("ban table is full of active bans")
	}
	e.banUntil, e.lastSeen = now.Add(d), now
	t.dirty = true
	return nil
}

// Unban lifts a ban and forgets the address; it reports whether it was banned.
func (t *Table) Unban(ip netip.Addr) bool {
	ip = netx.Aggregate(ip)
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[ip]
	if !ok {
		return false
	}
	was := banned(e, t.now())
	delete(t.m, ip)
	t.dirty = true
	return was
}

// Bans lists the active bans, soonest-expiring first.
func (t *Table) Bans() []Ban {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Ban
	for ip, e := range t.m {
		if banned(e, now) {
			out = append(out, Ban{IP: ip, Until: e.banUntil, Strikes: e.strikes})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Until.Equal(out[j].Until) {
			return out[i].Until.Before(out[j].Until)
		}
		return out[i].IP.Less(out[j].IP)
	})
	return out
}

// Replace makes bans the table's ban set: bans not listed are lifted (failure counts of other
// addresses are kept); expired or exempt entries are ignored. Used at start and when the file changes.
func (t *Table) Replace(bans []Ban) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.m {
		if banned(e, now) {
			e.banUntil = time.Time{}
			if e.fails == 0 {
				delete(t.m, k)
			}
		}
	}
	for _, b := range bans {
		if !b.IP.IsValid() || !now.Before(b.Until) || t.exempt(b.IP) {
			continue
		}
		e := t.entry(netx.Aggregate(b.IP), now)
		if e == nil {
			continue
		}
		e.banUntil, e.strikes, e.lastSeen = b.Until, max(b.Strikes, e.strikes), now
	}
}

// TakeDirty reports whether the ban set changed since the last call, and clears the flag.
func (t *Table) TakeDirty() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.dirty
	t.dirty = false
	return d
}
