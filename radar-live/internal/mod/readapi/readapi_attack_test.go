package readapi_test

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRejectsDuplicateAndOddParams(t *testing.T) {
	r := newRig(t)
	long := strings.Repeat("a", 65)
	bad := []string{
		"/api/v1/sweep?rs=rp&rs=pvp",
		"/api/v1/sweep?q=a&q=b",
		"/api/v1/sweep?evil=1",
		"/api/v1/sweep?limit=5", // not a sweep parameter
		"/api/v1/sweep?q=a%00b",
		"/api/v1/sweep?q=a%0d%0aSet-Cookie:x",
		"/api/v1/sweep?q=a%09b",
		"/api/v1/sweep?q=a%e2%80%aeb", // bidi override
		"/api/v1/sweep?q=a%ffb",       // invalid utf-8
		"/api/v1/sweep?q=" + long,
		"/api/v1/sweep?scope=Zone&mapId=1",
		"/api/v1/sweep?scope=player&guid=Creature-1",
		"/api/v1/sweep?scope=player&guid=Player-1%27%20OR%201=1",
		"/api/v1/sweep?scope=player&guid=Player-1%0a",
		"/api/v1/sweep?mapId=65536&scope=zone",
		"/api/v1/sweep?mapId=-1&scope=zone",
		"/api/v1/sweep?mapId=01&scope=zone",
		"/api/v1/sweep?mapId=1e2&scope=zone",
		"/api/v1/sweep?since=99999999999999999999",
		"/api/v1/sweep?since=" + "4000000000001",
		"/api/v1/sweep?activeWithin=9999999999999",
		"/api/v1/sweep?trail=true",
		"/api/v1/contacts?limit=100001",
		"/api/v1/contacts?offset=1000001",
		"/api/v1/contacts?limit=%2b5",
		"/api/v1/contacts?limit=5&limit=6",
		"/api/v1/trail?guid=Player-1&guid=Player-2",
		"/api/v1/trail?guid=Player-1&q=x",
		"/api/v1/bounty?limit=100001",
		"/api/v1/bounty/player?guid=Player-%00",
		"/api/v1/charts?rs=rp&rs=rp",
		"/api/v1/status?x=1",
	}
	for _, target := range bad {
		t.Run(target[:min(len(target), 60)], func(t *testing.T) {
			rec := r.get("GET", target)
			if rec.Code != 400 {
				t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
			}
			if code := errCode(t, rec); code != "invalid_request" {
				t.Fatalf("code = %q", code)
			}
		})
	}
}

func TestAcceptsBoundaryValues(t *testing.T) {
	r := newRig(t)
	good := []string{
		"/api/v1/sweep?mapId=0&scope=zone",
		"/api/v1/sweep?mapId=65535&scope=zone",
		"/api/v1/sweep?q=" + strings.Repeat("a", 64),
		"/api/v1/sweep?since=0&activeWithin=0&trail=0",
		"/api/v1/sweep?scope=player&guid=Player-1234-0ABC",
		"/api/v1/sweep?q=%25_%5C", // LIKE wildcards are plain text
		"/api/v1/sweep?q=J%C3%B6rg",
		"/api/v1/contacts?limit=100000&offset=1000000",
		"/api/v1/contacts?limit=0",
	}
	for _, target := range good {
		if rec := r.get("GET", target); rec.Code != 200 {
			t.Errorf("%s: status %d %s", target, rec.Code, rec.Body)
		}
	}
}

func TestErrorsDoNotReflectInput(t *testing.T) {
	r := newRig(t)
	payloads := []string{
		"/api/v1/sweep?grp=%3Cscript%3Ealert(1)%3C/script%3E",
		"/api/v1/sweep?rs=%3Cscript%3Ealert(1)%3C/script%3E",
		"/api/v1/sweep?scope=%3Cscript%3Ealert(1)%3C/script%3E",
		"/api/v1/sweep?%3Cscript%3E=1",
		"/api/v1/sweep?scope=player&guid=%3Cscript%3E",
		"/api/v1/sweep?q=%3Cscript%3E%00",
		"/api/v1/trail?guid=%3Cscript%3E",
	}
	for _, target := range payloads {
		rec := r.get("GET", target)
		if rec.Code != 400 {
			t.Fatalf("%s: status %d", target, rec.Code)
		}
		if strings.Contains(strings.ToLower(rec.Body.String()), "script") {
			t.Errorf("%s: response reflects input: %s", target, rec.Body)
		}
	}
}

func TestResponseHygiene(t *testing.T) {
	r := newRig(t)
	for _, target := range []string{"/api/v1/sweep", "/api/v1/sweep?grp=zz", "/api/v1/status", "/api/v1/trail"} {
		rec := r.get("GET", target)
		h := rec.Header()
		if !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
			t.Errorf("%s: Content-Type = %q", target, h.Get("Content-Type"))
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: nosniff missing", target)
		}
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, httptest.NewRequest("OPTIONS", "/api/v1/sweep", nil))
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("preflight: nosniff missing")
	}
}
