package relay

import (
	"context"
	"strings"
	"testing"
	"time"

	"radarlive/internal/core"
)

func startedHost(t *testing.T, args ...string) (*core.Host, *Module) {
	t.Helper()
	h := core.NewHost(core.WithLogger(quietLog()))
	m := NewModule()
	if err := h.Register(m); err != nil {
		t.Fatal(err)
	}
	if err := h.Config.Load(args, func(k string) (string, bool) { return "abcdefabcdefabcdef", k == "RADAR_DB_KEY" }); err != nil {
		t.Fatal(err)
	}
	return h, m
}

func TestModuleForwardsPublishedPings(t *testing.T) {
	db := &fakeDB{}
	_, srv, _ := newRelay(t, db, nil)
	h, m := startedHost(t, "--db-url="+srv.URL)
	if h.Sinks.Len() != 1 {
		t.Fatal("sink not registered")
	}
	if err := h.Sinks.Publish(context.Background(), ping("early")); err == nil {
		t.Fatal("publishing before Start must report an error")
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Sinks.Publish(context.Background(), ping("cx-1")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return m.Stats().Sent == 1 })
	_ = h.Sinks.Publish(context.Background(), ping("cx-2"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.ids(); len(got) != 2 || got[1] != "cx-2" || db.keys[0] != "abcdefabcdefabcdef" {
		t.Fatalf("ids %v keys %v", got, db.keys)
	}
}

func TestModuleStartNeedsURL(t *testing.T) {
	_, m := startedHost(t)
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("expected error without --db-url")
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (m.Stats() != Stats{}) {
		t.Fatal("stats before start must be zero")
	}
}

func TestModuleDeclaresSettings(t *testing.T) {
	h, _ := startedHost(t, "--db-url=http://x", "--relay-buffer-pings=7")
	if h.Config.Int(SettingBufferPing) != 7 || h.Config.Duration(SettingTimeout) != defaultTimeout || h.Config.Int(SettingBatchPings) != defaultBatchPings {
		t.Fatal("settings not declared with defaults")
	}
	var _ core.Sink = moduleSink{}
}

func TestDBKeyCannotBeAFlag(t *testing.T) {
	h := core.NewHost(core.WithLogger(quietLog()))
	if err := h.Register(NewModule()); err != nil {
		t.Fatal(err)
	}
	err := h.Config.Load([]string{"--db-key=abcdefabcdefabcdef"}, func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "RADAR_DB_KEY") || strings.Contains(err.Error(), "abcdefabcdefabcdef") {
		t.Fatalf("err = %v", err)
	}
}
