// Package client is the engine behind radar-client, the desktop app that uploads a World of Warcraft
// combat log to radar-ingest while the game writes it.
//
// The pieces: Settings (non-secret preferences kept in a per-user file), KeyStore (the ingest key, memory
// only), Session (tails the log and streams it through internal/feed), FilePicker (native "Browse..."
// dialog) and UIServer (the local, token-protected web UI and its JSON API).
package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"radarlive/internal/domain"
)

// Names and defaults of the client's configuration.
const (
	// EnvServer supplies the default server address.
	EnvServer = "RADAR_CLIENT_SERVER"
	// EnvKey supplies the ingest key. The key is never stored anywhere else.
	EnvKey = "RADAR_CLIENT_INGEST_KEY"
	// FallbackServer is used when neither the settings file nor EnvServer names a server.
	FallbackServer = "http://127.0.0.1:8788"

	settingsDirName  = "RadarClient"
	settingsFileName = "settings.json"
	privateDirMode   = 0o700
	privateFileMode  = 0o600
)

// Settings are the non-secret preferences of the client. The ingest key is deliberately not part of
// this type: it never reaches the settings file.
type Settings struct {
	Server          string `json:"server"`          // base URL of radar-ingest
	Ruleset         string `json:"ruleset"`         // "" = let ingest detect it, else normal|pvp|rp|rppvp
	LogPath         string `json:"logPath"`         // a log file, or (with FollowNewest) a folder or a file inside it
	FollowNewest    bool   `json:"followNewest"`    // follow the newest WoWCombatLog*.txt in the folder
	IncludeExisting bool   `json:"includeExisting"` // send the content already in the file, not only new lines
}

// DefaultSettings returns the settings used before the user saved any; lookup reads the environment.
func DefaultSettings(lookup func(string) (string, bool)) Settings {
	server := FallbackServer
	if v, ok := lookup(EnvServer); ok && strings.TrimSpace(v) != "" {
		server = strings.TrimSpace(v)
	}
	return Settings{Server: server}
}

// Normalized trims the text fields, lower-cases the ruleset and removes the quotes Explorer's
// "Copy as path" puts around a path.
func (s Settings) Normalized() Settings {
	s.Server = strings.TrimRight(strings.TrimSpace(s.Server), "/")
	s.Ruleset = strings.ToLower(strings.TrimSpace(s.Ruleset))
	s.LogPath = strings.Trim(strings.TrimSpace(s.LogPath), `"`)
	return s
}

// Validate checks the format of the values that are set; an empty log path is allowed.
func (s Settings) Validate() error {
	s = s.Normalized()
	if s.Server == "" {
		return errors.New("server address is required")
	}
	u, err := url.Parse(s.Server)
	switch {
	case err != nil:
		return fmt.Errorf("server address: %w", err)
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("server address %q must start with http:// or https://", s.Server)
	case u.Host == "":
		return fmt.Errorf("server address %q has no host", s.Server)
	case u.User != nil:
		return errors.New("server address must not contain a user name or password")
	}
	if s.Ruleset != "" {
		if _, err := domain.ParseRuleset(s.Ruleset); err != nil {
			return fmt.Errorf("ruleset: %w", err)
		}
	}
	return nil
}

// ValidateForStart is Validate plus the requirement that a log is chosen.
func (s Settings) ValidateForStart() error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Normalized().LogPath == "" {
		return errors.New("choose the combat log file (or its folder) first")
	}
	return nil
}

// DefaultSettingsPath is %APPDATA%\RadarClient\settings.json on Windows and the matching per-user
// config directory elsewhere. It is outside any repository.
func DefaultSettingsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the per-user config folder: %w", err)
	}
	return filepath.Join(dir, settingsDirName, settingsFileName), nil
}

// LoadSettings reads path on top of defaults. A missing file gives the defaults; an unreadable or
// corrupt file gives the defaults and an error the caller may show.
func LoadSettings(path string, defaults Settings) (Settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaults, nil
	}
	if err != nil {
		return defaults, fmt.Errorf("read settings: %w", err)
	}
	loaded := defaults
	if err := json.Unmarshal(data, &loaded); err != nil {
		return defaults, fmt.Errorf("settings file %s is not valid JSON: %w", path, err)
	}
	return loaded.Normalized(), nil
}

// SaveSettings writes s to path, creating the folder. The file is replaced atomically and readable
// by the current user only.
func SaveSettings(path string, s Settings) error {
	data, err := json.MarshalIndent(s.Normalized(), "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, privateDirMode); err != nil {
		return fmt.Errorf("create settings folder: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := writeAndClose(tmp, data); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if err := os.Chmod(tmp.Name(), privateFileMode); err != nil {
		return fmt.Errorf("protect settings: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	return nil
}

func writeAndClose(f *os.File, data []byte) error {
	_, err := f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
