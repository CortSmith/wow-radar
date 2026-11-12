package static

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const (
	// MapsDirKey is the config key naming the folder of zone images served at /maps/. Empty = feature off.
	MapsDirKey = "maps-dir"

	mapsPrefix     = "/maps/"
	mapsIndexFile  = "index.json"
	mapsImageCache = "public, max-age=2592000"
	mapsIndexCache = "public, max-age=60, must-revalidate"
)

// mapExts are the only file types served from the maps folder.
var mapExts = map[string]bool{".webp": true, ".png": true, ".json": true}

// mapFiles resolves the maps folder once, after configuration has been loaded (nil = feature off).
func (m *Module) mapFiles() fs.FS {
	m.mapsOnce.Do(func() {
		dir := m.host.Config.String(MapsDirKey)
		if dir == "" {
			return
		}
		sd, err := newSafeDir(dir)
		if err != nil {
			m.host.Log.Error("maps-dir unusable; serving no maps", "err", err)
			m.maps = emptyFS{}
			return
		}
		m.maps = sd
	})
	return m.maps
}

// serveMap answers a request below /maps/ and reports whether it was handled. With maps-dir unset
// it handles nothing, so the request falls through to the page files (which hold no maps/ folder).
func (m *Module) serveMap(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, mapsPrefix) && r.URL.Path != strings.TrimSuffix(mapsPrefix, "/") {
		return false
	}
	root := m.mapFiles()
	if root == nil {
		return false
	}
	rel := strings.TrimPrefix(r.URL.Path, mapsPrefix)
	if !validMapName(rel) {
		notFound(w, r)
		return true
	}
	data, err := fs.ReadFile(root, rel)
	if err != nil {
		notFound(w, r)
		return true
	}
	cache := mapsImageCache
	if path.Base(rel) == mapsIndexFile {
		cache = mapsIndexCache
	}
	writeAsset(w, r, rel, data, cache)
	return true
}

// validMapName accepts clean relative file names of a served type: no empty, dot or hidden segments,
// no backslash or NUL, and only the extensions in mapExts.
func validMapName(rel string) bool {
	if rel == "" || strings.ContainsAny(rel, backslashOrNUL) || path.Clean(rel) != rel {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return mapExts[path.Ext(rel)]
}
