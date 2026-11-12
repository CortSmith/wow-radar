// Package static is the module that serves the embedded web page (site/) from the host's Routes
// socket. A --site-dir setting serves the files from disk instead, for development. It never
// shadows /api/*: unknown API paths get the standard JSON error.
package static

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"

	"radarlive/internal/core"
	"radarlive/site"
)

const (
	// ModuleName is the module's registered name.
	ModuleName = "static"
	// SiteDirKey is the config key that overrides the embedded page with a directory.
	SiteDirKey = "site-dir"

	indexFile       = "index.html"
	apiPrefix       = "/api/"
	htmlCache       = "no-cache"
	assetCache      = "public, max-age=300, must-revalidate"
	etagBytes       = 8
	codeUnknownPath = "not_found"
)

// contentTypes maps file extensions to types (the OS registry is not trusted for this).
var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".mjs":  "text/javascript; charset=utf-8",
	".json": "application/json; charset=utf-8",
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".webp": "image/webp",
	".ico":  "image/x-icon",
}

// Module serves the web page.
type Module struct {
	host *core.Host
	once sync.Once
	root fs.FS
}

// NewModule returns the static module.
func NewModule() *Module { return &Module{} }

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares the site-dir setting and registers the catch-all GET route.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	err := host.Config.Declare(core.Setting{
		Key: SiteDirKey, Kind: core.KindString,
		Usage: "serve the web page from this directory instead of the embedded copy (development)",
	})
	if err != nil {
		return err
	}
	err = host.Config.Declare(core.Setting{
		Key: SecurityContactKey, Kind: core.KindString,
		Usage: "contact for vulnerability reports, published at /.well-known/security.txt (empty = no file)",
	})
	if err != nil {
		return err
	}
	if err := host.Routes.HandleFunc(http.MethodGet, securityTxtPath, m.serveSecurityTxt); err != nil {
		return err
	}
	return host.Routes.HandleFunc(http.MethodGet, "/", m.serve)
}

// files resolves the file system once, after configuration has been loaded.
func (m *Module) files() fs.FS {
	m.once.Do(func() {
		if dir := m.host.Config.String(SiteDirKey); dir != "" {
			sd, err := newSafeDir(dir)
			if err != nil {
				m.host.Log.Error("site-dir unusable; serving nothing", "err", err)
				m.root = emptyFS{}
				return
			}
			m.root = sd
			return
		}
		m.root = site.FS()
	})
	return m.root
}

// serve answers GET requests that no other route claimed.
func (m *Module) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, apiPrefix) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		core.WriteError(w, http.StatusNotFound, codeUnknownPath, "unknown API path")
		return
	}
	if strings.ContainsAny(r.URL.Path, backslashOrNUL) {
		notFound(w, r)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = indexFile
	}
	data, err := fs.ReadFile(m.files(), name)
	if err != nil || strings.HasSuffix(r.URL.Path, "/") && r.URL.Path != "/" {
		notFound(w, r)
		return
	}
	writeAsset(w, r, name, data)
}

// writeAsset sends one file with its type, cache policy and ETag.
func writeAsset(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:etagBytes]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Type", typeOf(name))
	if path.Ext(name) == ".html" {
		h.Set("Cache-Control", htmlCache)
	} else {
		h.Set("Cache-Control", assetCache)
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}

// typeOf returns the content type for a file name.
func typeOf(name string) string {
	if t, ok := contentTypes[path.Ext(name)]; ok {
		return t
	}
	return "application/octet-stream"
}

// notFound is http.NotFound plus nosniff.
func notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.NotFound(w, r)
}

// emptyFS has no files; it replaces an unusable site directory.
type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// backslashOrNUL are bytes that never belong in a request path.
var backslashOrNUL = "\\" + string(rune(0))
