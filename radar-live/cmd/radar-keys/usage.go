package main

import (
	"encoding/json"
	"io"
	"os"
	"time"
)

type usageView struct {
	LastSeen   *time.Time `json:"lastSeen"`
	LinesToday int64      `json:"linesToday"`
	BytesToday int64      `json:"bytesToday"`
}

type usageFile struct {
	Version int                  `json:"version"`
	Keys    map[string]usageView `json:"keys"`
}

// readUsage reads keyusage.json (written by radar-ingest). A missing or invalid file gives no usage.
func readUsage(path string) map[string]usageView {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil {
		return nil
	}
	var u usageFile
	if json.Unmarshal(data, &u) != nil || u.Version != 1 {
		return nil
	}
	return u.Keys
}
