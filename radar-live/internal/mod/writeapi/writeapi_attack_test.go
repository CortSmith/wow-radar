package writeapi_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"radarlive/internal/domain"
	"radarlive/internal/validate"
)

func pingWith(contact string) string {
	return fmt.Sprintf(`{"rs":"pvp","batchId":"atk","source":"s","contacts":[%s],"charts":[],"deaths":[]}`, contact)
}

func TestAttackBodiesRejected(t *testing.T) {
	r := newRig(t)
	ms := fakeNow.UnixMilli()
	deep := strings.Repeat(`{"a":`, 20) + `1` + strings.Repeat(`}`, 20)
	tests := map[string]string{
		"deep nesting":       `{"rs":"pvp","batchId":"b","contacts":[],"x":` + deep + `}`,
		"huge array":         `{"rs":"pvp","batchId":"b","contacts":[` + strings.Repeat(`1,`, 6000) + `1]}`,
		"two documents":      validPing("b") + validPing("c"),
		"nul trailer":        validPing("b") + "\x00",
		"future timestamp":   pingWith(fmt.Sprintf(`{"guid":"Player-1","lastSeen":%d}`, ms+25*3600*1000)),
		"ancient timestamp":  pingWith(`{"guid":"Player-1","lastSeen":1000}`),
		"coordinate bomb":    pingWith(`{"guid":"Player-1","x":1e30}`),
		"level bomb":         pingWith(`{"guid":"Player-1","level":99999}`),
		"delta bomb":         pingWith(`{"guid":"Player-1","d":{"events":9223372036854775807}}`),
		"huge string":        pingWith(`{"guid":"Player-1","name":"` + strings.Repeat("a", 5000) + `"}`),
		"guid injection":     pingWith(`{"guid":"Player-1'; DROP TABLE contacts;--"}`),
		"script in guid":     pingWith(`{"guid":"Player-<script>"}`),
		"unknown reaction":   pingWith(`{"guid":"Player-1","reaction":"<img>"}`),
		"map id bomb":        pingWith(`{"guid":"Player-1","mapId":99999999}`),
		"fix with bad kind":  pingWith(`{"guid":"Player-1","fixes":[{"t":1,"kind":"x"}]}`),
		"fix far future":     pingWith(fmt.Sprintf(`{"guid":"Player-1","fixes":[{"t":%d,"mapId":1}]}`, ms+48*3600*1000)),
		"number as string":   pingWith(`{"guid":"Player-1","level":"80"}`),
		"null body":          `null`,
		"scalar body":        `1`,
		"duplicate key kept": `{"rs":"pvp","rs":"zzz","batchId":"b"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			rec := r.post(body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "DROP") || strings.Contains(rec.Body.String(), "script") {
				t.Fatalf("error reflects input: %s", rec.Body)
			}
		})
	}
	if len(r.store.got) != 0 {
		t.Fatal("rejected ping reached the store")
	}
}

func TestNamesAreSanitisedNotRejected(t *testing.T) {
	r := newRig(t)
	body := pingWith(fmt.Sprintf(`{"guid":"Player-1","name":"Al‮pha\nx","raw":"Raw\u0000","lastSeen":%d}`, fakeNow.UnixMilli()))
	rec := r.post(body)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := r.store.got[0].Contacts[0]; got.Name != "Alpha x" || got.Raw != "Raw" {
		t.Fatalf("stored %q %q", got.Name, got.Raw)
	}
}

func TestResponseHygiene(t *testing.T) {
	r := newRig(t)
	for _, body := range []string{validPing("h1"), `{bad`} {
		rec := r.post(body)
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("headers = %v", rec.Header())
		}
	}
}

// FuzzPingJSON runs arbitrary bodies through the same pipeline as the handler (strict decode,
// sanitise, validate): no panic, and an accepted ping holds only safe text inside the bounds.
func FuzzPingJSON(f *testing.F) {
	f.Add(validPing("seed"))
	f.Add(`{"rs":"pvp","batchId":"b","contacts":[{"guid":"Player-1","name":"A‮B\n","x":1e30,"level":-1,"d":{"events":-5}}]}`)
	f.Add(`{"rs":"pvp","batchId":"b","contacts":[{"guid":"Player-1","fixes":[{"t":1,"x":"NaN"}]}],"deaths":[{"id":"d","victim":"Player-2"}]}`)
	f.Add(`{"rs":"pvp","batchId":"b"}{}`)
	f.Add(`{"charts":[{"mapId":1,"x0":1e400}]}`)
	f.Fuzz(func(t *testing.T, body string) {
		var p domain.Ping
		if err := validate.DecodeJSON(strings.NewReader(body), &p, validate.JSONLimits{MaxArray: domain.PingMaxFixesPerContact}); err != nil {
			return
		}
		p.Sanitize()
		if err := p.ValidateAt(fakeNow); err != nil {
			return
		}
		for _, c := range p.Contacts {
			if !validate.IsSafeText(c.Name) || !validate.IsGUID(c.GUID) || c.Level < 0 || c.Level > domain.PingLevelMax ||
				c.X > domain.CoordMax || c.X < -domain.CoordMax {
				t.Fatalf("accepted unsafe contact %+v", c)
			}
		}
	})
}
