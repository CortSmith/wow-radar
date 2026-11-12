package bountyx

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// Module settings and limits.
const (
	// ModuleName is the module and parser name.
	ModuleName = "bountyx"
	// SettingFlushEvery is how often the module publishes pings.
	SettingFlushEvery = "bountyx-flush"
	// SettingRuleset is the ruleset used until a source's own realm token is seen.
	SettingRuleset = "bountyx-ruleset"
	// SettingRealmSuffix is the realm suffix stripped from names ("" = auto-detect from the stream).
	SettingRealmSuffix = "bountyx-realm-suffix"

	defaultFlushEvery = 10 * time.Second
	maxSources        = 256
	// idleExpiry is how long a silent source is kept before it is forgotten (after its last flush).
	idleExpiry       = 15 * time.Minute
	sourceIDMaxChars = 24
)

// Module plugs the Analyzer into the Parsers socket and publishes deaths and bounty credits to the
// Sinks socket. Events arrive as domain.Event (from logparse); each Event.Source gets its own Analyzer.
type Module struct {
	host *core.Host
	now  func() time.Time

	mu      sync.Mutex
	sources map[string]*Analyzer
	fed     map[string]time.Time
	seq     uint64
	runID   string

	cancel context.CancelFunc
	done   chan struct{}
}

// New returns the module.
func New() *Module {
	return &Module{now: time.Now, sources: map[string]*Analyzer{}, fed: map[string]time.Time{}}
}

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares settings and registers the parser.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	m.runID = strconv.FormatInt(m.now().UnixMilli(), 36)
	for _, s := range []core.Setting{
		{Key: SettingFlushEvery, Kind: core.KindDuration, Default: defaultFlushEvery.String(), Usage: "how often bounty pings are published"},
		{Key: SettingRuleset, Kind: core.KindString, Default: string(domain.DefaultRuleset()), Usage: "ruleset until a source's realm token is seen"},
		{Key: SettingRealmSuffix, Kind: core.KindString, Default: "", Usage: "realm suffix stripped from names, e.g. PvP-US (empty = auto-detect)"},
	} {
		if err := host.Config.Declare(s); err != nil {
			return fmt.Errorf("bountyx settings: %w", err)
		}
	}
	host.Parsers.Add(parser{m})
	return nil
}

// parser adapts the module to core.Parser.
type parser struct{ m *Module }

func (parser) Name() string { return ModuleName }

// Parse feeds domain.Event values to the source's analyzer; any other event type is ignored.
func (p parser) Parse(_ context.Context, event any) error {
	ev, ok := event.(domain.Event)
	if !ok {
		return nil
	}
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	a := p.m.sources[ev.Source]
	if a == nil {
		if len(p.m.sources) >= maxSources {
			return fmt.Errorf("bountyx: more than %d sources", maxSources)
		}
		a = NewAnalyzer(Options{RealmSuffix: p.m.host.Config.String(SettingRealmSuffix)})
		p.m.sources[ev.Source] = a
	}
	p.m.fed[ev.Source] = p.m.now()
	a.Feed(ev)
	return nil
}

// Start launches the flush loop.
func (m *Module) Start(ctx context.Context) error {
	every := m.host.Config.Duration(SettingFlushEvery)
	if every <= 0 {
		every = defaultFlushEvery
	}
	loopCtx, cancel := context.WithCancel(ctx)
	m.cancel, m.done = cancel, make(chan struct{})
	go m.loop(loopCtx, every)
	return nil
}

func (m *Module) loop(ctx context.Context, every time.Duration) {
	defer close(m.done)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.Flush(ctx); err != nil {
				m.host.Log.Warn("bountyx flush failed", slog.String("err", err.Error()))
			}
		}
	}
}

// Stop ends the loop and publishes what is left.
func (m *Module) Stop(ctx context.Context) error {
	if m.cancel != nil {
		m.cancel()
		<-m.done
	}
	return m.Flush(context.WithoutCancel(ctx))
}

