package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// seededDB runs a server on a fresh database, stores one contact and stops it.
func seededDB(t *testing.T, dir, name, batch, contactName string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	s := startServer(t, "--db-path", path)
	if _, code := s.ping(batch, contactJSON("Player-1-"+contactName, contactName, 1, time.Now().UnixMilli(), 2)); code != 200 {
		t.Fatalf("ping = %d", code)
	}
	s.stop()
	return path
}

func fakeClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	now := start
	old := backupNow
	backupNow = func() time.Time { return now }
	t.Cleanup(func() { backupNow = old })
	return &now
}

func runCmd(args ...string) (int, string) {
	var errOut bytes.Buffer
	code := run(context.Background(), args, noEnv, io.Discard, &errOut)
	return code, errOut.String()
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestBackupKeepRotatesAndPrunesOldest(t *testing.T) {
	dir := t.TempDir()
	db := seededDB(t, dir, "live.db", "b1", "Ann")
	backups := filepath.Join(dir, "backups")
	clock := fakeClock(t, time.Date(2026, 5, 1, 3, 17, 0, 0, time.UTC))
	// Files the rotation must never touch.
	if err := os.MkdirAll(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"notes.txt", "radar-manual.db", "radar-20250101T000000Z.db.bak"} {
		if err := os.WriteFile(filepath.Join(backups, f), []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for range 5 {
		if code, msg := runCmd("backup", "--db-path", db, "--keep", "3", backups); code != 0 {
			t.Fatalf("backup exit %d: %s", code, msg)
		}
		*clock = clock.Add(24 * time.Hour)
	}
	got := listDir(t, backups)
	want := []string{"notes.txt", "radar-20250101T000000Z.db.bak", "radar-20260503T031700Z.db",
		"radar-20260504T031700Z.db", "radar-20260505T031700Z.db", "radar-manual.db"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("directory = %v\nwant       %v", got, want)
	}
	for _, name := range want {
		if !strings.HasPrefix(name, "radar-2026") {
			continue
		}
		p := filepath.Join(backups, name)
		if err := verifyDB(p); err != nil {
			t.Errorf("%s does not verify: %v", name, err)
		}
		if st, _ := os.Stat(p); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", name, st.Mode().Perm())
		}
	}
}

func TestBackupNeverOverwritesInTheSameSecond(t *testing.T) {
	dir := t.TempDir()
	db := seededDB(t, dir, "live.db", "b1", "Ann")
	backups := filepath.Join(dir, "backups")
	fakeClock(t, time.Date(2026, 5, 1, 3, 17, 0, 0, time.UTC))
	for range 3 {
		if code, msg := runCmd("backup", "--db-path", db, "--keep", "10", backups); code != 0 {
			t.Fatalf("exit %d: %s", code, msg)
		}
	}
	if got := listDir(t, backups); len(got) != 3 {
		t.Fatalf("expected 3 distinct files, got %v", got)
	}
	// Single-file mode refuses an existing target and leaves it intact.
	target := filepath.Join(dir, "single.db")
	if err := os.WriteFile(target, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, msg := runCmd("backup", "--db-path", db, target); code == 0 || !strings.Contains(msg, "overwrite") {
		t.Fatalf("exit %d msg %q, want refusal", code, msg)
	}
	if b, _ := os.ReadFile(target); string(b) != "precious" {
		t.Fatal("existing target was modified")
	}
}

func TestBackupFailingVerificationPublishesNothingAndKeepsOldBackups(t *testing.T) {
	dir := t.TempDir()
	db := seededDB(t, dir, "live.db", "b1", "Ann")
	backups := filepath.Join(dir, "backups")
	clock := fakeClock(t, time.Date(2026, 5, 1, 3, 17, 0, 0, time.UTC))
	if code, msg := runCmd("backup", "--db-path", db, "--keep", "1", backups); code != 0 {
		t.Fatalf("first backup: %s", msg)
	}
	*clock = clock.Add(time.Hour)
	old := verifyBackup
	verifyBackup = func(string) error { return errors.New("quick_check: simulated damage") }
	t.Cleanup(func() { verifyBackup = old })
	code, msg := runCmd("backup", "--db-path", db, "--keep", "1", backups)
	if code == 0 || !strings.Contains(msg, "verification") {
		t.Fatalf("exit %d msg %q, want a verification failure", code, msg)
	}
	if got := listDir(t, backups); len(got) != 1 || got[0] != "radar-20260501T031700Z.db" {
		t.Fatalf("a failed backup must leave the old one alone and no partial file: %v", got)
	}
}

func TestBackupKeepNeedsPositiveNumber(t *testing.T) {
	if code, _ := runCmd("backup", "--keep", "-1", t.TempDir()); code == 0 {
		t.Fatal("negative --keep must fail")
	}
}

func TestRestoreReplacesDatabaseAfterVerifying(t *testing.T) {
	dir := t.TempDir()
	db := seededDB(t, dir, "live.db", "b1", "Ann")
	good := filepath.Join(dir, "good.db")
	if code, msg := runCmd("backup", "--db-path", db, good); code != 0 {
		t.Fatalf("backup: %s", msg)
	}
	// The live database moves on (a second contact), then gets "damaged" with a stale WAL beside it.
	s := startServer(t, "--db-path", db)
	if _, code := s.ping("b2", contactJSON("Player-2-Bob", "Bob", 1, time.Now().UnixMilli(), 2)); code != 200 {
		t.Fatalf("ping = %d", code)
	}
	s.stop()
	if err := os.WriteFile(db+"-wal", []byte("stale wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeClock(t, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	var out bytes.Buffer
	if code := run(context.Background(), []string{"restore", "--db-path", db, good}, noEnv, &out, io.Discard); code != 0 {
		t.Fatalf("restore exit %d", code)
	}
	if !strings.Contains(out.String(), "pre-restore") {
		t.Fatalf("output should name the saved copy: %s", out.String())
	}
	if _, err := os.Stat(db + "-wal"); err == nil {
		t.Fatal("stale WAL must not stay beside the restored database")
	}
	aside := db + ".pre-restore-20260601T000000Z"
	for _, p := range []string{aside, aside + "-wal"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("previous state not kept: %v", err)
		}
	}
	srv := startServer(t, "--db-path", db)
	if code, data := srv.do("GET", "/api/v1/contacts?q=ann", ""); code != 200 || !strings.Contains(string(data), `"total":1`) {
		t.Fatalf("restored data: %d %s", code, data)
	}
	if code, data := srv.do("GET", "/api/v1/contacts?q=bob", ""); code != 200 || !strings.Contains(string(data), `"total":0`) {
		t.Fatalf("data from after the backup must be gone: %d %s", code, data)
	}
	if left := listDir(t, dir); strings.Contains(strings.Join(left, ","), "restore-tmp") {
		t.Fatalf("staging file left behind: %v", left)
	}
}

func TestRestoreRefusesWhileServerHoldsTheLock(t *testing.T) {
	dir := t.TempDir()
	db := seededDB(t, dir, "live.db", "b1", "Ann")
	good := filepath.Join(dir, "good.db")
	if code, msg := runCmd("backup", "--db-path", db, good); code != 0 {
		t.Fatalf("backup: %s", msg)
	}
	srv := startServer(t, "--db-path", db)
	if _, code := srv.ping("b2", contactJSON("Player-2-Bob", "Bob", 1, time.Now().UnixMilli(), 2)); code != 200 {
		t.Fatalf("ping = %d", code)
	}
	code, msg := runCmd("restore", "--db-path", db, good)
	if code == 0 || !strings.Contains(msg, "in use") {
		t.Fatalf("exit %d msg %q, want a refusal while the server runs", code, msg)
	}
	if c, data := srv.do("GET", "/api/v1/contacts?q=bob", ""); c != 200 || !strings.Contains(string(data), `"total":1`) {
		t.Fatalf("live data must be untouched: %d %s", c, data)
	}
}

func TestRestoreRefusesUnusableBackups(t *testing.T) {
	dir := t.TempDir()
	db := seededDB(t, dir, "live.db", "b1", "Ann")
	before, _ := os.ReadFile(db)
	garbage := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(garbage, []byte(strings.Repeat("not sqlite ", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	smashed := filepath.Join(dir, "smashed.db")
	raw, _ := os.ReadFile(db)
	for i := 2 * 4096; i < 6*4096 && i < len(raw); i++ {
		raw[i] = 0xA5
	}
	if err := os.WriteFile(smashed, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{"garbage": garbage, "smashed": smashed,
		"missing": filepath.Join(dir, "nope.db"), "itself": db} {
		t.Run(name, func(t *testing.T) {
			if code, msg := runCmd("restore", "--db-path", db, src); code == 0 || msg == "" {
				t.Fatalf("exit %d msg %q, want failure", code, msg)
			}
			if after, _ := os.ReadFile(db); !bytes.Equal(before, after) {
				t.Fatal("the database was modified by a refused restore")
			}
		})
	}
	if code, _ := runCmd("restore", "--db-path", db); code == 0 {
		t.Fatal("restore without a source must fail")
	}
}

func TestStatusReportsIntegrityOK(t *testing.T) {
	s := startServer(t, "--db-path", filepath.Join(t.TempDir(), "s.db"))
	code, data := s.do("GET", "/api/v1/status", "")
	if code != 200 || !strings.Contains(string(data), `"integrity":"ok"`) || !strings.Contains(string(data), `"lastCheck":1`) {
		t.Fatalf("status = %d %s", code, data)
	}
}
