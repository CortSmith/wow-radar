package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"radarlive/internal/keyring"
)

// keysEnv is a radar-keys / radar-ingest environment pointing at one temporary layout.
type keysEnv struct {
	bin, ring, vault, vaultKey, usage string
}

func (k keysEnv) env() []string {
	return []string{"RADAR_KEYRING_FILE=" + k.ring, "RADAR_KEYVAULT_FILE=" + k.vault,
		"RADAR_KEYVAULT_KEY_FILE=" + k.vaultKey, "RADAR_KEYUSAGE_FILE=" + k.usage}
}

// keys runs a radar-keys CLI command and returns its stdout (the key, or the id) and stderr.
func (k keysEnv) keys(t *testing.T, args ...string) (string, string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(k.bin, exe("radar-keys")), args...)
	cmd.Env = append(os.Environ(), k.env()...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("radar-keys %s: %v\n%s", args[0], err, errb.String())
	}
	return strings.TrimSpace(out.String()), errb.String()
}

type uploadResult struct {
	status int
	body   string
	err    error
}

var e2eClient = &http.Client{Timeout: 30 * time.Second}

func streamURL(addr, source string) string {
	return "http://" + addr + "/v1/stream?source=" + source + "&rs=pvp"
}

// upload sends lines in one stream and returns the status and the reply.
func upload(addr, key, source, lines string) uploadResult {
	req, err := http.NewRequest(http.MethodPost, streamURL(addr, source), strings.NewReader(lines))
	if err != nil {
		return uploadResult{err: err}
	}
	req.Header.Set("X-Ingest-Key", key)
	return doUpload(req)
}

