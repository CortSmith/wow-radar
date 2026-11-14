// Package keyvault is a small sealed store of raw ingest keys by key id (AES-256-GCM, a random
// nonce per entry, the id bound as additional data). The sealing key lives in a separate file.
// Errors never contain key material.
package keyvault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"radarlive/internal/keyring"
)

const (
	// Version is the vault file format version.
	Version = 1
	// SealKeyBytes is the exact size of the sealing key file.
	SealKeyBytes = 32
	// MaxFileBytes caps the vault file size.
	MaxFileBytes = 8 << 20
	maxKeyLen    = 1024
	idHexLen     = 32
)

// ErrOpen is returned for a wrong sealing key or tampered data.
var ErrOpen = errors.New("keyvault: cannot open entry (wrong sealing key or tampered data)")

type file struct {
	Version int               `json:"version"`
	Entries map[string]string `json:"entries"` // id -> base64(nonce || ciphertext)
}

// Vault is a handle on a vault file and its sealing key. It is safe for concurrent use, also
// across processes (writes take a sidecar lock and re-read the file).
type Vault struct {
	path string
	aead cipher.AEAD
}

// Open reads the sealing key (creating it, mode 0600, if absent) and returns the vault. The vault
// file itself is created on the first Put.
func Open(vaultPath, sealKeyPath string) (*Vault, error) {
	seal, err := loadOrCreateSealKey(sealKeyPath)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(seal)
	if err != nil {
		return nil, errors.New("keyvault: bad sealing key")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("keyvault: cipher setup failed")
	}
	return &Vault{path: vaultPath, aead: aead}, nil
}

func loadOrCreateSealKey(path string) ([]byte, error) {
	if b, err := readSealKey(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return b, err
	}
	b := make([]byte, SealKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("keyvault: random: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) { // lost a race with another process
		return readSealKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("keyvault: create sealing key %s: %w", path, err)
	}
	_, werr := f.Write(b)
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return nil, fmt.Errorf("keyvault: write sealing key %s: %w", path, werr)
	}
	return b, nil
}

func readSealKey(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, SealKeyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("keyvault: read sealing key %s: %w", path, err)
	}
	if len(b) != SealKeyBytes {
		return nil, fmt.Errorf("keyvault: sealing key %s must be exactly %d bytes", path, SealKeyBytes)
	}
	return b, nil
}

func checkID(id string) error {
	if len(id) != idHexLen {
		return errors.New("keyvault: id must be 32 lowercase hex characters")
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return errors.New("keyvault: id must be 32 lowercase hex characters")
		}
	}
	return nil
}

func (v *Vault) load() (*file, error) {
	f, err := os.Open(v.path)
	if errors.Is(err, os.ErrNotExist) {
		return &file{Version: Version, Entries: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("keyvault: read %s: %w", v.path, err)
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("keyvault: %s is larger than %d bytes", v.path, MaxFileBytes)
	}
	var vf file
	if err := json.Unmarshal(data, &vf); err != nil {
		return nil, errors.New("keyvault: file is not valid vault JSON")
	}
	if vf.Version != Version {
		return nil, fmt.Errorf("keyvault: unsupported version %d", vf.Version)
	}
	if vf.Entries == nil {
		vf.Entries = map[string]string{}
	}
	return &vf, nil
}

func (v *Vault) save(vf *file) error {
	data, err := json.MarshalIndent(vf, "", "  ")
	if err != nil {
		return errors.New("keyvault: encode failed")
	}
	return keyring.WriteFileAtomic(v.path, append(data, '\n'))
}

// Put seals key under id, replacing any earlier entry.
func (v *Vault) Put(id, key string) error {
	if err := checkID(id); err != nil {
		return err
	}
	if key == "" || len(key) > maxKeyLen {
		return fmt.Errorf("keyvault: key must be 1 to %d bytes", maxKeyLen)
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("keyvault: random: %w", err)
	}
	sealed := v.aead.Seal(nonce, nonce, []byte(key), []byte(id))
	unlock, err := keyring.Lock(v.path)
	if err != nil {
		return err
	}
	defer unlock()
	vf, err := v.load()
	if err != nil {
		return err
	}
	vf.Entries[id] = base64.StdEncoding.EncodeToString(sealed)
	return v.save(vf)
}

// Get returns the raw key for id. ok is false when there is no entry. A wrong sealing key or
// modified data is an error (ErrOpen), never a wrong key.
func (v *Vault) Get(id string) (key string, ok bool, err error) {
	if err := checkID(id); err != nil {
		return "", false, err
	}
	vf, err := v.load()
	if err != nil {
		return "", false, err
	}
	enc, found := vf.Entries[id]
	if !found {
		return "", false, nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) < v.aead.NonceSize()+v.aead.Overhead() {
		return "", false, ErrOpen
	}
	n := v.aead.NonceSize()
	plain, err := v.aead.Open(nil, raw[:n], raw[n:], []byte(id))
	if err != nil {
		return "", false, ErrOpen
	}
	return string(plain), true, nil
}

// Has reports whether an entry exists for id (without decrypting it).
func (v *Vault) Has(id string) (bool, error) {
	if err := checkID(id); err != nil {
		return false, err
	}
	vf, err := v.load()
	if err != nil {
		return false, err
	}
	_, ok := vf.Entries[id]
	return ok, nil
}

// Delete removes the entry for id; deleting an absent id is not an error.
func (v *Vault) Delete(id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	unlock, err := keyring.Lock(v.path)
	if err != nil {
		return err
	}
	defer unlock()
	vf, err := v.load()
	if err != nil {
		return err
	}
	if _, ok := vf.Entries[id]; !ok {
		return nil
	}
	delete(vf.Entries, id)
	return v.save(vf)
}
