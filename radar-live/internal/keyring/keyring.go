// Package keyring is the on-disk list of ingest API keys (keyring.json). It holds only SHA-256
// hashes, never raw keys. Errors from this package never contain key material or e-mail addresses.
package keyring

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

const (
	// Version is the only file format version this package reads and writes.
	Version = 1
	// MaxFileBytes is the largest keyring file Load accepts.
	MaxFileBytes = 4 << 20

	generatedBytes = 32
	idBytes        = 16 // same as streamapi's keyIDBytes

	MaxEntries    = 10000
	MaxEmailLen   = 254
	MaxPlayerLen  = 64
	MaxNotesLen   = 1000
	MaxReasonLen  = 200
	MaxTags       = 16
	MaxTagLen     = 32
	MaxLineRate   = 1_000_000
	MaxLineBurst  = 10_000_000
	MaxByteRate   = 1 << 30
	MaxByteBurst  = 1 << 32
	MaxMaxStreams = 1000
	hashHexLen    = sha256.Size * 2
	idHexLen      = idBytes * 2
)

// Keyring is the content of keyring.json.
type Keyring struct {
	Version int     `json:"version"`
	Keys    []Entry `json:"keys"`
}

// Entry is one ingest key. Hash is the SHA-256 (hex) of the raw key; the raw key is never stored here.
type Entry struct {
	ID             string     `json:"id"`
	Hash           string     `json:"hash"`
	Email          string     `json:"email"`
	Player         string     `json:"player"`
	Tags           []string   `json:"tags"`
	Notes          string     `json:"notes"`
	CreatedAt      time.Time  `json:"createdAt"`
	ExpiresAt      *time.Time `json:"expiresAt"`
	Disabled       bool       `json:"disabled"`
	DisabledReason string     `json:"disabledReason"`
	DisabledAt     *time.Time `json:"disabledAt"`
	Limits         Limits     `json:"limits"`
}

// Limits are per-key rate limits; nil means "server default". When set they must be positive.
type Limits struct {
	LineRate   *int64 `json:"lineRate"`
	LineBurst  *int64 `json:"lineBurst"`
	ByteRate   *int64 `json:"byteRate"`
	ByteBurst  *int64 `json:"byteBurst"`
	MaxStreams *int64 `json:"maxStreams"`
}

// GenerateKey returns a new random key: 32 random bytes, base64url without padding
// (the same format as internal/mod/keys.GenerateKey).
func GenerateKey() (string, error) {
	buf := make([]byte, generatedBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashKey is the lowercase hex SHA-256 of the raw key.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// IDFromKey is the hex of the first 16 bytes of the key's SHA-256, the same id streamapi derives.
func IDFromKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:idBytes])
}

// NewEntry builds an entry for key with the id and hash filled in and CreatedAt set to now (UTC).
func NewEntry(key, email string, now time.Time) Entry {
	return Entry{
		ID:        IDFromKey(key),
		Hash:      HashKey(key),
		Email:     email,
		Tags:      []string{},
		CreatedAt: now.UTC().Truncate(time.Second),
	}
}

// Find returns the index of the entry with the given id, or -1.
func (kr *Keyring) Find(id string) int {
	for i := range kr.Keys {
		if kr.Keys[i].ID == id {
			return i
		}
	}
	return -1
}

// Validate checks the whole keyring. Errors name the entry by position only.
func (kr *Keyring) Validate() error {
	if kr.Version != Version {
		return fmt.Errorf("keyring: unsupported version %d", kr.Version)
	}
	if len(kr.Keys) > MaxEntries {
		return fmt.Errorf("keyring: too many keys (max %d)", MaxEntries)
	}
	ids := make(map[string]int, len(kr.Keys))
	hashes := make(map[string]int, len(kr.Keys))
	for i := range kr.Keys {
		e := &kr.Keys[i]
		if err := e.Validate(); err != nil {
			return fmt.Errorf("keyring: entry %d: %w", i, err)
		}
		if j, dup := ids[e.ID]; dup {
			return fmt.Errorf("keyring: entries %d and %d share an id", j, i)
		}
		if j, dup := hashes[e.Hash]; dup {
			return fmt.Errorf("keyring: entries %d and %d share a hash", j, i)
		}
		ids[e.ID], hashes[e.Hash] = i, i
	}
	return nil
}

// Validate checks one entry.
func (e *Entry) Validate() error {
	if !isLowerHex(e.Hash, hashHexLen) {
		return errors.New("hash must be 64 lowercase hex characters")
	}
	if !isLowerHex(e.ID, idHexLen) {
		return errors.New("id must be 32 lowercase hex characters")
	}
	if e.ID != e.Hash[:idHexLen] {
		return errors.New("id does not match the hash")
	}
	if err := validateEmail(e.Email); err != nil {
		return err
	}
	if len(e.Player) > MaxPlayerLen || hasControl(e.Player) {
		return fmt.Errorf("player must be at most %d bytes without control characters", MaxPlayerLen)
	}
	if len(e.Notes) > MaxNotesLen {
		return fmt.Errorf("notes must be at most %d bytes", MaxNotesLen)
	}
	if len(e.DisabledReason) > MaxReasonLen || hasControl(e.DisabledReason) {
		return fmt.Errorf("disabledReason must be at most %d bytes without control characters", MaxReasonLen)
	}
	if len(e.Tags) > MaxTags {
		return fmt.Errorf("at most %d tags", MaxTags)
	}
	seen := make(map[string]bool, len(e.Tags))
	for _, t := range e.Tags {
		if err := validateTag(t); err != nil {
			return err
		}
		if seen[t] {
			return errors.New("duplicate tag")
		}
		seen[t] = true
	}
	l := e.Limits
	for _, c := range []struct {
		name string
		v    *int64
		max  int64
	}{
		{"lineRate", l.LineRate, MaxLineRate},
		{"lineBurst", l.LineBurst, MaxLineBurst},
		{"byteRate", l.ByteRate, MaxByteRate},
		{"byteBurst", l.ByteBurst, MaxByteBurst},
		{"maxStreams", l.MaxStreams, MaxMaxStreams},
	} {
		if c.v == nil {
			continue
		}
		if *c.v <= 0 {
			return fmt.Errorf("limit %s must be positive (leave it unset for the default)", c.name)
		}
		if *c.v > c.max {
			return fmt.Errorf("limit %s must be at most %d", c.name, c.max)
		}
	}
	return nil
}

func validateEmail(s string) error {
	if s == "" || len(s) > MaxEmailLen {
		return errors.New("email is required and must be at most 254 bytes")
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || !strings.Contains(s, "@") {
		return errors.New("email is not a valid address")
	}
	if at := strings.LastIndexByte(s, '@'); !strings.Contains(s[at+1:], ".") {
		return errors.New("email is not a valid address")
	}
	return nil
}

func validateTag(t string) error {
	if t == "" || len(t) > MaxTagLen {
		return fmt.Errorf("a tag must be 1 to %d characters", MaxTagLen)
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == ':', c == '.':
		default:
			return errors.New("a tag may only contain letters, digits and - _ : .")
		}
	}
	return nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
