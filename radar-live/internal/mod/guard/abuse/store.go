package abuse

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	fileVersion  = 1
	maxFileBytes = 4 << 20
	fileMode     = 0o600
)

type fileBan struct {
	IP      string    `json:"ip"`
	Until   time.Time `json:"until"`
	Strikes int       `json:"strikes,omitempty"`
}

type fileDoc struct {
	Version int       `json:"version"`
	Bans    []fileBan `json:"bans"`
}

// ErrCorrupt wraps every failure to understand a ban file.
var ErrCorrupt = errors.New("ban file is corrupt")

// SaveBans writes the ban list atomically (temporary file in the same directory, then rename) with
// mode 0600. It contains only addresses and expiry times, never keys.
func SaveBans(path string, bans []Ban) error {
	doc := fileDoc{Version: fileVersion, Bans: make([]fileBan, 0, len(bans))}
	for _, b := range bans {
		doc.Bans = append(doc.Bans, fileBan{IP: b.IP.String(), Until: b.Until.UTC(), Strikes: b.Strikes})
	}
	sort.Slice(doc.Bans, func(i, j int) bool { return doc.Bans[i].IP < doc.Bans[j].IP })
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("ban file directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".abuse-bans-*.tmp")
	if err != nil {
		return fmt.Errorf("write ban file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if err := tmp.Chmod(fileMode); err != nil && !errors.Is(err, os.ErrInvalid) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// LoadBans reads the ban list, dropping expired and unparsable entries. A missing file is an empty
// list; anything unreadable returns an error wrapping ErrCorrupt (callers start empty and keep going).
func LoadBans(path string, now time.Time) ([]Ban, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrCorrupt, maxFileBytes)
	}
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if doc.Version != fileVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrCorrupt, doc.Version)
	}
	var out []Ban
	for _, fb := range doc.Bans {
		ip, err := netip.ParseAddr(fb.IP)
		if err != nil || !now.Before(fb.Until) || fb.Strikes < 0 {
			continue
		}
		out = append(out, Ban{IP: ip.Unmap(), Until: fb.Until, Strikes: fb.Strikes})
	}
	return out, nil
}

// fileSignature identifies a version of the ban file cheaply (modification time and size).
func fileSignature(path string) (time.Time, int64) {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}, -1
	}
	return st.ModTime(), st.Size()
}