func doUpload(req *http.Request) uploadResult {
	resp, err := e2eClient.Do(req)
	if err != nil {
		return uploadResult{err: err}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return uploadResult{status: resp.StatusCode, body: string(b)}
}

// uploadUntil retries until the upload answers want (the keyring is re-read every 2 s).
func uploadUntil(t *testing.T, addr, key, lines string, want int, what string) uploadResult {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	var last uploadResult
	for n := 0; time.Now().Before(deadline); n++ {
		last = upload(addr, key, "e2e-retry"+strings.Repeat("x", n%3), lines)
		if last.err == nil && last.status == want {
			return last
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("%s: want HTTP %d, last %d %q (err %v)", what, want, last.status, last.body, last.err)
	return last
}

func TestKeyManagerEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds three binaries; skipped with -short")
	}
	root := repoRoot(t)
	bin := t.TempDir()
	buildNamed(t, root, bin, "radar-db", "radar-ingest", "radar-keys")

	keygen := func() string {
		out, err := exec.Command(filepath.Join(bin, exe("radar-db")), "keygen").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	dbKey, viewKey := keygen(), keygen()
	base := t.TempDir()
	k := keysEnv{bin: bin, ring: filepath.Join(base, "ring", "keyring.json"),
		vault: filepath.Join(base, "vault", "keyvault.bin"), vaultKey: filepath.Join(base, "vault", "keyvault.key"),
		usage: filepath.Join(base, "usage", "keyusage.json")}
	if err := os.MkdirAll(filepath.Dir(k.usage), 0o700); err != nil {
		t.Fatal(err)
	}

	session, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(sessionRel)))
	if err != nil {
		t.Fatal(err)
	}
	lines := rewriteStamps(string(session), time.Now())

	dbAddr, dbInternalAddr, ingestAddr := freeAddr(t), freeAddr(t), freeAddr(t)
	db := start(t, filepath.Join(bin, exe("radar-db")), []string{"RADAR_INGEST_KEYS=" + dbKey, "RADAR_VIEW_KEYS=" + viewKey},
		"--listen", dbAddr, "--internal-listen", dbInternalAddr, "--db-path", filepath.Join(base, "radar-live.db"))
	waitFor(t, "radar-db", func() bool { return healthy("http://" + dbAddr) })

	// A first key creates the keyring; radar-ingest has NO key from the environment, only the keyring.
	// The per-address limiters are off here: the test polls and retries far faster than a player would.
	seedKey, _ := k.keys(t, "add", "--email", "seed.owner@example.invalid")
	ingest := start(t, filepath.Join(bin, exe("radar-ingest")), append([]string{"RADAR_DB_KEY=" + dbKey, "RADAR_RATELIMIT_ENABLED=false", "RADAR_ABUSE_ENABLED=false"}, k.env()...),
		"--listen", ingestAddr, "--db-url", "http://"+dbInternalAddr, "--logparse-allow-replay", "true")
	waitFor(t, "radar-ingest", func() bool { return healthy("http://" + ingestAddr) })
	ingestPID := ingest.cmd.Process.Pid

	// A key with custom limits, created while radar-ingest runs.
	const email = "player.one@example.invalid"
	playerKey, _ := k.keys(t, "add", "--email", email, "--player", "Arthas-E2E", "--tag", "e2e", "--tag", "tester",
		"--line-rate", "100", "--line-burst", "200", "--max-streams", "2")
	playerID := keyring.IDFromKey(playerKey)
	kr, err := keyring.Load(k.ring)
	if err != nil {
		t.Fatal(err)
	}
	if i := kr.Find(playerID); i < 0 || kr.Keys[i].Limits.LineRate == nil || *kr.Keys[i].Limits.LineRate != 100 ||
		kr.Keys[i].Limits.MaxStreams == nil || *kr.Keys[i].Limits.MaxStreams != 2 {
		t.Fatalf("custom limits not stored: %+v", kr.Keys)
	}
	if out, _ := k.keys(t, "list"); strings.Contains(out, playerKey) || !strings.Contains(out, playerID[:12]) {
		t.Fatalf("list output: %s", out)
	}

	// A key nobody created is refused; the new key works without any restart.
	stranger, err := keyring.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if r := upload(ingestAddr, stranger, "e2e-stranger", lines); r.err != nil || r.status != http.StatusUnauthorized {
		t.Fatalf("never-created key: %+v", r)
	}
	r := uploadUntil(t, ingestAddr, playerKey, lines, http.StatusOK, "upload with the new key")
	var sum struct {
		Lines int64  `json:"lines"`
		Ended string `json:"ended"`
	}
	if err := json.Unmarshal([]byte(r.body), &sum); err != nil || sum.Lines == 0 || sum.Ended != "eof" {
		t.Fatalf("summary %q: %v", r.body, err)
	}

	// An open stream ends with "blocked" shortly after the key is blocked.
	pr, pw := io.Pipe()
	req, _ := http.NewRequest(http.MethodPost, streamURL(ingestAddr, "e2e-open"), pr)
	req.Header.Set("X-Ingest-Key", playerKey)
	done := make(chan uploadResult, 1)
	go func() { done <- doUpload(req) }()
	go func() { _, _ = pw.Write([]byte(lines)) }()
	waitFor(t, "the open stream to be listed", func() bool {
		rq, _ := http.NewRequest(http.MethodGet, "http://"+ingestAddr+"/v1/sources", nil)
		rq.Header.Set("X-Ingest-Key", seedKey)
		resp, err := e2eClient.Do(rq)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(b), "e2e-open")
	})
	blockedAt := time.Now()
	k.keys(t, "block", playerID[:12], "--reason", "e2e test")
	select {
	case res := <-done:
		_ = pw.Close()
		if res.err != nil || res.status != http.StatusOK || !strings.Contains(res.body, `"ended":"blocked"`) {
			t.Fatalf("open stream after block: %+v", res)
		}
		if d := time.Since(blockedAt); d > 6*time.Second {
			t.Errorf("the open stream took %v to end after the block", d)
		}
	case <-time.After(10 * time.Second):
		_ = pw.CloseWithError(io.ErrClosedPipe)
		t.Fatal("the open stream was not ended by the block")
	}
	r = uploadUntil(t, ingestAddr, playerKey, lines, http.StatusForbidden, "upload with a blocked key")
	if !strings.Contains(r.body, "key_disabled") {
		t.Errorf("blocked reply %q lacks key_disabled", r.body)
	}
	if r := upload(ingestAddr, seedKey, "e2e-seed", lines); r.status != http.StatusOK {
		t.Errorf("another key must be unaffected: %+v", r)
	}

	// Unblock: works again, same process.
	k.keys(t, "unblock", playerID)
	uploadUntil(t, ingestAddr, playerKey, lines, http.StatusOK, "upload after unblock")
	if ingest.cmd.Process.Pid != ingestPID {
		t.Fatal("radar-ingest must not have been restarted")
	}

	// Regenerate: the old key stops, the new one (also revealable from the vault) works.
	newKey, _ := k.keys(t, "regen", playerID)
	if newKey == playerKey || newKey == "" {
		t.Fatalf("regen printed %q", newKey)
	}
	newID := keyring.IDFromKey(newKey)
	if got, _ := k.keys(t, "reveal", newID); got != newKey {
		t.Fatal("reveal does not return the regenerated key")
	}
	uploadUntil(t, ingestAddr, newKey, lines, http.StatusOK, "upload with the regenerated key")
	uploadUntil(t, ingestAddr, playerKey, lines, http.StatusUnauthorized, "upload with the replaced key")

	// The manager's HTTP API: the list never carries a key, reveal does.
	apiKey := assertManagerAPI(t, k, ingestAddr, lines, newKey, seedKey)

	// Delete: the key is unknown again.
	k.keys(t, "delete", newID)
	uploadUntil(t, ingestAddr, newKey, lines, http.StatusUnauthorized, "upload with a deleted key")
	if r := upload(ingestAddr, apiKey, "e2e-api2", lines); r.status != http.StatusOK {
		t.Errorf("the key created through the API must still work: %+v", r)
	}

	// Files on disk: hashes and sealed copies only.
	for _, f := range []string{k.ring, k.vault} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{playerKey, newKey, seedKey, apiKey} {
			if bytes.Contains(b, []byte(raw)) {
				t.Errorf("%s contains a raw key", filepath.Base(f))
			}
		}
	}

	// Usage: radar-ingest flushes every 60 s and once more on a graceful stop. A console interrupt
	// cannot be sent to a child on Windows, so there the test waits for the periodic flush.
	if runtime.GOOS != "windows" {
		_ = ingest.cmd.Process.Signal(os.Interrupt)
		_ = ingest.cmd.Wait()
	}
	var usage struct {
		Keys map[string]struct {
			LastSeen   *time.Time `json:"lastSeen"`
			LinesToday int64      `json:"linesToday"`
		} `json:"keys"`
	}
	deadline := time.Now().Add(100 * time.Second)
	for {
		usage.Keys = nil
		if b, err := os.ReadFile(k.usage); err == nil {
			if strings.Contains(string(b), "@") {
				t.Fatalf("the usage file holds an e-mail address: %s", b)
			}
			_ = json.Unmarshal(b, &usage)
			if u, ok := usage.Keys[newID]; ok && u.LinesToday > 0 && u.LastSeen != nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no usage for %s after the flush interval: %+v", newID, usage)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Neither the keys nor the e-mail addresses reach the service logs.
	secrets := []string{playerKey, newKey, seedKey, apiKey, dbKey, viewKey, email, "player.one", "seed.owner", "api.player", "@example.invalid"}
	for name, p := range map[string]*proc{"radar-ingest": ingest, "radar-db": db} {
		out := p.out.String()
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Errorf("%s output contains %q", name, s)
			}
		}
	}
}

