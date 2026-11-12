package core

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteErrorAndJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusBadRequest, "bad_request", "nope")
	if rec.Code != 400 || strings.TrimSpace(rec.Body.String()) != `{"error":"bad_request","message":"nope"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q", ct)
	}
	rec = httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, map[string]int{"a": 1})
	if strings.TrimSpace(rec.Body.String()) != `{"a":1}` {
		t.Fatalf("body %s", rec.Body)
	}
}

func TestMiddlewareSetsRequestID(t *testing.T) {
	log, _ := bufferLogger()
	h := Middleware(log, 1024)(okHandler())
	a := serve(h, httptest.NewRequest("GET", "/x", nil)).Header().Get("X-Request-Id")
	b := serve(h, httptest.NewRequest("GET", "/x", nil)).Header().Get("X-Request-Id")
	if a == "" || a == b {
		t.Fatalf("request ids not unique: %q %q", a, b)
	}
}

func TestMiddlewareRecoversPanics(t *testing.T) {
	log, buf := bufferLogger()
	h := Middleware(log, 1024)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("oops") }))
	rec := serve(h, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), `"error":"internal"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(buf.String(), "oops") {
		t.Fatal("panic not logged")
	}
}

func TestMiddlewareCapsBody(t *testing.T) {
	log, _ := bufferLogger()
	var readErr error
	h := Middleware(log, 10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))
	serve(h, httptest.NewRequest("POST", "/x", strings.NewReader(strings.Repeat("a", 100))))
	if readErr == nil {
		t.Fatal("oversized body must produce a read error")
	}
	readErr = nil
	serve(h, httptest.NewRequest("POST", "/x", strings.NewReader("small")))
	if readErr != nil {
		t.Fatalf("small body failed: %v", readErr)
	}
}

func TestMiddlewareAccessLogOmitsQueryString(t *testing.T) {
	log, buf := bufferLogger()
	h := Middleware(log, 1024)(okHandler())
	serve(h, httptest.NewRequest("GET", "/api/v1/sweep?key=SECRET&rs=pvp", nil))
	line := buf.String()
	if strings.Contains(line, "SECRET") || strings.Contains(line, "key=") {
		t.Fatalf("query string leaked into log: %s", line)
	}
	for _, want := range []string{"/api/v1/sweep", "GET", "status=200"} {
		if !strings.Contains(line, want) {
			t.Errorf("access log missing %q: %s", want, line)
		}
	}
}
