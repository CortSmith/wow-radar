package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"radarlive/internal/domain"
)

func seedServer(t *testing.T, reply string, check func(r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSeedsSuccessAndRequestShape(t *testing.T) {
	var gotKey, gotPath, gotCursor, gotLimit, gotMethod string
	srv := seedServer(t, `{"items":[{"kind":"character","realm":"","name":"Alpha"},{"kind":"character","realm":"Testrealm","name":"Beta"},{"kind":"guild","realm":"testrealm","guild":"Fake Guild"}],"next":"g|testrealm|Fake Guild"}`,
		func(r *http.Request) {
			gotKey, gotMethod, gotPath = r.Header.Get("X-Ingest-Key"), r.Method, r.URL.Path
			gotCursor, gotLimit = r.URL.Query().Get("cursor"), r.URL.Query().Get("limit")
		})
	page, err := newSink(t, srv.URL).Seeds(context.Background(), "c|alpha|testrealm&x=1#y", 50)
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != fakeDBKey || gotMethod != "GET" || gotPath != "/internal/v1/armory/seeds" || gotCursor != "c|alpha|testrealm&x=1#y" || gotLimit != "50" {
		t.Fatalf("key=%q %s %s cursor=%q limit=%q", gotKey, gotMethod, gotPath, gotCursor, gotLimit)
	}
	if len(page.Items) != 3 || page.Next != "g|testrealm|Fake Guild" || page.Items[0].Realm != "" || page.Items[2].Guild != "Fake Guild" || page.Items[2].Kind != domain.ArmorySeedGuild {
		t.Fatalf("%+v", page)
	}
	// the first page sends no cursor at all
	srv2 := seedServer(t, `{"items":[],"next":""}`, func(r *http.Request) {
		if _, ok := r.URL.Query()["cursor"]; ok {
			t.Error("cursor sent on the first page")
		}
	})
	if page, err := newSink(t, srv2.URL).Seeds(context.Background(), "", 10); err != nil || len(page.Items) != 0 || page.Next != "" {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestSeedsDropsInvalidItemsKeepsTheRest(t *testing.T) {
	srv := seedServer(t, `{"items":[
{"kind":"character","realm":"Testrealm","name":"Good"},
{"kind":"character","realm":"Testrealm","name":"../etc/passwd"},
{"kind":"character","realm":"Test\u0000realm","name":"Nul"},
{"kind":"character","realm":"Testrealm","name":"Evil‮Name"},
{"kind":"dragon","realm":"Testrealm","name":"X"},
{"kind":"guild","realm":"","guild":"No Realm"},
{"kind":"guild","realm":"Testrealm","guild":"`+strings.Repeat("g", 100)+`"},
{"kind":"guild","realm":"Testrealm","guild":"Fine Guild"}],"next":""}`, nil)
	page, err := newSink(t, srv.URL).Seeds(context.Background(), "", 50)
	if err != nil || len(page.Items) != 2 || page.Items[0].Name != "Good" || page.Items[1].Guild != "Fine Guild" {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestSeedsRejectsBadAnswers(t *testing.T) {
	long := strings.Repeat("c", 300)
	items := func(n int) string {
		return strings.TrimSuffix(strings.Repeat(`{"kind":"character","realm":"R","name":"N"},`, n), ",")
	}
	for name, reply := range map[string]string{
		"not json":          `<html>`,
		"unknown field":     `{"items":[],"next":"","extra":1}`,
		"unknown item key":  `{"items":[{"kind":"character","realm":"R","name":"N","x":1}],"next":""}`,
		"trailing":          `{"items":[],"next":""} {"items":[]}`,
		"items wrong type":  `{"items":"x","next":""}`,
		"next wrong type":   `{"items":[],"next":5}`,
		"next too long":     `{"items":[],"next":"` + long + `"}`,
		"next control":      `{"items":[],"next":"a\u0000b"}`,
		"next newline":      `{"items":[],"next":"a\nb"}`,
		"next bad utf8":     "{\"items\":[],\"next\":\"a\xffb\"}",
		"more than limit":   `{"items":[` + items(11) + `],"next":""}`,
		"next equals input": `{"items":[],"next":"same"}`,
		"deep":              strings.Repeat(`{"items":`, 100) + `1` + strings.Repeat("}", 100),
	} {
		t.Run(name, func(t *testing.T) {
			srv := seedServer(t, reply, nil)
			page, err := newSink(t, srv.URL).Seeds(context.Background(), "same", 10)
			if err == nil {
				t.Fatalf("accepted as %+v", page)
			}
			var de *dbError
			if !errors.As(err, &de) {
				t.Fatalf("not a dbError: %v", err)
			}
			if strings.Contains(err.Error(), fakeDBKey) || strings.Contains(err.Error(), "same") {
				t.Fatalf("error leaks: %v", err)
			}
		})
	}
}

func TestSeedsLimitIsValidatedBeforeSending(t *testing.T) {
	var hits int
	srv := seedServer(t, `{"items":[],"next":""}`, func(*http.Request) { hits++ })
	for _, limit := range []int{0, -1, domain.ArmorySeedLimitMax + 1, 1 << 30} {
		if _, err := newSink(t, srv.URL).Seeds(context.Background(), "", limit); err == nil {
			t.Errorf("limit %d accepted", limit)
		}
	}
	if _, err := newSink(t, srv.URL).Seeds(context.Background(), strings.Repeat("c", 300), 10); err == nil {
		t.Error("an oversized cursor was sent")
	}
	if _, err := newSink(t, srv.URL).Seeds(context.Background(), "a\nb", 10); err == nil {
		t.Error("a cursor with a control character was sent")
	}
	if hits != 0 {
		t.Fatalf("%d requests were made for invalid input", hits)
	}
}

func TestSeedsSizeCaps(t *testing.T) {
	// A full page of 500 maximal seeds is about 120 KB: larger than other answers, still accepted.
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i := 0; i < domain.ArmorySeedLimitMax; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"kind":"guild","realm":"%s","guild":"%s"}`, strings.Repeat("r", 48), strings.Repeat("g", 64))
	}
	b.WriteString(`],"next":"x"}`)
	if b.Len() <= maxDBResponse {
		t.Fatalf("test page is only %d bytes", b.Len())
	}
	srv := seedServer(t, b.String(), nil)
	if page, err := newSink(t, srv.URL).Seeds(context.Background(), "", domain.ArmorySeedLimitMax); err != nil || len(page.Items) != domain.ArmorySeedLimitMax {
		t.Fatalf("full page refused: %d items, %v", len(page.Items), err)
	}
	// beyond the seed cap the answer is refused (a lying radar-db)
	huge := seedServer(t, `{"items":[],"next":""}`+strings.Repeat(" ", maxSeedResponse+1), nil)
	if _, err := newSink(t, huge.URL).Seeds(context.Background(), "", 10); err == nil {
		t.Fatal("an oversized answer was accepted")
	}
}

func TestSeedsStatusRedirectAndAuth(t *testing.T) {
	status := func(code int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
	}
	for code, retry := range map[int]bool{404: false, 400: false, 429: true, 500: true, 503: true} {
		srv := status(code)
		_, err := newSink(t, srv.URL).Seeds(context.Background(), "", 10)
		var de *dbError
		if !errors.As(err, &de) || de.Retryable != retry {
			t.Errorf("%d: %v", code, err)
		}
		srv.Close()
	}
	srv := status(401)
	defer srv.Close()
	s := newSink(t, srv.URL)
	if _, err := s.Seeds(context.Background(), "", 10); err == nil || !s.authFailed() {
		t.Fatalf("401 must fail and be remembered: %v", err)
	}
	var trap int
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { trap++ }))
	defer other.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	if _, err := newSink(t, redir.URL).Seeds(context.Background(), "", 10); err == nil || trap != 0 {
		t.Fatalf("redirect: %v trap=%d", err, trap)
	}
}
