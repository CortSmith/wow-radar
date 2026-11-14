package keyring

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Load reads and validates keyring.json. A missing file is reported as an error wrapping os.ErrNotExist.
func Load(path string) (*Keyring, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("keyring: read %s: %w", path, err)
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("keyring: %s is larger than %d bytes", path, MaxFileBytes)
	}
	return Parse(data)
}

// Parse decodes and validates keyring JSON. Decode errors are deliberately generic.
func Parse(data []byte) (*Keyring, error) {
	var kr Keyring
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&kr); err != nil {
		return nil, errors.New("keyring: file is not valid keyring JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("keyring: unexpected data after the keyring JSON")
	}
	if err := kr.Validate(); err != nil {
		return nil, err
	}
	for i := range kr.Keys {
		if kr.Keys[i].Tags == nil {
			kr.Keys[i].Tags = []string{}
		}
	}
	return &kr, nil
}

// Save validates kr and writes it atomically with mode 0600. It does not lock; use Update for
// read-modify-write.
func Save(path string, kr *Keyring) error {
	if kr.Version == 0 {
		kr.Version = Version
	}
	if kr.Keys == nil {
		kr.Keys = []Entry{}
	}
	for i := range kr.Keys {
		if kr.Keys[i].Tags == nil {
			kr.Keys[i].Tags = []string{}
		}
	}
	if err := kr.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(kr, "", "  ")
	if err != nil {
		return errors.New("keyring: encode failed")
	}
	if len(data) > MaxFileBytes {
		return fmt.Errorf("keyring: result would exceed %d bytes", MaxFileBytes)
	}
	return WriteFileAtomic(path, append(data, '\n'))
}

// Update locks the keyring, loads it (an absent file is an empty keyring), applies fn, validates
// and saves. If fn returns an error nothing is written.
func Update(path string, fn func(*Keyring) error) error {
	unlock, err := Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	kr, err := Load(path)
	if errors.Is(err, os.ErrNotExist) {
		kr, err = &Keyring{Version: Version, Keys: []Entry{}}, nil
	}
	if err != nil {
		return err
	}
	if err := fn(kr); err != nil {
		return err
	}
	return Save(path, kr)
}

// WriteFileAtomic writes data to path through a temp file in the same directory (mode 0600, or the
// mode and owner of the file being replaced), fsyncs it and renames it over path.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*") // created 0600
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	name := tmp.Name()
	inheritFileAttrs(tmp, path)
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	ok = true
	if d, err := os.Open(dir); err == nil { // best effort: make the rename durable
		d.Sync()
		d.Close()
	}
	return nil
}
