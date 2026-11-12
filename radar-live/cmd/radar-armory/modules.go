package main

import (
	"context"
	"net/http"
	"time"

	"radarlive/internal/domain"
	"radarlive/internal/mod/armoryclient"
	"radarlive/internal/mod/armorysync"
)

// This is the only file of the binary that imports module packages (architecture rule). It adapts the
// armory client to the crawler and builds the crawler from the validated configuration.

// testHooks replaces environment-dependent parts in tests; production passes the zero value.
type testHooks struct {
	allowHTTP bool // accept an http:// armory and token URL (httptest fake)
	transport http.RoundTripper
	clock     func() time.Time
	sleep     func(context.Context, time.Duration) error
}

// sourceAdapter turns armoryclient.Source into armorysync.Source (same shapes, different named types).
type sourceAdapter struct{ src armoryclient.Source }

func (a sourceAdapter) GuildRoster(ctx context.Context, realm, guild string) ([]armorysync.CharRef, error) {
	refs, err := a.src.GuildRoster(ctx, realm, guild)
	if err != nil {
		return nil, err
	}
	out := make([]armorysync.CharRef, len(refs))
	for i, r := range refs {
		out[i] = armorysync.CharRef{Realm: r.Realm, Name: r.Name}
	}
	return out, nil
}

func (a sourceAdapter) Character(ctx context.Context, realm, name string) (domain.ArmoryCharacter, error) {
	return a.src.Character(ctx, realm, name)
}

type clock struct{ now func() time.Time }

func (c clock) Now() time.Time { return c.now() }

// buildSyncer wires client, DB sink and crawler. It returns the sink so main can check its auth state.
func buildSyncer(c config, h testHooks) (*armorysync.Syncer, *dbSink, error) {
	if c.defaultRealm != "" {
		if _, ok := armoryclient.RealmSlug(c.defaultRealm); !ok {
			return nil, nil, cfgErr("the default realm must be in slug form (lowercase letters, digits and hyphens)")
		}
	}
	client, err := armoryclient.New(armoryclient.Config{
		BaseURL:          c.baseURL,
		Region:           c.region,
		Namespace:        c.namespace,
		RosterNamespace:  c.rosterNamespace,
		Locale:           c.locale,
		TokenURL:         c.tokenURL,
		ClientID:         armoryclient.NewSecret(c.clientID.Reveal()),
		ClientSecret:     armoryclient.NewSecret(c.apiKey.Reveal()),
		SummaryPath:      c.summaryPath,
		EquipmentPath:    c.equipmentPath,
		SpecsPath:        c.specsPath,
		StatsPath:        c.statsPath,
		PvPPath:          c.pvpPath,
		RosterPath:       c.rosterPath,
		MaxRosterMembers: c.maxGuildMembers,
		RatePerSecond:    c.rate,
	}, armoryclient.Options{AllowHTTP: h.allowHTTP, Transport: h.transport, Now: h.clock, Sleep: h.sleep})
	if err != nil {
		return nil, nil, cfgErr("%v", err)
	}
	sink, err := newDBSink(c.dbURL, c.dbKey, nil)
	if err != nil {
		return nil, nil, cfgErr("%v", err)
	}
	now := h.clock
	if now == nil {
		now = time.Now
	}
	syncer := armorysync.New(sourceAdapter{client}, sink, sink, clock{now}, armorysync.Config{
		MaxDuration:     c.maxRuntime,
		MaxRequests:     c.maxRequests,
		Concurrency:     c.concurrency,
		Location:        c.location,
		AuthErr:         armoryclient.ErrAuth,
		InvalidErr:      armoryclient.ErrBadName,
		Sleep:           h.sleep,
		DefaultRealm:    c.defaultRealm,
		SeedPageSize:    c.seedPageSize,
		MaxGuildMembers: c.maxGuildMembers,
	})
	return syncer, sink, nil
}

// summarize is the exit code and counters of a finished run, decoupled from armorysync's types.
type outcome struct {
	status                                       string
	runID, note                                  string
	enumerated, fetched, stored, skipped, failed int
	duration                                     time.Duration
	exit                                         int
}

// runSync executes one run and converts the result.
func runSync(ctx context.Context, s *armorysync.Syncer) (outcome, error) {
	sum, err := s.Run(ctx)
	return outcome{
		status: sum.Status, runID: sum.RunID, note: sum.Note, enumerated: sum.Enumerated, fetched: sum.Fetched,
		stored: sum.Stored, skipped: sum.Skipped, failed: sum.Failed, duration: sum.Duration, exit: sum.ExitCode(),
	}, err
}
