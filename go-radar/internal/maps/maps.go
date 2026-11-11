// Package maps loads the optional zone-map index (maps/index.json + png files).
// The maps are never embedded in the executable.
package maps

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotFound means no maps folder with an index.json was found.
var ErrNotFound = errors.New("no maps folder found")

// Zone is one entry of index.json.
type Zone struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	File     string `json:"file"`
	W        int    `json:"w"`
	H        int    `json:"h"`
	Revealed bool   `json:"revealed"`
}

// Index is a loaded maps folder.
type Index struct {
	Dir   string
	zones map[string]Zone
	files map[string]bool
}

// Normalize lowercases name and keeps only [a-z0-9].
func Normalize(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Load reads dir/index.json.
func Load(dir string) (*Index, error) {
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, err
	}
	var raw struct {
		Zones map[string]Zone `json:"zones"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	ix := &Index{Dir: dir, zones: map[string]Zone{}, files: map[string]bool{}}
	for k, z := range raw.Zones {
		z.Key = k
		ix.zones[k] = z
		if z.File != "" && z.File == filepath.Base(z.File) && !strings.ContainsAny(z.File, `/\`) {
			ix.files[z.File] = true
		}
	}
	return ix, nil
}

// LoadFirst loads the first directory in dirs that contains a readable index.json.
func LoadFirst(dirs []string) (*Index, error) {
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if ix, err := Load(d); err == nil {
			return ix, nil
		}
	}
	return nil, ErrNotFound
}

// Candidates lists where to look for the maps folder: an explicit override, then next to
// the executable, then ..\maps and ..\..\maps (the latter covers dist\radar.exe in this repo).
func Candidates(exeDir, override string) []string {
	return []string{override, filepath.Join(exeDir, "maps"),
		filepath.Join(exeDir, "..", "maps"), filepath.Join(exeDir, "..", "..", "maps")}
}

// Lookup finds a zone by its display name.
func (ix *Index) Lookup(zoneName string) (Zone, bool) {
	z, ok := ix.zones[Normalize(zoneName)]
	return z, ok
}

// List returns all zones sorted by key.
func (ix *Index) List() []Zone {
	out := make([]Zone, 0, len(ix.zones))
	for _, z := range ix.zones {
		out = append(out, z)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// FilePath resolves a file named in the index to a path inside Dir; anything else
// (traversal, files not listed in the index) is rejected.
func (ix *Index) FilePath(file string) (string, bool) {
	if !ix.files[file] {
		return "", false
	}
	return filepath.Join(ix.Dir, file), true
}
