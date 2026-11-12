// Package site embeds the static web page so the radar-db binary can serve it.
// The page also works from any static file host; nothing here is required to view it.
package site

import (
	"embed"
	"io/fs"
)

//go:embed index.html style.css js
var files embed.FS

// FS returns the embedded page rooted at the site directory.
func FS() fs.FS { return files }
