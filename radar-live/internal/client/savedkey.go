package client

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// The saved ingest key (opt-in, "Remember on this device").
//
// The key is never written in clear text. On Windows it is sealed with DPAPI (CryptProtectData, current
// user scope): only the same Windows user on the same machine can open the file. Elsewhere it is sealed
// with AES-GCM under a key derived from the host and user name, which is obfuscation only: it keeps the
// key out of casual view (a text editor, a backup browse, a screen share) but anyone who can run code as
// that user can recover it. The file is also created 0600 in the per-user config folder. Use
// RADAR_CLIENT_INGEST_KEY if that is not enough.

const (
	savedKeyFileName = "ingest-key.bin"
	savedKeyMagic    = "RCK1"
)

// SavedKeyPath is the saved-key file next to the settings file.
func SavedKeyPath(settingsPath string) string {
	return filepath.Join(filepath.Dir(settingsPath), savedKeyFileName)
}

// sealer protects and opens the key bytes; replaced in tests.
type sealer struct {
	seal func(plain []byte) ([]byte, error)
	open func(sealed []byte) ([]byte, error)
}

func defaultSealer() sealer { return sealer{seal: sealBytes, open: openBytes} }

// readSavedKey returns the key in path ("" when there is no file) or an error for a damaged file or one
// sealed for another user or machine.
func readSavedKey(path string, s sealer) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read saved key: %w", err)
	}
	if !bytes.HasPrefix(data, []byte(savedKeyMagic)) {
		return "", errors.New("saved key file is not in a known format")
	}
	plain, err := s.open(data[len(savedKeyMagic):])
	if err != nil {
		return "", fmt.Errorf("saved key cannot be opened (another Windows user or computer?): %w", err)
	}
	return strings.TrimSpace(string(plain)), nil
}

func writeSavedKey(path, key string, s sealer) error {
	sealed, err := s.seal([]byte(key))
	if err != nil {
		return fmt.Errorf("protect key: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, privateDirMode); err != nil {
		return fmt.Errorf("create key folder: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".key-*.tmp")
	if err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	name := tmp.Name()
	_ = tmp.Chmod(privateFileMode)
	_, werr := tmp.Write(append([]byte(savedKeyMagic), sealed...))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write key: %w", errors.Join(werr, cerr))
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write key: %w", err)
	}
	return nil
}

// obfuscationKey derives the AES key used off Windows from the host and user name.
func obfuscationKey() []byte {
	host, _ := os.Hostname()
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	sum := sha256.Sum256([]byte("radar-client/ingest-key/v1\x00" + host + "\x00" + name))
	return sum[:]
}

func newGCM() (cipher.AEAD, error) {
	block, err := aes.NewCipher(obfuscationKey())
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealAESGCM(plain []byte) ([]byte, error) {
	gcm, err := newGCM()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

func openAESGCM(sealed []byte) ([]byte, error) {
	gcm, err := newGCM()
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("sealed key is too short")
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
}
