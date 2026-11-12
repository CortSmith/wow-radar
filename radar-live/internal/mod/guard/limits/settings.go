package limits

import (
	"fmt"
	"time"

	"radarlive/internal/core"
)

// Setting keys (flags; the environment names are RADAR_LIMITS_MAX_CONNS and so on).
const (
	SettingReadHeader  = "limits-read-header-timeout"
	SettingRead        = "limits-read-timeout"
	SettingWrite       = "limits-write-timeout"
	SettingIdle        = "limits-idle-timeout"
	SettingStreamIdle  = "limits-stream-idle-timeout"
	SettingMaxHeader   = "limits-max-header-bytes"
	SettingMaxConns    = "limits-max-conns"
	SettingMaxInflight = "limits-max-inflight"
)

// Defaults (docs/security/threat-model.md, resource limits).
const (
	defaultReadHeader  = 5 * time.Second
	defaultRead        = 15 * time.Second
	defaultWrite       = 30 * time.Second
	defaultIdle        = 60 * time.Second
	defaultStreamIdle  = 60 * time.Second
	defaultMaxHeader   = 16 << 10
	defaultMaxConns    = 512
	defaultMaxInflight = 256

	minHeaderBytes = 1 << 10
)

// settings is the validated, typed view of the module's configuration.
type settings struct {
	readHeader, read, write, idle, streamIdle time.Duration
	maxHeader, maxConns, maxInflight          int
}

// declare adds the module's settings to the host configuration.
func declare(cfg *core.Config) error {
	for _, s := range []core.Setting{
		{Key: SettingReadHeader, Kind: core.KindDuration, Default: defaultReadHeader.String(), Usage: "time allowed to read request headers"},
		{Key: SettingRead, Kind: core.KindDuration, Default: defaultRead.String(), Usage: "time allowed to read a whole request"},
		{Key: SettingWrite, Kind: core.KindDuration, Default: defaultWrite.String(), Usage: "time allowed to write a response"},
		{Key: SettingIdle, Kind: core.KindDuration, Default: defaultIdle.String(), Usage: "keep-alive idle time"},
		{Key: SettingStreamIdle, Kind: core.KindDuration, Default: defaultStreamIdle.String(), Usage: "idle deadline of long-lived stream requests"},
		{Key: SettingMaxHeader, Kind: core.KindInt, Default: fmt.Sprint(defaultMaxHeader), Usage: "max request header bytes"},
		{Key: SettingMaxConns, Kind: core.KindInt, Default: fmt.Sprint(defaultMaxConns), Usage: "max concurrent connections per listener"},
		{Key: SettingMaxInflight, Kind: core.KindInt, Default: fmt.Sprint(defaultMaxInflight), Usage: "max in-flight requests per listener (excess get 503)"},
	} {
		if err := cfg.Declare(s); err != nil {
			return err
		}
	}
	return nil
}

// load reads and validates the settings; a nonsensical value stops the start instead of running unprotected.
func load(cfg *core.Config) (settings, error) {
	s := settings{
		readHeader: cfg.Duration(SettingReadHeader), read: cfg.Duration(SettingRead),
		write: cfg.Duration(SettingWrite), idle: cfg.Duration(SettingIdle),
		streamIdle: cfg.Duration(SettingStreamIdle), maxHeader: cfg.Int(SettingMaxHeader),
		maxConns: cfg.Int(SettingMaxConns), maxInflight: cfg.Int(SettingMaxInflight),
	}
	for key, d := range map[string]time.Duration{
		SettingReadHeader: s.readHeader, SettingRead: s.read, SettingWrite: s.write,
		SettingIdle: s.idle, SettingStreamIdle: s.streamIdle,
	} {
		if d <= 0 {
			return s, fmt.Errorf("limits: %s must be positive, got %v", key, d)
		}
	}
	if s.maxHeader < minHeaderBytes {
		return s, fmt.Errorf("limits: %s must be at least %d, got %d", SettingMaxHeader, minHeaderBytes, s.maxHeader)
	}
	if s.maxConns < 1 || s.maxInflight < 1 {
		return s, fmt.Errorf("limits: %s and %s must be at least 1, got %d and %d",
			SettingMaxConns, SettingMaxInflight, s.maxConns, s.maxInflight)
	}
	return s, nil
}
