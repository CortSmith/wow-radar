package limits

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// routeLimits applies the per-route-class rules inside the server-wide deadlines: stream routes
// lose the read/write deadlines and get an idle deadline per read and write instead; other routes
// get the tightest body cap whose prefix matches.
type routeLimits struct {
	streamPaths []string
	bodyCaps    map[string]int64
	streamIdle  time.Duration
}

func (rl *routeLimits) isStream(r *http.Request) bool {
	return r.Method == http.MethodPost && slices.Contains(rl.streamPaths, r.URL.Path)
}

// capFor returns the body cap of the longest matching prefix, or 0 when none applies.
func (rl *routeLimits) capFor(path string) int64 {
	best, limit := -1, int64(0)
	for prefix, n := range rl.bodyCaps {
		if strings.HasPrefix(path, prefix) && len(prefix) > best {
			best, limit = len(prefix), n
		}
	}
	return limit
}

func (rl *routeLimits) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rl.isStream(r) {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Time{})
			_ = rc.SetWriteDeadline(time.Time{})
			r.Body = &idleBody{ReadCloser: r.Body, rc: rc, idle: rl.streamIdle}
			w = &idleWriter{ResponseWriter: w, rc: rc, idle: rl.streamIdle}
		} else if n := rl.capFor(r.URL.Path); n > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, n)
		}
		next.ServeHTTP(w, r)
	})
}

// idleBody re-arms the connection's read deadline before every read, so a stalled sender is cut
// after idle while a steady one may continue indefinitely.
type idleBody struct {
	io.ReadCloser
	rc   *http.ResponseController
	idle time.Duration
}

func (b *idleBody) Read(p []byte) (int, error) {
	_ = b.rc.SetReadDeadline(time.Now().Add(b.idle))
	return b.ReadCloser.Read(p)
}

// idleWriter does the same for writes, so a receiver that stops reading cannot pin the handler.
type idleWriter struct {
	http.ResponseWriter
	rc   *http.ResponseController
	idle time.Duration
}

func (w *idleWriter) Write(p []byte) (int, error) {
	_ = w.rc.SetWriteDeadline(time.Now().Add(w.idle))
	return w.ResponseWriter.Write(p)
}

// Flush re-arms the write deadline too, then flushes.
func (w *idleWriter) Flush() {
	_ = w.rc.SetWriteDeadline(time.Now().Add(w.idle))
	_ = w.rc.Flush()
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *idleWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