// Flush publishes one ping per source with the deaths and credits since the previous Flush. Deltas
// are consumed even if a sink fails: the relay sink is responsible for buffering and retrying.
func (m *Module) Flush(ctx context.Context) error {
	type flushed struct {
		source string
		part   Part
	}
	var all []flushed
	m.mu.Lock()
	now := m.now()
	for source, a := range m.sources {
		all = append(all, flushed{source, a.Flush()})
		if fed, ok := m.fed[source]; ok && now.Sub(fed) > idleExpiry {
			delete(m.sources, source)
			delete(m.fed, source)
		}
	}
	m.mu.Unlock()
	var firstErr error
	for _, f := range all {
		for _, ping := range m.pings(f.source, f.part) {
			if err := m.host.Sinks.Publish(ctx, ping); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// pings turns a Part into valid pings: credits as kb/assist deltas, deaths as the ledger.
func (m *Module) pings(source string, part Part) []domain.Ping {
	rs := part.Ruleset
	if rs == "" {
		rs = m.defaultRuleset()
	}
	contacts, deaths := m.usable(rs, source, creditContacts(part.Credits), part.Deaths)
	part.Deaths = deaths
	var out []domain.Ping
	for first := true; first || len(contacts) > 0; first = false {
		n := min(len(contacts), domain.PingMaxContacts)
		p := domain.Ping{RS: rs, BatchID: m.batchID(source), Source: source, Contacts: contacts[:n]}
		contacts = contacts[n:]
		if first {
			p.Deaths = part.Deaths
		}
		p.Sanitize() // names come from the log: no control, bidi or over-long text reaches a ping
		if len(p.Contacts) > 0 || len(p.Deaths) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// creditContacts builds hostile-player contacts carrying only kb and assist deltas, sorted by guid.
func creditContacts(credits map[string]Credit) []domain.PingContact {
	guids := make([]string, 0, len(credits))
	for g := range credits {
		guids = append(guids, g)
	}
	slices.Sort(guids)
	out := make([]domain.PingContact, 0, len(guids))
	for _, g := range guids {
		c := credits[g]
		out = append(out, domain.PingContact{
			Contact: domain.Contact{GUID: g, Name: c.Name, Raw: c.Raw, Reaction: domain.ReactionHostile,
				FirstSeen: c.LastMs, LastSeen: c.LastMs},
			D: domain.Deltas{KB: clampCount(c.KB), Assists: clampCount(c.Assists)},
		})
	}
	return out
}

// usable repairs and checks every contact and death on its own, so one poisoned entry (an odd GUID,
// a 400-character name) is dropped or cleaned instead of making the whole ping invalid and lost.
func (m *Module) usable(rs domain.Ruleset, source string, contacts []domain.PingContact, deaths []domain.Death) ([]domain.PingContact, []domain.Death) {
	keptContacts := contacts[:0]
	var keptDeaths []domain.Death
	dropped := 0
	for _, c := range contacts {
		one := domain.Ping{RS: rs, BatchID: "check", Source: source, Contacts: []domain.PingContact{c}}
		one.Sanitize()
		if one.Validate() != nil {
			dropped++
			continue
		}
		keptContacts = append(keptContacts, one.Contacts[0])
	}
	for _, d := range deaths {
		if (domain.Ping{RS: rs, BatchID: "check", Source: source, Deaths: []domain.Death{d}}).Validate() != nil {
			dropped++
			continue
		}
		keptDeaths = append(keptDeaths, d)
	}
	if dropped > 0 && m.host != nil {
		m.host.Log.Warn("bountyx dropped invalid entries", slog.Int("count", dropped), slog.String("source", source))
	}
	return keptContacts, keptDeaths
}

func (m *Module) defaultRuleset() domain.Ruleset {
	if rs, err := domain.ParseRuleset(m.host.Config.String(SettingRuleset)); err == nil {
		return rs
	}
	return domain.DefaultRuleset()
}

// batchID is unique per run, source and flush: "bx-<source>-<run>-<n>", matching the ping id pattern.
func (m *Module) batchID(source string) string {
	m.mu.Lock()
	m.seq++
	n := m.seq
	m.mu.Unlock()
	return fmt.Sprintf("bx-%s-%s-%d", sanitizeID(source, sourceIDMaxChars), m.runID, n)
}

// sanitizeID keeps word characters, dots, colons and dashes of s (others become '_') and truncates it.
func sanitizeID(s string, limit int) string {
	out := make([]byte, 0, min(len(s), limit))
	for i := 0; i < len(s) && len(out) < limit; i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == ':', c == '-':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// clampCount keeps a counter inside what one ping may carry.
func clampCount(n int64) int64 { return min(max(n, 0), domain.DeltaMaxCount) }
