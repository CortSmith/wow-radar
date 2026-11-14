package core

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type extAuth struct{ ok bool }

func (e extAuth) Authenticate(*http.Request) bool { return e.ok }
func (e extAuth) Identify(*http.Request) (KeyInfo, bool) {
	return KeyInfo{ID: "abc", FromKeyring: true}, e.ok
}
func (extAuth) Reject(*http.Request) (int, string, string, bool) {
	return http.StatusForbidden, "key_disabled", "key is disabled", true
}

func TestAuthIdentifierAndRejecter(t *testing.T) {
	for _, ok := range []bool{true, false} {
		a := NewAuth(slog.New(slog.NewTextHandler(io.Discard, nil)))
		a.Add(RoleIngest, extAuth{ok: ok})
		var info KeyInfo
		var got bool
		h := a.Require(RoleIngest, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			info, got = KeyInfoFromContext(r.Context())
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if ok && (rec.Code != 200 || !got || info.ID != "abc") {
			t.Fatalf("accept: %d %+v", rec.Code, info)
		}
		if !ok && (rec.Code != 403 || !strings.Contains(rec.Body.String(), "key_disabled")) {
			t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestAuthPlainAuthenticatorStill401(t *testing.T) {
	a := NewAuth(slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.Add(RoleView, AuthenticatorFunc(func(*http.Request) bool { return false }))
	h := a.Require(RoleView, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 401 {
		t.Fatalf("code = %d", rec.Code)
	}
}
