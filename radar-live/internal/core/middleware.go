package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

const requestIDBytes = 8

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes the standard error body {"error": code, "message": message}.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]string{"error": code, "message": message})
}

// statusWriter remembers the status code and whether the header went out.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

// Flush supports streaming responses.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Middleware wraps a handler with a request id, panic recovery, a body size cap and one access log
// line per request. The log line carries the path only, never the query string, which may hold keys.
func Middleware(log *slog.Logger, maxBodyBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := newRequestID()
			w.Header().Set("X-Request-Id", id)
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			defer func() {
				reportPanic(log, sw, id, recover())
				log.Info("request", "id", id, "method", r.Method, "path", r.URL.Path,
					"status", sw.status, "dur", time.Since(start).Round(time.Millisecond))
			}()
			r.Body = http.MaxBytesReader(sw, r.Body, maxBodyBytes)
			next.ServeHTTP(sw, r)
		})
	}
}

// reportPanic turns a recovered handler panic p (nil when none) into a 500 response and a log entry.
func reportPanic(log *slog.Logger, w *statusWriter, id string, p any) {
	if p == nil {
		return
	}
	log.Error("handler panic", "id", id, "panic", p)
	if !w.wrote {
		WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func newRequestID() string {
	b := make([]byte, requestIDBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
