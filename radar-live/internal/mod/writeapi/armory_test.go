package writeapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
	"radarlive/internal/mod/writeapi"
)

const (
	batchPath = "/internal/v1/armory/batch"
	runPath   = "/internal/v1/armory/run"
)

// armoryRig hosts writeapi on a real memstore.
type armoryRig struct {
	host  *core.Host
	store *fakeArmory
}

func newArmoryRig(t *testing.T, args ...string) *armoryRig {
	t.Helper()
	r := &armoryRig{host: core.NewHost(), store: newFakeArmory()}
	if err := r.host.Stores.Add("mem", r.store); err != nil {
		t.Fatal(err)
	}
	if err := r.host.Register(writeapi.NewModule(writeapi.WithClock(func() time.Time { return fakeNow }))); err != nil {
		t.Fatal(err)
	}
	if err := r.host.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *armoryRig) do(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.host.Routes.HandlerFor(core.ClassInternal).ServeHTTP(rec, req)
	return rec
}

func charJSON(realm, name string) string {
	b, _ := json.Marshal(name)
	rb, _ := json.Marshal(realm)
	return fmt.Sprintf(`{"realm":%s,"name":%s,"class":"Mage","race":"Human","faction":"Alliance","level":60,
		"guild":"Test Guild","health":4000,"powerType":"mana","power":3000,"gear":{"head":{"id":1}},
		"fetchedAt":%d,"status":"ok"}`, rb, b, fakeNow.UnixMilli())
}

func batchJSON(id string, chars ...string) string {
	return fmt.Sprintf(`{"batchId":%q,"runId":"run-1","characters":[%s],"cursor":"c1"}`, id, strings.Join(chars, ","))
}

func TestArmoryBatchApplied(t *testing.T) {
	r := newArmoryRig(t)
	rec := r.do("POST", batchPath, batchJSON("ab1", charJSON("Testrealm", "Ann"), charJSON("Testrealm", "Bob")))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res domain.ArmoryApplyResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Stored != 2 || res.Duplicate {
		t.Fatalf("result %+v err %v", res, err)
	}
	got, err := r.store.GetArmoryCharacter(context.Background(), "testrealm", "ann")
	if err != nil || got.Class != "Mage" || got.Level != 60 {
		t.Fatalf("stored %+v err %v", got, err)
	}
}

func TestArmoryDuplicateBatchID(t *testing.T) {
	r := newArmoryRig(t)
	body := batchJSON("dup1", charJSON("Testrealm", "Ann"))
	r.do("POST", batchPath, body)
	rec := r.do("POST", batchPath, body)
	var res domain.ArmoryApplyResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || !res.Duplicate || res.Stored != 0 {
		t.Fatalf("duplicate: %d %+v", rec.Code, res)
	}
}

