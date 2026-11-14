package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"radarlive/internal/keyring"
)

type fixture struct {
	t   *testing.T
	a   *app
	srv *httptest.Server
	cfg config
	log *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	cfg := config{keyring: filepath.Join(dir, "keyring.json")}
	buf := &bytes.Buffer{}
	a, err := newApp(cfg, log.New(buf, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	a.token = "tok123"
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)
	cfg.fill()
	return &fixture{t: t, a: a, srv: srv, cfg: cfg, log: buf}
}

func (f *fixture) do(method, path string, body any) (int, http.Header, []byte) {
	f.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, rd)
	req.Header.Set(tokenHeader, "tok123")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, data
}

func (f *fixture) create(extra map[string]any) (string, keyView) {
	f.t.Helper()
	body := map[string]any{"email": "a@example.com", "player": "Arthas", "tags": []string{"t1"}}
	for k, v := range extra {
		body[k] = v
	}
	code, _, data := f.do("POST", "/api/keys", body)
	if code != 201 {
		f.t.Fatalf("create: %d %s", code, data)
	}
	var out struct {
		Key   string  `json:"key"`
		Entry keyView `json:"entry"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		f.t.Fatal(err)
	}
	return out.Key, out.Entry
}

func errCode(data []byte) string {
	var e map[string]string
	_ = json.Unmarshal(data, &e)
	return e["error"]
}

func TestTokenAndHostOrigin(t *testing.T) {
	f := newFixture(t)
	req, _ := http.NewRequest("GET", f.srv.URL+"/api/keys", nil)
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 401 {
		t.Fatalf("no token: %d", res.StatusCode)
	}
	req.Header.Set(tokenHeader, "wrong")
	if res, _ = http.DefaultClient.Do(req); res.StatusCode != 401 {
		t.Fatalf("wrong token: %d", res.StatusCode)
	}
	req.Header.Set(tokenHeader, "tok123")
	if res, _ = http.DefaultClient.Do(req); res.StatusCode != 200 {
		t.Fatalf("good token: %d", res.StatusCode)
	}
	req.Host = "evil.example.com"
	if res, _ = http.DefaultClient.Do(req); res.StatusCode != 403 {
		t.Fatalf("foreign host: %d", res.StatusCode)
	}
	req.Host = ""
	req.Header.Set("Origin", "http://evil.example.com")
	if res, _ = http.DefaultClient.Do(req); res.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", res.StatusCode)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("headers wrong")
	}
}

func TestCreateListAndNeverLeaksKey(t *testing.T) {
	f := newFixture(t)
	raw, entry := f.create(nil)
	if entry.ID != keyring.IDFromKey(raw) || !entry.CanReveal || entry.Status != "active" {
		t.Fatalf("entry: %+v", entry)
	}
	if entry.Masked != raw[:4]+"..."+raw[len(raw)-4:] {
		t.Fatalf("masked %q", entry.Masked)
	}
	code, _, data := f.do("GET", "/api/keys", nil)
	if code != 200 || bytes.Contains(data, []byte(raw)) {
		t.Fatalf("list %d leaks key", code)
	}
	var list struct {
		Keys     []keyView `json:"keys"`
		Defaults Defaults  `json:"defaults"`
	}
	json.Unmarshal(data, &list)
	if len(list.Keys) != 1 || list.Defaults != defaultLimits || list.Keys[0].Usage.LinesToday != 0 || list.Keys[0].Usage.LastSeen != nil {
		t.Fatalf("list: %+v", list)
	}
	disk, _ := os.ReadFile(f.cfg.keyring)
	if bytes.Contains(disk, []byte(raw)) {
		t.Fatal("keyring file has the raw key")
	}
	kr, err := keyring.Load(f.cfg.keyring)
	if err != nil || kr.Keys[0].Hash != keyring.HashKey(raw) {
		t.Fatalf("hash mismatch: %v", err)
	}
}

func TestRevealAndNoStoredCopy(t *testing.T) {
	f := newFixture(t)
	raw, e := f.create(nil)
	code, hdr, data := f.do("GET", "/api/keys/"+e.ID+"/reveal", nil)
	var r map[string]string
	json.Unmarshal(data, &r)
	if code != 200 || r["key"] != raw || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("reveal: %d %s %v", code, data, hdr)
	}
	logged := f.log.String()
	if !strings.Contains(logged, "audit: reveal key id="+e.ID) || strings.Contains(logged, raw) || strings.Contains(logged, "a@example.com") {
		t.Fatalf("audit line: %q", logged)
	}
	raw2, e2 := f.create(map[string]any{"email": "b@example.com", "keepCopy": false})
	if e2.CanReveal || e2.Masked != e2.ID[:8]+"..." {
		t.Fatalf("no-vault entry: %+v", e2)
	}
	if strings.Contains(e2.Masked, raw2[:4]) {
		t.Fatal("masked derived from key")
	}
	code, _, data = f.do("GET", "/api/keys/"+e2.ID+"/reveal", nil)
	if code != 404 || errCode(data) != "no_stored_copy" {
		t.Fatalf("reveal without copy: %d %s", code, data)
	}
	before := f.log.Len()
	f.do("GET", "/api/keys/"+e2.ID+"/reveal", nil)
	if f.log.Len() != before {
		t.Fatal("failed reveal must not write an audit line")
	}
	code, _, data = f.do("GET", "/api/keys/"+strings.Repeat("0", 32)+"/reveal", nil)
	if code != 404 || errCode(data) != "not_found" {
		t.Fatalf("unknown id: %d %s", code, data)
	}
	code, _, _ = f.do("GET", "/api/keys/zz/reveal", nil)
	if code != 404 {
		t.Fatalf("bad id: %d", code)
	}
}

func TestUpdate(t *testing.T) {
	f := newFixture(t)
	_, e := f.create(nil)
	code, _, data := f.do("PUT", "/api/keys/"+e.ID, map[string]any{
		"email": "new@example.com", "player": "P", "tags": []string{"x", "y"}, "notes": "n",
		"expiresAt": "2099-01-02T23:59:59Z", "limits": map[string]any{"lineRate": 100, "maxStreams": 2},
	})
	var k keyView
	json.Unmarshal(data, &k)
	if code != 200 || k.Email != "new@example.com" || len(k.Tags) != 2 || k.ExpiresAt == nil ||
		k.Limits.LineRate == nil || *k.Limits.LineRate != 100 || k.Limits.ByteRate != nil || k.ID != e.ID {
		t.Fatalf("update: %d %s", code, data)
	}
	code, _, data = f.do("PUT", "/api/keys/"+e.ID, map[string]any{"email": "bad", "limits": map[string]any{}})
	if code != 400 || errCode(data) != "invalid_request" {
		t.Fatalf("bad email: %d %s", code, data)
	}
	code, _, _ = f.do("PUT", "/api/keys/"+strings.Repeat("1", 32), map[string]any{"email": "a@b.co"})
	if code != 404 {
		t.Fatalf("missing: %d", code)
	}
}

func TestInvalidRequests(t *testing.T) {
	f := newFixture(t)
	cases := []map[string]any{
		{"email": ""},
		{"email": "nope"},
		{"email": "a@example.com", "limits": map[string]any{"lineRate": 0}},
		{"email": "a@example.com", "limits": map[string]any{"lineRate": -5}},
		{"email": "a@example.com", "limits": map[string]any{"maxStreams": 100000}},
		{"email": "a@example.com", "expiresAt": "tomorrow"},
		{"email": "a@example.com", "tags": []string{"bad tag"}},
	}
	for i, c := range cases {
		code, _, data := f.do("POST", "/api/keys", c)
		if code != 400 || errCode(data) != "invalid_request" {
			t.Errorf("case %d: %d %s", i, code, data)
		}
	}
	code, _, data := f.do("POST", "/api/keys", `{"email":"a@example.com","bogus":1}`)
	if code != 400 || errCode(data) != "invalid_request" {
		t.Errorf("unknown field: %d %s", code, data)
	}
	code, _, _ = f.do("POST", "/api/keys", strings.Repeat(" ", maxBodyBytes+10)+`{}`)
	if code != 400 {
		t.Errorf("oversize: %d", code)
	}
	if kr, _ := f.a.load(); len(kr.Keys) != 0 {
		t.Fatal("invalid requests created keys")
	}
	if ok, _ := f.a.vault.Has(strings.Repeat("0", 32)); ok {
		t.Fatal("unexpected vault entry")
	}
}

func TestDisableEnable(t *testing.T) {
	f := newFixture(t)
	_, e := f.create(nil)
	code, _, data := f.do("POST", "/api/keys/"+e.ID+"/disable", map[string]any{"reason": "abuse"})
	var k keyView
	json.Unmarshal(data, &k)
	if code != 200 || !k.Disabled || k.DisabledReason != "abuse" || k.Status != "blocked" {
		t.Fatalf("disable: %d %s", code, data)
	}
	kr, _ := keyring.Load(f.cfg.keyring)
	if kr.Keys[0].DisabledAt == nil {
		t.Fatal("disabledAt not set")
	}
	code, _, data = f.do("POST", "/api/keys/"+e.ID+"/enable", nil)
	json.Unmarshal(data, &k)
	if code != 200 || k.Disabled || k.DisabledReason != "" || k.Status != "active" {
		t.Fatalf("enable: %d %s", code, data)
	}
	code, _, _ = f.do("POST", "/api/keys/"+e.ID+"/disable", map[string]any{"reason": strings.Repeat("x", 500)})
	if code != 400 {
		t.Fatalf("long reason: %d", code)
	}
	code, _, _ = f.do("POST", "/api/keys/"+strings.Repeat("2", 32)+"/disable", nil)
	if code != 404 {
		t.Fatalf("missing: %d", code)
	}
}

func TestExpiredStatus(t *testing.T) {
	f := newFixture(t)
	_, e := f.create(map[string]any{"expiresAt": "2001-01-01T00:00:00Z"})
	if e.Status != "expired" {
		t.Fatalf("status %q", e.Status)
	}
}

func TestRegenerate(t *testing.T) {
	f := newFixture(t)
	raw, e := f.create(map[string]any{"notes": "keep me", "limits": map[string]any{"byteRate": 1000}})
	code, _, data := f.do("POST", "/api/keys/"+e.ID+"/regenerate", map[string]any{"keepCopy": true})
	var out struct {
		Key   string  `json:"key"`
		Entry keyView `json:"entry"`
	}
	json.Unmarshal(data, &out)
	if code != 200 || out.Key == "" || out.Key == raw || out.Entry.ID == e.ID || out.Entry.ID != keyring.IDFromKey(out.Key) {
		t.Fatalf("regen: %d %s", code, data)
	}
	if out.Entry.Notes != "keep me" || out.Entry.Email != "a@example.com" || out.Entry.Limits.ByteRate == nil {
		t.Fatalf("regen lost fields: %+v", out.Entry)
	}
	kr, _ := keyring.Load(f.cfg.keyring)
	if len(kr.Keys) != 1 || kr.Keys[0].Hash != keyring.HashKey(out.Key) {
		t.Fatal("hash not replaced")
	}
	if ok, _ := f.a.vault.Has(e.ID); ok {
		t.Fatal("old vault copy remains")
	}
	if got, ok, _ := f.a.vault.Get(out.Entry.ID); !ok || got != out.Key {
		t.Fatal("new vault copy missing")
	}
	// regenerate without a copy removes the vault entry
	code, _, data = f.do("POST", "/api/keys/"+out.Entry.ID+"/regenerate", map[string]any{"keepCopy": false})
	var out2 struct {
		Entry keyView `json:"entry"`
	}
	json.Unmarshal(data, &out2)
	if code != 200 || out2.Entry.CanReveal {
		t.Fatalf("regen no copy: %d %s", code, data)
	}
	if code, _, _ = f.do("POST", "/api/keys/"+e.ID+"/regenerate", nil); code != 404 {
		t.Fatalf("old id: %d", code)
	}
}

func TestDelete(t *testing.T) {
	f := newFixture(t)
	_, e := f.create(nil)
	code, _, _ := f.do("DELETE", "/api/keys/"+e.ID, nil)
	if code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if ok, _ := f.a.vault.Has(e.ID); ok {
		t.Fatal("vault entry remains")
	}
	if kr, _ := f.a.load(); len(kr.Keys) != 0 {
		t.Fatal("entry remains")
	}
	if code, _, _ = f.do("DELETE", "/api/keys/"+e.ID, nil); code != 404 {
		t.Fatalf("second delete: %d", code)
	}
}

func TestUsageFile(t *testing.T) {
	f := newFixture(t)
	_, e := f.create(nil)
	seen := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	u := usageFile{Version: 1, Keys: map[string]usageView{e.ID: {LastSeen: &seen, LinesToday: 5, BytesToday: 99}}}
	j, _ := json.Marshal(u)
	os.WriteFile(f.cfg.usage, j, 0o600)
	_, _, data := f.do("GET", "/api/keys", nil)
	var list struct{ Keys []keyView }
	json.Unmarshal(data, &list)
	if got := list.Keys[0].Usage; got.LinesToday != 5 || got.BytesToday != 99 || got.LastSeen == nil || !got.LastSeen.Equal(seen) {
		t.Fatalf("usage %+v", got)
	}
	os.WriteFile(f.cfg.usage, []byte("{garbage"), 0o600)
	_, _, data = f.do("GET", "/api/keys", nil)
	list.Keys = nil
	json.Unmarshal(data, &list)
	if list.Keys[0].Usage.LinesToday != 0 {
		t.Fatal("invalid usage file must give zeros")
	}
}

func TestAssetsAndPage(t *testing.T) {
	f := newFixture(t)
	get := func(path string) (*http.Response, string) {
		res, err := http.Get(f.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	for path, ct := range map[string]string{"/app.js": "text/javascript; charset=utf-8", "/logic.js": "text/javascript; charset=utf-8", "/style.css": "text/css; charset=utf-8"} {
		res, body := get(path)
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != ct || body == "" {
			t.Errorf("%s: %d %q", path, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
	for _, p := range []string{"/test/app.test.mjs", "/web/test/fake-dom.mjs", "/test/logic.test.mjs", "/package.json", "/index.html", "/test/"} {
		if res, _ := get(p); res.StatusCode == 200 {
			t.Errorf("%s must not be served", p)
		}
	}
	if res, _ := get("/"); res.StatusCode != 403 {
		t.Errorf("page without token: %d", res.StatusCode)
	}
	res, body := get("/?t=tok123")
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("page: %d", res.StatusCode)
	}
	if !strings.Contains(body, `content="tok123"`) || strings.Contains(body, "{{") {
		t.Fatalf("token not rendered: %s", body[:300])
	}
}

func TestListenLocalRefusesNonLoopback(t *testing.T) {
	if _, err := listenLocal("0.0.0.0:0"); err == nil {
		t.Fatal("0.0.0.0 must be refused")
	}
	ln, err := listenLocal("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

// ---- CLI ----

func runCLI(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	full := append(append([]string{}, args[:1]...), "--keyring", filepath.Join(dir, "keyring.json"))
	full = append(full, args[1:]...)
	code := run(full, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCLIAddListBlockRevealDelete(t *testing.T) {
	dir := t.TempDir()
	code, out, errs := runCLI(t, dir, "add", "--email", "cli@example.com", "--player", "P", "--tag", "a", "--tag", "b", "--line-rate", "50", "--expires", "2099-01-01")
	if code != 0 {
		t.Fatalf("add: %d %s", code, errs)
	}
	key := strings.TrimSpace(out)
	if strings.Contains(key, "\n") || key == "" || len(key) != 43 {
		t.Fatalf("stdout must be exactly the key: %q", out)
	}
	kr, err := keyring.Load(filepath.Join(dir, "keyring.json"))
	if err != nil || len(kr.Keys) != 1 {
		t.Fatal(err)
	}
	e := kr.Keys[0]
	if e.Hash != keyring.HashKey(key) || e.ID != keyring.IDFromKey(key) || len(e.Tags) != 2 ||
		e.Limits.LineRate == nil || *e.Limits.LineRate != 50 || e.Limits.LineBurst != nil || e.ExpiresAt == nil {
		t.Fatalf("entry %+v", e)
	}
	if strings.Contains(errs, key) {
		t.Fatal("stderr leaks key")
	}

	_, out, _ = runCLI(t, dir, "list")
	if !strings.Contains(out, e.ID[:12]) || !strings.Contains(out, "cli@example.com") || strings.Contains(out, key) {
		t.Fatalf("list: %s", out)
	}

	prefix := e.ID[:6]
	if code, _, errs = runCLI(t, dir, "block", prefix, "--reason", "spam"); code != 0 {
		t.Fatalf("block: %s", errs)
	}
	kr, _ = keyring.Load(filepath.Join(dir, "keyring.json"))
	if !kr.Keys[0].Disabled || kr.Keys[0].DisabledReason != "spam" {
		t.Fatal("not blocked")
	}
	if code, _, errs = runCLI(t, dir, "unblock", prefix); code != 0 {
		t.Fatalf("unblock: %s", errs)
	}
	kr, _ = keyring.Load(filepath.Join(dir, "keyring.json"))
	if kr.Keys[0].Disabled {
		t.Fatal("still blocked")
	}
	if code, out, _ = runCLI(t, dir, "reveal", prefix); code != 0 || strings.TrimSpace(out) != key {
		t.Fatalf("reveal: %d %q", code, out)
	}

	code, out, _ = runCLI(t, dir, "regen", prefix)
	newKey := strings.TrimSpace(out)
	if code != 0 || newKey == key || len(newKey) != 43 {
		t.Fatalf("regen: %d %q", code, out)
	}
	kr, _ = keyring.Load(filepath.Join(dir, "keyring.json"))
	if kr.Keys[0].Hash != keyring.HashKey(newKey) {
		t.Fatal("regen hash")
	}

	if code, _, _ = runCLI(t, dir, "delete", kr.Keys[0].ID[:8]); code != 0 {
		t.Fatal("delete failed")
	}
	kr, _ = keyring.Load(filepath.Join(dir, "keyring.json"))
	if len(kr.Keys) != 0 {
		t.Fatal("not deleted")
	}
}

func TestCLINoVaultAndErrors(t *testing.T) {
	dir := t.TempDir()
	_, out, _ := runCLI(t, dir, "add", "--email", "nv@example.com", "--no-vault")
	id := keyring.IDFromKey(strings.TrimSpace(out))
	if code, _, errs := runCLI(t, dir, "reveal", id[:10]); code == 0 || !strings.Contains(errs, "stored") {
		t.Fatalf("reveal no-vault: %d %s", code, errs)
	}
	if code, _, _ := runCLI(t, dir, "add", "--email", "bad"); code == 0 {
		t.Fatal("bad email accepted")
	}
	if code, _, _ := runCLI(t, dir, "add", "--email", "x@example.com", "--line-rate", "0"); code == 0 {
		t.Fatal("zero limit accepted")
	}
	if code, _, _ := runCLI(t, dir, "add"); code == 0 {
		t.Fatal("missing email accepted")
	}
	if code, _, _ := runCLI(t, dir, "add", "--email", "x@example.com", "--key", "secret"); code == 0 {
		t.Fatal("a --key flag must not exist")
	}
}

func TestResolveIDPrefix(t *testing.T) {
	f := newFixture(t)
	var ids []string
	for i := 0; i < 3; i++ {
		_, e := f.create(nil)
		ids = append(ids, e.ID)
	}
	if got, err := f.a.resolveID(ids[0][:10]); err != nil || got != ids[0] {
		t.Fatalf("unique prefix: %v %q", err, got)
	}
	if _, err := f.a.resolveID(ids[0][:5]); err == nil || !strings.Contains(err.Error(), "6") {
		t.Fatalf("short prefix: %v", err)
	}
	if _, err := f.a.resolveID("ffffff"); err == nil {
		t.Fatal("unknown prefix accepted")
	}
	// force an ambiguous prefix with a synthetic keyring
	var kr keyring.Keyring
	kr.Version = 1
	for _, h := range []string{"abcdef" + strings.Repeat("0", 58), "abcdef" + strings.Repeat("1", 58)} {
		kr.Keys = append(kr.Keys, keyring.Entry{ID: h[:32], Hash: h, Email: "a@example.com", Tags: []string{}, CreatedAt: time.Now()})
	}
	if err := keyring.Save(f.cfg.keyring, &kr); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.resolveID("abcdef"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous: %v", err)
	}
	if got, err := f.a.resolveID("abcdef0"); err != nil || got != kr.Keys[0].ID {
		t.Fatalf("longer prefix: %v %q", err, got)
	}
}

// The installed layout (docs/keys.md): vault and sealing key in the root-only directory, usage in the
// service user's directory; a custom keyring path keeps everything next to it.
func TestDefaultPathsMatchInstalledLayout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Linux layout")
	}
	c := config{keyring: defaultKeyringPath()}
	c.fill()
	if c.keyring != "/var/lib/radar-keys/keyring/keyring.json" || c.vault != "/var/lib/radar-keys/vault/keyvault.bin" ||
		c.vaultKey != "/var/lib/radar-keys/vault/keyvault.key" || c.usage != "/var/lib/radar-keys/usage/keyusage.json" {
		t.Fatalf("defaults: %+v", c)
	}
	d := t.TempDir()
	c = config{keyring: filepath.Join(d, "keyring.json")}
	c.fill()
	if c.vault != filepath.Join(d, "keyvault.bin") || c.usage != filepath.Join(d, "keyusage.json") {
		t.Fatalf("custom keyring: %+v", c)
	}
}
