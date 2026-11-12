package client

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web/index.html web/app.js web/logic.js web/style.css
var webFiles embed.FS

// contentSecurityPolicy lets the page load only its own files and talk only to its own server.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// assetTypes are the files served without a token (they hold nothing secret).
var assetTypes = map[string]string{
	"app.js":    "text/javascript; charset=utf-8",
	"logic.js":  "text/javascript; charset=utf-8",
	"style.css": "text/css; charset=utf-8",
}

// pageTemplate is index.html; html/template escapes the token for its attribute.
var pageTemplate = template.Must(template.ParseFS(webFiles, "web/index.html"))

// servePage renders index.html with the per-run token in a <meta> tag the script reads.
func servePage(w http.ResponseWriter, token string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, struct{ Token string }{token}); err != nil {
		http.Error(w, "page error", http.StatusInternalServerError)
	}
}

// serveAsset serves one embedded script or stylesheet.
func serveAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	contentType, ok := assetTypes[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(webFiles, "web/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}
