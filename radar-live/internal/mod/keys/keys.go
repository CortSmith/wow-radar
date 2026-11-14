// Package keys is the module that guards the API with shared secrets: view keys for the read API
// and ingest keys for the write API. Keys are hashed in memory and compared in constant time; they
// are never logged. With no key configured for a role, the core leaves that role open and warns.
package keys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"radarlive/internal/core"
)

// Module name, configuration keys and limits.
const (
	ModuleName        = "keys"
	KeyViewKeys       = "view-keys"
	KeyViewFile       = "view-keys-file"
	KeyIngestKeys     = "ingest-keys"
	KeyIngestFile     = "ingest-keys-file"
	KeyKeyringFile    = "keyring-file"
	MinKeyLen         = 16
	generatedBytes    = 32
	idBytes           = 16
	viewHeader        = "X-View-Key"
	ingestHeader      = "X-Ingest-Key"
	bearerPrefix      = "Bearer "
	commentPrefix     = "#"
	placeholderPrefix = "CHANGE_ME"
	keySeparatorSet   = ",\r\n"
)

// GenerateKey returns a new random key: 32 random bytes, base64url without padding.
func GenerateKey() (string, error) {
	buf := make([]byte, generatedBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Module registers key authenticators for the view and ingest roles.
type Module struct {
	host *core.Host
	poll time.Duration
	ring *keyringAuth
}

// Option customises the module.
type Option func(*Module)

// WithKeyringPoll sets how often the keyring file is checked for changes (default 2s).
func WithKeyringPoll(d time.Duration) Option { return func(m *Module) { m.poll = d } }

// New returns the keys module.
func New(opts ...Option) *Module {
	m := &Module{poll: defaultKeyringPoll}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares the settings and loads the keys when the host starts (settings are loaded by then).
func (m *Module) Init(host *core.Host) error {
	m.host = host
	for _, s := range []core.Setting{
		{Key: KeyViewKeys, Kind: core.KindString, Secret: true, Usage: "comma-separated keys allowed to read the API"},
		{Key: KeyViewFile, Kind: core.KindString, Usage: "file with read keys, one per line"},
		{Key: KeyIngestKeys, Kind: core.KindString, Secret: true, Usage: "comma-separated keys allowed to write pings"},
		{Key: KeyIngestFile, Kind: core.KindString, Usage: "file with write keys, one per line"},
		{Key: KeyKeyringFile, Kind: core.KindString, Usage: "keyring.json with managed write keys (reloaded when it changes); empty = off"},
	} {
		if err := host.Config.Declare(s); err != nil {
			return fmt.Errorf("keys: %w", err)
		}
	}
	host.Hooks.OnStart(m.load)
	host.Hooks.OnStop(m.stopKeyring)
	return nil
}

func (m *Module) load(context.Context) error {
	roles := []struct {
		role         core.Role
		list, file   string
		header, name string
	}{
		{core.RoleView, KeyViewKeys, KeyViewFile, viewHeader, "view"},
		{core.RoleIngest, KeyIngestKeys, KeyIngestFile, ingestHeader, "ingest"},
	}
	for _, r := range roles {
		list, err := collect(m.host.Config.String(r.list), m.host.Config.String(r.file))
		if err != nil {
			return fmt.Errorf("keys: %s keys: %w", r.name, err)
		}
		if len(list) > 0 {
			m.host.Auth.Add(r.role, newKeySet(r.header, list))
			m.host.Log.Info("keys loaded", "role", r.name, "count", len(list))
		}
	}
	return m.startKeyring()
}

// collect merges inline and file keys, validating each (errors never contain the key itself).
func collect(inline, file string) ([]string, error) {
	text := inline
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read key file: %w", err)
		}
		text += "," + string(data)
	}
	var out []string
	for _, part := range strings.FieldsFunc(text, func(r rune) bool { return strings.ContainsRune(keySeparatorSet, r) }) {
		key := strings.TrimSpace(part)
		if key == "" || strings.HasPrefix(key, commentPrefix) {
			continue
		}
		if strings.HasPrefix(key, placeholderPrefix) {
			return nil, fmt.Errorf("key #%d is the CHANGE_ME placeholder (an example value, not a key) from the example file: generate one with radar-db keygen", len(out)+1)
		}
		if len(key) < MinKeyLen {
			return nil, fmt.Errorf("key #%d is shorter than %d characters", len(out)+1, MinKeyLen)
		}
		out = append(out, key)
	}
	return out, nil
}

// keySet authenticates requests carrying any of its keys.
type keySet struct {
	header string
	hashes [][sha256.Size]byte
}

func newKeySet(header string, list []string) *keySet {
	s := &keySet{header: header}
	for _, k := range list {
		s.hashes = append(s.hashes, sha256.Sum256([]byte(k)))
	}
	return s
}

// presentedKey returns the key a request carries in header or as a bearer token.
func presentedKey(r *http.Request, header string) string {
	presented := r.Header.Get(header)
	if auth := r.Header.Get("Authorization"); presented == "" && strings.HasPrefix(auth, bearerPrefix) {
		presented = strings.TrimSpace(strings.TrimPrefix(auth, bearerPrefix))
	}
	return presented
}

// Identify implements core.Identifier: like Authenticate, and it reports the matching key's id.
func (s *keySet) Identify(r *http.Request) (core.KeyInfo, bool) {
	presented := presentedKey(r, s.header)
	sum := sha256.Sum256([]byte(presented))
	match := 0
	var matched [sha256.Size]byte
	for _, h := range s.hashes {
		eq := subtle.ConstantTimeCompare(sum[:], h[:])
		match |= eq
		subtle.ConstantTimeCopy(eq, matched[:], h[:])
	}
	if presented == "" || match != 1 {
		return core.KeyInfo{}, false
	}
	return core.KeyInfo{ID: hex.EncodeToString(matched[:idBytes])}, true
}

// Authenticate implements core.Authenticator; it checks every stored hash so timing does not reveal which matched.
func (s *keySet) Authenticate(r *http.Request) bool {
	presented := presentedKey(r, s.header)
	// A missing key takes the same path as a wrong one (hash, compare all), so timing does not
	// separate "no key sent" from "wrong key".
	sum := sha256.Sum256([]byte(presented))
	match := 0
	for _, h := range s.hashes {
		match |= subtle.ConstantTimeCompare(sum[:], h[:])
	}
	return presented != "" && match == 1
}
