package logparse

import (
	"context"
	"fmt"
	"sync"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

const (
	// ModuleName is the module and parser name.
	ModuleName = "logparse"
	// SettingZone names the time zone (IANA name) of log timestamps that carry no UTC offset.
	SettingZone = "logparse-zone"
	// SettingWindow is how far from now an event time may be (both directions).
	SettingWindow = "logparse-window"
	// SettingAllowReplay accepts events older than the window, for importing old logs.
	SettingAllowReplay = "logparse-allow-replay"

	defaultZoneName = "UTC"
)

// Module plugs the line parser into the Parsers socket. It accepts domain.RawLine values, parses
// them and feeds the resulting domain.Event to the same socket, where contactx and bountyx pick it up.
// Any other event type is ignored, so the module never re-parses its own output.
type Module struct {
	host *core.Host
	now  func() time.Time

	once sync.Once
	zone *time.Location
	err  error
}

// New returns the module.
func New() *Module { return &Module{now: time.Now} }

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares the zone setting and registers the parser.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	err := host.Config.Declare(core.Setting{Key: SettingZone, Kind: core.KindString, Default: defaultZoneName,
		Usage: "IANA time zone of log timestamps without a UTC offset"})
	if err != nil {
		return fmt.Errorf("logparse settings: %w", err)
	}
	for _, st := range []core.Setting{
		{Key: SettingWindow, Kind: core.KindDuration, Default: DefaultWindow.String(), Usage: "events further than this from the clock are rejected"},
		{Key: SettingAllowReplay, Kind: core.KindBool, Default: "false", Usage: "accept events older than the window (importing old logs)"},
	} {
		if err := host.Config.Declare(st); err != nil {
			return fmt.Errorf("logparse settings: %w", err)
		}
	}
	host.Parsers.Add(parser{m})
	return nil
}

// location resolves the configured zone once (the configuration is loaded after Init).
func (m *Module) location() (*time.Location, error) {
	m.once.Do(func() {
		name := m.host.Config.String(SettingZone)
		if name == "" {
			name = defaultZoneName
		}
		loc, err := time.LoadLocation(name)
		if err != nil {
			m.err = fmt.Errorf("logparse: zone %q: %w", name, err)
			return
		}
		m.zone = loc
	})
	return m.zone, m.err
}

// parser adapts the module to core.Parser.
type parser struct{ m *Module }

func (parser) Name() string { return ModuleName }

// Parse parses a domain.RawLine and feeds the Event to every parser; other values are ignored.
func (p parser) Parse(ctx context.Context, event any) error {
	line, ok := event.(domain.RawLine)
	if !ok {
		return nil
	}
	loc, err := p.m.location()
	if err != nil {
		return err
	}
	ev, err := ParseLine(line.Text, Options{FallbackZone: loc, Source: line.Source})
	if err != nil {
		return err
	}
	if line.Ruleset != "" {
		ev.Ruleset = line.Ruleset
	}
	cfg := p.m.host.Config
	if err := CheckEvent(ev, p.m.now(), Bounds{Window: cfg.Duration(SettingWindow), AllowReplay: cfg.Bool(SettingAllowReplay)}); err != nil {
		return err
	}
	SanitizeEvent(&ev)
	return p.m.host.Parsers.Feed(ctx, ev)
}
