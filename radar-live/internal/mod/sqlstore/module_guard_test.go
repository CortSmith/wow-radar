package sqlstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

func TestModuleSafetySettingsAndEnvNames(t *testing.T) {
	host, _ := moduleHost(t, "--db-path", filepath.Join(t.TempDir(), "x.db"))
	for key, env := range map[string]string{
		KeyMaxDBMB: "RADAR_DB_MAX_MB", KeyMinFreeMB: "RADAR_DB_MIN_FREE_MB", KeyIntegrityEvery: "RADAR_DB_INTEGRITY_EVERY",
	} {
		if got := host.Config.EnvName(key); got != env {
			t.Errorf("env name of %s = %s, want %s", key, got, env)
		}
	}
	if host.Config.Int(KeyMaxDBMB) != 2048 || host.Config.Int(KeyMinFreeMB) != 200 || host.Config.Duration(KeyIntegrityEvery) != 24*time.Hour {
		t.Fatal("defaults must be 2048 MiB, 200 MiB and 24h")
	}
}

func TestModuleHoldsServerLockAndExposesHealth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	host, m := moduleHost(t, "--db-path", path)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("lock while serving = %v, want ErrLocked", err)
	}
	st, _ := host.Stores.Get(StoreName)
	hs, ok := st.(interface{ Health() domain.Health })
	if !ok || hs.Health().Integrity != domain.IntegrityOK {
		t.Fatalf("store must expose Health, got %T", st)
	}
	if err := host.Stores.CloseAll(); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("lock after close: %v", err)
	}
	_ = release()
}

func TestModuleMetricsAndAuditSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	host, m := moduleHost(t, "--db-path", path)
	val := func(name string) float64 { v, _ := host.Metrics.Value(name); return v }
	if val("radar_db_size_bytes") != 0 {
		t.Fatal("size reported before the database opened")
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer host.Stores.CloseAll()
	if val("radar_db_size_bytes") <= 0 || val("radar_db_integrity_degraded") != 0 || val("radar_db_writes_paused") != 0 || val("radar_db_last_write_age_seconds") != -1 {
		t.Fatalf("fresh metrics: size %v degraded %v paused %v age %v", val("radar_db_size_bytes"),
			val("radar_db_integrity_degraded"), val("radar_db_writes_paused"), val("radar_db_last_write_age_seconds"))
	}
	st, _ := host.Stores.Default()
	if _, err := st.ApplyPing(context.Background(), contactPing("m1", 1, "Alice")); err != nil {
		t.Fatal(err)
	}
	if age := val("radar_db_last_write_age_seconds"); age < 0 || age > 60 {
		t.Fatalf("age after a write = %v", age)
	}
	var got []core.AuditEvent
	host.Audit.Observe(func(ev core.AuditEvent) { got = append(got, ev) })
	lazy := st.(*lazyStore)
	lazy.store.runCheck = func(context.Context, string) (string, error) { return "corrupt", nil }
	_ = lazy.store.CheckIntegrity(context.Background(), false)
	if val("radar_db_integrity_degraded") != 1 {
		t.Fatal("degraded gauge not set")
	}
	if len(got) != 1 || got[0].Event != "integrity_degraded" || got[0].Class != "db" || got[0].Reason != "quick_check" {
		t.Fatalf("events = %+v", got)
	}
}
