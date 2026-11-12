package core

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func bufferLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestAuthOpenWhenNothingRegisteredWarnsOnce(t *testing.T) {
	log, buf := bufferLogger()
	a := NewAuth(log)
	h := a.Require(RoleView, okHandler())
	for i := 0; i < 3; i++ {
		if rec := serve(h, httptest.NewRequest("GET", "/x", nil)); rec.Code != http.StatusOK {
			t.Fatalf("open API must allow, got %d", rec.Code)
		}
	}
	if n := strings.Count(buf.String(), "no authenticator"); n != 1 {
		t.Fatalf("warning count = %d, want 1; log: %s", n, buf.String())
	}
}

func TestAuthEnforcesRegisteredAuthenticators(t *testing.T) {
	log, _ := bufferLogger()
	a := NewAuth(log)
	a.Add(RoleView, AuthenticatorFunc(func(r *http.Request) bool { return r.Header.Get("X-View-Key") == "k" }))
	view, ingest := a.Require(RoleView, okHandler()), a.Require(RoleIngest, okHandler())

	good := httptest.NewRequest("GET", "/x", nil)
	good.Header.Set("X-View-Key", "k")
	if rec := serve(view, good); rec.Code != http.StatusOK {
		t.Errorf("valid key rejected: %d", rec.Code)
	}
	rec := serve(view, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"error":"unauthorized"`) {
		t.Errorf("missing key: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(ingest, httptest.NewRequest("POST", "/x", nil)); rec.Code != http.StatusOK {
		t.Errorf("ingest role has no authenticators so it stays open, got %d", rec.Code)
	}
}

func TestAuthAnyAuthenticatorMayAccept(t *testing.T) {
	log, _ := bufferLogger()
	a := NewAuth(log)
	a.Add(RoleIngest, AuthenticatorFunc(func(*http.Request) bool { return false }))
	a.Add(RoleIngest, AuthenticatorFunc(func(*http.Request) bool { return true }))
	if rec := serve(a.Require(RoleIngest, okHandler()), httptest.NewRequest("POST", "/x", nil)); rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestBusDeliversTypedPayloads(t *testing.T) {
	log, _ := bufferLogger()
	b := NewBus(log)
	var got []int
	Subscribe(b, "n", func(v int) { got = append(got, v) })
	Subscribe(b, "n", func(v int) { got = append(got, v*10) })
	Publish(b, "n", 2)
	Publish(b, "other", 5)
	if len(got) != 2 || got[0] != 2 || got[1] != 20 {
		t.Fatalf("got %v", got)
	}
}

func TestBusIgnoresWrongTypeAndLogsIt(t *testing.T) {
	log, buf := bufferLogger()
	b := NewBus(log)
	called := false
	Subscribe(b, "n", func(int) { called = true })
	Publish(b, "n", "not an int")
	if called || !strings.Contains(buf.String(), "type mismatch") {
		t.Fatalf("called=%v log=%s", called, buf)
	}
}

func TestBusRecoversSubscriberPanics(t *testing.T) {
	log, buf := bufferLogger()
	b := NewBus(log)
	var after bool
	Subscribe(b, "t", func(int) { panic("kaboom") })
	Subscribe(b, "t", func(int) { after = true })
	Publish(b, "t", 1)
	if !after || !strings.Contains(buf.String(), "kaboom") {
		t.Fatalf("after=%v log=%s", after, buf)
	}
}

func TestBusUnsubscribe(t *testing.T) {
	log, _ := bufferLogger()
	b := NewBus(log)
	count := 0
	cancel := Subscribe(b, "t", func(int) { count++ })
	Publish(b, "t", 1)
	cancel()
	Publish(b, "t", 1)
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
}
