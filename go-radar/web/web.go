// Package web holds the embedded single-page UI (HTML/JS/CSS). Map images are NOT embedded.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html app.js style.css
var files embed.FS

// FS returns the embedded UI files rooted at the web directory.
func FS() fs.FS { return files }
