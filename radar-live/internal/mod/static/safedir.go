package static

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// errEscape is returned for any name that resolves outside the site directory.
var errEscape = errors.New("static: path escapes the site directory")

// safeDir serves files below root only. Every open validates the slash-separated name, resolves
// symbolic links with EvalSymlinks and checks that the real path is still inside the real root, so
// neither "..", backslashes, NUL bytes, absolute paths nor a symlink pointing out can leave it.
type safeDir struct{ root string } // root is absolute with symlinks resolved

// newSafeDir resolves dir once (a symlinked site root is allowed; it is the operator's choice).
func newSafeDir(dir string) (*safeDir, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return &safeDir{root: real}, nil
}

// Open implements fs.FS.
func (d *safeDir) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) || strings.ContainsAny(name, backslashOrNUL+":") {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	full := filepath.Join(d.root, filepath.FromSlash(name))
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if real != d.root && !strings.HasPrefix(real, d.root+string(filepath.Separator)) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: errEscape}
	}
	return os.Open(real)
}
