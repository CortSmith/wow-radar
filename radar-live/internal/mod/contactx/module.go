package contactx

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// Module settings and limits.
const (
	// ModuleName is the module and parser name.
	ModuleName = "contactx"
	// SettingFlushEvery is how often the module publishes pings.
	SettingFlushEvery = "contactx-flush"
	// SettingRuleset is the ruleset used until a source's own realm token is seen.
	SettingRuleset = "contactx-ruleset"
	// SettingTrackNPCs also tracks non-player units in memory (they are never published).
	SettingTrackNPCs = "contactx-track-npcs"
	// SettingRealmSuffix is the realm suffix stripped from names ("" = auto-detect from the stream).
	SettingRealmSuffix = "contactx-realm-suffix"

	defaultFlushEvery = 10 * time.Second
	maxSources        = 256
	sourceIDMaxChars  = 24
)

// Module plugs the Extractor into the Parsers socket and publishes its output to the Sinks socket.
// Events arrive as domain.Event (from logparse); each Event.Source gets its own Extractor.
type Module struct {
	host *core.Host
	now  func() time.Time

	mu      sync.Mutex
	sources map[string]*Extractor
	fed     map[string]time.Time // when each source last sent an event, for expiry
	seq     uint64
	runID   string

	cancel context.CancelFunc
	done   chan struct{}
}

// New returns the module.
func New() *Module {
	return &Module{now: time.Now, sources: map[string]*Extractor{}, fed: map[string]time.Time{}}
}

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares settings and registers the parser.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	m.runID = strconv.FormatInt(m.now().UnixMilli(), 36)
	for _, s := range []core.Setting{
		{Key: SettingFlushEvery, Kind: core.KindDuration, Default: defaultFlushEvery.String(), Usage: "how often contact pings are published"},
		{Key: SettingRuleset, Kind: core.KindString, Default: string(domain.DefaultRuleset()), Usage: "ruleset until a source's realm token is seen"},
		{Key: SettingTrackNPCs, Kind: core.KindBool, Default: "false", Usage: "also track non-player units in memory"},
		{Key: SettingRealmSuffix, Kind: core.KindString, Default: "", Usage: "realm suffix stripped from names, e.g. PvP-US (empty = auto-detect)"},
	} {
		if err := host.Config.Declare(s); err != nil {
			return fmt.Errorf("contactx settings: %w", err)
		}
	}
	host.Parsers.Add(parser{m})
	return nil
}

// parser adapts the module to core.Parser.
type parser struct{ m *Module }

func (parser) Name() string { return ModuleName }

// Parse feeds domain.Event values to the source's extractor; any other event type is ignored.
func (p parser) Parse(_ context.Context, event any) error {
	ev, ok := event.(domain.Event)
	if !ok {
		return nil
	}
	return p.m.feed(ev)
}

func (m *Module) feed(ev domain.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.sources[ev.Source]
	if x == nil {
		if len(m.sources) >= maxSources {
			return fmt.Errorf("contactx: more than %d sources", maxSources)
		}
		x = NewExtractor(Options{TrackNPCs: m.trackNPCs(), RealmSuffix: m.host.Config.String(SettingRealmSuffix)})
		m.sources[ev.Source] = x
	}
	m.fed[ev.Source] = m.now()
	x.Feed(ev)
	return nil
}

func (m *Module) trackNPCs() bool { return m.host != nil && m.host.Config.Bool(SettingTrackNPCs) }

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
			m.report(m.Flush(ctx))
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

func (m *Module) report(err error) {
	if err != nil {
		m.host.Log.Warn("contactx flush failed", slog.String("err", err.Error()))
	}
}

// Flush publishes one ping per source (more when a source has over domain.PingMaxContacts changed
// contacts) with whatever changed since the previous Flush. Deltas are consumed even if a sink
// fails: the relay sink is responsible for buffering and retrying.
func (m *Module) Flush(ctx context.Context) error {
	m.mu.Lock()
	type flushed struct {
		source string
		part   Part
	}
	var all []flushed
	now := m.now()
	for source, x := range m.sources {
		all = append(all, flushed{source, x.Flush(now)})
		// A source that sent nothing for ExpireAfter is forgotten (after its last flush), so
		// throwaway source names cannot fill the table for good.
		if fed, ok := m.fed[source]; ok && now.Sub(fed) > ExpireAfter {
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

// pings turns a Part into valid pings: players only, at most PingMaxContacts each, charts in the first.
func (m *Module) pings(source string, part Part) []domain.Ping {
	var players []domain.PingContact
	for _, c := range part.Contacts {
		if domain.KindOf(c.GUID) == domain.KindPlayer {
			players = append(players, c)
		}
	}
	rs := part.Ruleset
	if rs == "" {
		rs = m.defaultRuleset()
	}
	players = m.usable(rs, source, players)
	var out []domain.Ping
	for first := true; first || len(players) > 0; first = false {
		n := min(len(players), domain.PingMaxContacts)
		p := domain.Ping{RS: rs, BatchID: m.batchID(source), Source: source, Contacts: players[:n]}
		players = players[n:]
		if first {
			p.Charts = part.Charts
		}
		p.Sanitize() // names come from the log: no control, bidi or over-long text reaches a ping
		if len(p.Contacts) > 0 || len(p.Charts) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// usable repairs and checks every contact on its own, so one poisoned entry (an odd GUID, a
// 400-character name) is dropped or cleaned instead of making the whole ping invalid and lost.
func (m *Module) usable(rs domain.Ruleset, source string, in []domain.PingContact) []domain.PingContact {
	out := in[:0]
	dropped := 0
	for _, c := range in {
		c.D = clampDeltas(c.D)
		one := domain.Ping{RS: rs, BatchID: "check", Source: source, Contacts: []domain.PingContact{c}}
		one.Sanitize()
		if one.Validate() != nil {
			dropped++
			continue
		}
		out = append(out, one.Contacts[0])
	}
	if dropped > 0 && m.host != nil {
		m.host.Log.Warn("contactx dropped invalid contacts", slog.Int("count", dropped), slog.String("source", source))
	}
	return out
}

func (m *Module) defaultRuleset() domain.Ruleset {
	if rs, err := domain.ParseRuleset(m.host.Config.String(SettingRuleset)); err == nil {
		return rs
	}
	return domain.DefaultRuleset()
}

// batchID is unique per run, source and flush: "cx-<source>-<run>-<n>", matching the ping id pattern.
func (m *Module) batchID(source string) string {
	m.mu.Lock()
	m.seq++
	n := m.seq
	m.mu.Unlock()
	return fmt.Sprintf("cx-%s-%s-%d", sanitizeID(source, sourceIDMaxChars), m.runID, n)
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

// clampDeltas keeps every counter inside what one ping may carry, so a hostile log cannot make
// the whole ping invalid (and lost) or push absurd numbers into the database. Garbage floats become 0.
func clampDeltas(d domain.Deltas) domain.Deltas {
	count := func(n int64) int64 { return min(max(n, 0), domain.DeltaMaxCount) }
	sum := func(v float64) float64 {
		if math.IsNaN(v) || v < 0 {
			return 0
		}
		return min(v, domain.DeltaMaxValue)
	}
	return domain.Deltas{Events: count(d.Events), SrcEvents: count(d.SrcEvents), DmgTotal: sum(d.DmgTotal),
		DmgHits: count(d.DmgHits), DmgEvents: count(d.DmgEvents), HPSum: sum(d.HPSum), HPSamples: count(d.HPSamples),
		KB: count(d.KB), Assists: count(d.Assists)}
}
