package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"radarlive/internal/domain"
)

func testBatch() domain.ArmoryBatch {
	return domain.ArmoryBatch{BatchID: "b1", RunID: "r1", Cursor: "c1", Characters: []domain.ArmoryCharacter{
		{Realm: "Testrealm", Name: "Alpha", Status: "ok", Level: 10, FetchedAt: 1000},
	}}
}

func newSink(t *testing.T, url string) *dbSink {
	t.Helper()
	s, err := newDBSink(url, secretVal{v: fakeDBKey}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSinkApplyBatchSuccess(t *testing.T) {
	var gotKey, gotType, gotPath string
	var got domain.ArmoryBatch
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotType, gotPath = r.Header.Get("X-Ingest-Key"), r.Header.Get("Content-Type"), r.Method+" "+r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"duplicate":false,"stored":1,"skipped":0}`)
	}))
	defer srv.Close()
	res, err := newSink(t, srv.URL).ApplyBatch(context.Background(), testBatch())
	if err != nil || res.Stored != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if gotKey != fakeDBKey || gotType != "application/json" || gotPath != "POST /internal/v1/armory/batch" || got.BatchID != "b1" {
		t.Fatalf("key=%q type=%q path=%q batch=%+v", gotKey, gotType, gotPath, got)
	}
}

func TestSinkRecordAndLastRun(t *testing.T) {
	var posted domain.ArmoryRun
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&posted)
			_, _ = io.WriteString(w, `{}`)
		default:
			_, _ = io.WriteString(w, `{"id":"r9","startedAt":5,"status":"running","cursor":"x"}`)
		}
	}))
	defer srv.Close()
	s := newSink(t, srv.URL)
	if err := s.RecordRun(context.Background(), domain.ArmoryRun{ID: "r1", Status: "running", StartedAt: 1}); err != nil || posted.ID != "r1" {
		t.Fatalf("%v %+v", err, posted)
	}
	run, ok, err := s.LastRun(context.Background())
	if err != nil || !ok || run.ID != "r9" || run.Cursor != "x" {
		t.Fatalf("%+v %v %v", run, ok, err)
	}
}

func TestSinkLastRunNoneIs404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	_, ok, err := newSink(t, srv.URL).LastRun(context.Background())
	if err != nil || ok {
		t.Fatalf("404 means no run yet: ok=%v err=%v", ok, err)
	}
}

func TestSinkStatusClassification(t *testing.T) {
	for code, retryable := range map[int]bool{
		400: false, 401: false, 403: false, 404: false, 413: false, 422: false,
		408: true, 429: true, 500: true, 502: true, 503: true, 507: true,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		_, err := newSink(t, srv.URL).ApplyBatch(context.Background(), testBatch())
		srv.Close()
		var de *dbError
		if !errors.As(err, &de) {
			t.Fatalf("%d: %v", code, err)
		}
		if de.Retryable != retryable {
			t.Errorf("%d: retryable=%v want %v", code, de.Retryable, retryable)
		}
		if strings.Contains(err.Error(), fakeDBKey) {
			t.Errorf("%d: key in error", code)
		}
	}
}

func TestSinkAuthFailureRemembered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	s := newSink(t, srv.URL)
	if s.authFailed() {
		t.Fatal("not yet")
	}
	_, _ = s.ApplyBatch(context.Background(), testBatch())
	if !s.authFailed() {
		t.Fatal("401 must be remembered")
	}
}

func TestSinkRedirectRefused(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/x", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	_, err := newSink(t, srv.URL).ApplyBatch(context.Background(), testBatch())
	var de *dbError
	if !errors.As(err, &de) || de.Retryable {
		t.Fatalf("redirect must be a fatal error: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("redirect was followed (key would leak)")
	}
}

func TestSinkOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"stored":1,"pad":"`+strings.Repeat("x", maxDBResponse+10)+`"}`)
	}))
	defer srv.Close()
	_, err := newSink(t, srv.URL).ApplyBatch(context.Background(), testBatch())
	if err == nil {
		t.Fatal("oversized response must fail")
	}
}

func TestSinkMalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "not json") }))
	defer srv.Close()
	s := newSink(t, srv.URL)
	if _, err := s.ApplyBatch(context.Background(), testBatch()); err == nil {
		t.Fatal("want error")
	}
	if _, _, err := s.LastRun(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestSinkRequestTooLarge(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	b := testBatch()
	b.Characters[0].Gear = json.RawMessage(`{"x":"` + strings.Repeat("y", maxDBRequest) + `"}`)
	_, err := newSink(t, srv.URL).ApplyBatch(context.Background(), b)
	var de *dbError
	if !errors.As(err, &de) || de.Retryable || hits.Load() != 0 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}

func TestSinkTimeoutAndUnreachableRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer srv.Close()
	s := newSink(t, srv.URL)
	s.timeout = 50 * time.Millisecond
	_, err := s.ApplyBatch(context.Background(), testBatch())
	var de *dbError
	if !errors.As(err, &de) || !de.Retryable {
		t.Fatalf("timeout must be retryable: %v", err)
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	_, err = newSink(t, url).ApplyBatch(context.Background(), testBatch())
	if !errors.As(err, &de) || !de.Retryable {
		t.Fatalf("unreachable must be retryable: %v", err)
	}
	if strings.Contains(err.Error(), fakeDBKey) {
		t.Fatal("key in error")
	}
}

func TestNewDBSinkRejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "ftp://127.0.0.1", "http://remote.example.test", "http://", "https://u:p@h.example.test"} {
		if _, err := newDBSink(u, secretVal{v: "k"}, nil); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

// A radar-db (or impostor) that answers nonsense counts or foreign JSON must not be believed.
func TestSinkRejectsImplausibleAnswers(t *testing.T) {
	for name, reply := range map[string]string{
		"negative stored":   `{"duplicate":false,"stored":-5,"skipped":0}`,
		"huge skipped":      `{"duplicate":false,"stored":0,"skipped":99999999999}`,
		"more than sent":    `{"duplicate":false,"stored":2,"skipped":0}`,
		"sum above sent":    `{"duplicate":false,"stored":1,"skipped":1}`,
		"unknown fields":    `{"duplicate":false,"stored":1,"skipped":0,"extra":{"a":1}}`,
		"foreign document":  `{"a":{"a":{"a":1}}}`,
		"trailing document": `{"stored":1} {"stored":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, reply) }))
			defer srv.Close()
			if res, err := newSink(t, srv.URL).ApplyBatch(context.Background(), testBatch()); err == nil {
				t.Fatalf("accepted %q as %+v", reply, res)
			}
		})
	}
}
