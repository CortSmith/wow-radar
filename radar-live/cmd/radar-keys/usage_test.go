package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The exact JSON radar-ingest (streamapi) writes to keyusage.json must read back through readUsage.
func TestReadUsageParsesIngestFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyusage.json")
	doc := `{"version":1,"keys":{"0123456789abcdef0123456789abcdef":{"lastSeen":"2026-03-10T12:00:00+01:00","linesToday":15,"bytesToday":90}}}` + "\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	v, ok := readUsage(path)["0123456789abcdef0123456789abcdef"]
	if !ok || v.LinesToday != 15 || v.BytesToday != 90 || v.LastSeen == nil ||
		!v.LastSeen.Equal(time.Date(2026, 3, 10, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", v)
	}
}