var tokenURL = regexp.MustCompile(`http://127\.0\.0\.1:\d+/\?t=\S+`)

// assertManagerAPI starts `radar-keys serve`, creates a key through the API, and checks that the
// list never contains a raw key while the reveal call does. It returns the key made through the API.
func assertManagerAPI(t *testing.T, k keysEnv, ingestAddr, lines, listedKey, seedKey string) string {
	t.Helper()
	srv := start(t, filepath.Join(k.bin, exe("radar-keys")), k.env(), "serve", "--no-browser")
	var page string
	waitFor(t, "radar-keys to print its address", func() bool {
		page = tokenURL.FindString(srv.out.String())
		return page != ""
	})
	u := strings.SplitN(page, "/?t=", 2)
	origin, token := u[0], u[1]
	call := func(method, path string, body string, withToken bool) (int, string) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, origin+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		if withToken {
			req.Header.Set("X-Radar-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := call("GET", "/api/keys", "", false); code != http.StatusUnauthorized {
		t.Fatalf("API without the token: %d", code)
	}
	code, created := call("POST", "/api/keys", `{"email":"api.player@example.invalid","player":"ApiPlayer","tags":["api"]}`, true)
	var made struct {
		Key   string `json:"key"`
		Entry struct {
			ID string `json:"id"`
		} `json:"entry"`
	}
	if code != http.StatusCreated || json.Unmarshal([]byte(created), &made) != nil || made.Key == "" || made.Entry.ID == "" {
		t.Fatalf("create through the API: %d %s", code, created)
	}
	code, list := call("GET", "/api/keys", "", true)
	if code != http.StatusOK || !strings.Contains(list, made.Entry.ID) {
		t.Fatalf("list through the API: %d %s", code, list)
	}
	for _, raw := range []string{made.Key, listedKey, seedKey} {
		if strings.Contains(list, raw) {
			t.Fatal("the list reply contains a raw key")
		}
	}
	code, rev := call("GET", "/api/keys/"+made.Entry.ID+"/reveal", "", true)
	if code != http.StatusOK || !strings.Contains(rev, made.Key) {
		t.Fatalf("reveal: %d %s", code, rev)
	}
	if code, _ := call("GET", "/api/keys/"+made.Entry.ID+"/reveal", "", false); code != http.StatusUnauthorized {
		t.Fatalf("reveal without the token: %d", code)
	}
	uploadUntil(t, ingestAddr, made.Key, lines, http.StatusOK, "upload with the key made through the API")
	return made.Key
}