func TestArmoryBatchBadInput(t *testing.T) {
	r := newArmoryRig(t)
	var many []string
	for i := 0; i < 101; i++ {
		many = append(many, charJSON("Testrealm", "Ann"))
	}
	okChar := func(extra string) string {
		return `{"batchId":"b","characters":[{"realm":"R","name":"Ann","status":"ok","fetchedAt":1900000000000` + extra + `}]}`
	}
	tests := map[string]string{
		"empty":             ``,
		"not json":          `{nope`,
		"array":             `[]`,
		"null":              `null`,
		"unknown field":     `{"batchId":"b","characters":[],"extra":1}`,
		"unknown char key":  `{"batchId":"b","characters":[{"bogus":1}]}`,
		"trailing":          batchJSON("b", charJSON("R", "Ann")) + ` {}`,
		"no batch id":       `{"characters":[]}`,
		"bad batch id":      `{"batchId":"has space","characters":[]}`,
		"too many":          batchJSON("b", many...),
		"sql name":          batchJSON("b", charJSON("Testrealm", "Ann'; DROP TABLE x;--")),
		"script realm":      batchJSON("b", charJSON("<script>alert(1)</script>", "Ann")),
		"path name":         batchJSON("b", charJSON("Testrealm", "../../etc")),
		"empty name":        batchJSON("b", charJSON("Testrealm", "")),
		"bad status":        `{"batchId":"b","characters":[{"realm":"R","name":"Ann","status":"zzz","fetchedAt":1900000000000}]}`,
		"level string":      `{"batchId":"b","characters":[{"realm":"R","name":"Ann","level":"80"}]}`,
		"blob not object":   okChar(`,"gear":[1]`),
		"blob too large":    okChar(`,"gear":{"a":"` + strings.Repeat("x", 20000) + `"}`),
		"ancient fetchedAt": `{"batchId":"b","characters":[{"realm":"R","name":"Ann","status":"ok","fetchedAt":5}]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			rec := r.do("POST", batchPath, body)
			if rec.Code != 400 || errCode(t, rec) != "invalid_request" {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			for _, bad := range []string{"DROP", "script", "etc"} {
				if strings.Contains(rec.Body.String(), bad) {
					t.Fatalf("error reflects input: %s", rec.Body)
				}
			}
		})
	}
	if _, err := r.store.GetArmoryCharacter(context.Background(), "testrealm", "ann"); err == nil {
		t.Fatal("rejected batch stored something")
	}
	// Rejected batches do not burn their id.
	if rec := r.do("POST", batchPath, batchJSON("b", charJSON("Testrealm", "Ann"))); rec.Code != 200 {
		t.Fatalf("id burned: %d %s", rec.Code, rec.Body)
	}
}

// Cosmetic damage is repaired like ping text (same as the ping route), never stored raw.
func TestArmoryNamesAreSanitisedNotStoredRaw(t *testing.T) {
	r := newArmoryRig(t)
	long := strings.Repeat("a", 30)
	body := batchJSON("san1", charJSON("Testrealm", "An‮n"), charJSON("Testrealm", long), charJSON("Testrealm", "Bo\u0000b"))
	if rec := r.do("POST", batchPath, body); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	for _, name := range []string{"Ann", strings.Repeat("a", domain.ArmoryNameMaxRunes), "Bob"} {
		got, err := r.store.GetArmoryCharacter(context.Background(), "testrealm", name)
		if err != nil || got.Name != name {
			t.Fatalf("%q: %+v %v", name, got, err)
		}
	}
}

func TestArmoryBatchBodyCap(t *testing.T) {
	r := newArmoryRig(t, "--max-body", "300")
	rec := r.do("POST", batchPath, batchJSON("big", charJSON("Testrealm", "Ann"))+strings.Repeat(" ", 400))
	if rec.Code != http.StatusRequestEntityTooLarge || errCode(t, rec) != "too_large" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestArmoryAuthAndMethod(t *testing.T) {
	r := newArmoryRig(t)
	r.host.Auth.Add(core.RoleIngest, core.AuthenticatorFunc(func(req *http.Request) bool {
		return req.Header.Get("X-Ingest-Key") == "k"
	}))
	run := `{"id":"run-1","startedAt":1900000000000,"status":"running"}`
	for _, tc := range []struct{ method, path, body string }{
		{"POST", batchPath, batchJSON("a1")}, {"POST", runPath, run}, {"GET", runPath, ""},
	} {
		if rec := r.do(tc.method, tc.path, tc.body); rec.Code != 401 {
			t.Fatalf("%s %s no key: %d", tc.method, tc.path, rec.Code)
		}
		if rec := r.do(tc.method, tc.path, tc.body, "X-Ingest-Key", "wrong"); rec.Code != 401 {
			t.Fatalf("%s %s wrong key: %d", tc.method, tc.path, rec.Code)
		}
	}
	if rec := r.do("POST", batchPath, batchJSON("a1"), "X-Ingest-Key", "k"); rec.Code != 200 {
		t.Fatalf("with key: %d %s", rec.Code, rec.Body)
	}
	if rec := r.do("GET", batchPath, "", "X-Ingest-Key", "k"); rec.Code != 405 {
		t.Fatalf("GET batch: %d", rec.Code)
	}
	if rec := r.do("DELETE", runPath, "", "X-Ingest-Key", "k"); rec.Code != 405 {
		t.Fatalf("DELETE run: %d", rec.Code)
	}
}

func TestArmoryRoutesNotOnPublicClass(t *testing.T) {
	r := newArmoryRig(t)
	for _, p := range []string{batchPath, runPath} {
		req := httptest.NewRequest("POST", p, strings.NewReader(batchJSON("x")))
		rec := httptest.NewRecorder()
		r.host.Routes.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s on public class: %d", p, rec.Code)
		}
	}
}

func TestArmoryUnavailableWithoutInterface(t *testing.T) {
	r := newRig(t) // fakeStore has no armory methods
	for _, tc := range []struct{ method, path, body string }{
		{"POST", batchPath, batchJSON("a1")},
		{"POST", runPath, `{"id":"run-1","startedAt":1900000000000,"status":"running"}`},
		{"GET", runPath, ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		r.host.Routes.HandlerFor(core.ClassInternal).ServeHTTP(rec, req)
		if rec.Code != 404 || errCode(t, rec) != "not_found" {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
}

func TestArmoryRunRoundTrip(t *testing.T) {
	r := newArmoryRig(t)
	if rec := r.do("GET", runPath, ""); rec.Code != 404 {
		t.Fatalf("empty GET: %d", rec.Code)
	}
	body := `{"id":"run-1","startedAt":1900000000000,"enumerated":10,"fetched":5,"stored":4,"skipped":1,"failed":0,"status":"running","cursor":"realm/page-3"}`
	if rec := r.do("POST", runPath, body); rec.Code != 200 {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	rec := r.do("GET", runPath, "")
	var got domain.ArmoryRun
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 || got.ID != "run-1" || got.Cursor != "realm/page-3" || got.Fetched != 5 {
		t.Fatalf("GET: %d %+v %v", rec.Code, got, err)
	}
	upd := strings.Replace(strings.Replace(body, `"running"`, `"ok"`, 1), `"fetched":5`, `"fetched":9`, 1)
	r.do("POST", runPath, upd)
	rec = r.do("GET", runPath, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Status != "ok" || got.Fetched != 9 {
		t.Fatalf("replace by id failed: %+v", got)
	}
}

func TestArmoryRunBadInput(t *testing.T) {
	r := newArmoryRig(t)
	tests := map[string]string{
		"empty":         ``,
		"unknown field": `{"id":"r","startedAt":1900000000000,"status":"ok","x":1}`,
		"bad status":    `{"id":"r","startedAt":1900000000000,"status":"zzz"}`,
		"bad id":        `{"id":"a b","startedAt":1900000000000,"status":"ok"}`,
		"negative":      `{"id":"r","startedAt":1900000000000,"status":"ok","failed":-1}`,
		"old start":     `{"id":"r","startedAt":3,"status":"ok"}`,
		"long cursor":   `{"id":"r","startedAt":1900000000000,"status":"ok","cursor":"` + strings.Repeat("a", 300) + `"}`,
		"bidi cursor":   `{"id":"r","startedAt":1900000000000,"status":"ok","cursor":"a‮b"}`,
		"trailing":      `{"id":"r","startedAt":1900000000000,"status":"ok"} {}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if rec := r.do("POST", runPath, body); rec.Code != 400 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
		})
	}
	if rec := r.do("GET", runPath, ""); rec.Code != 404 {
		t.Fatalf("rejected run was stored: %d", rec.Code)
	}
}

func TestArmoryRunBodyCap(t *testing.T) {
	r := newArmoryRig(t, "--max-body", "100")
	rec := r.do("POST", runPath, `{"id":"r","startedAt":1900000000000,"status":"ok"}`+strings.Repeat(" ", 200))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestArmoryCancelledContextIs503(t *testing.T) {
	r := newArmoryRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", batchPath, strings.NewReader(batchJSON("c1", charJSON("Testrealm", "Ann")))).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.host.Routes.HandlerFor(core.ClassInternal).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}
