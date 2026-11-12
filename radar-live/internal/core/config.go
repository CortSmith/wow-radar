package core

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// envFileKey is the built-in setting naming the optional KEY=value file.
const envFileKey = "env-file"

// Kind is the type of a configuration setting.
type Kind int

// Setting kinds.
const (
	KindString Kind = iota
	KindInt
	KindDuration
	KindBool
)

// Setting declares one configuration key. Key is the flag name ("max-body"); the environment
// variable is derived from it (RADAR_MAX_BODY). Default is written as it would be typed.
type Setting struct {
	Key     string
	Kind    Kind
	Default string
	Usage   string
	// Secret marks a value that is read from the environment or env file only: no flag, masked in help and dumps.
	Secret bool
}

// Config resolves typed settings declared by the host and its modules.
// Precedence: command-line flag, then environment, then env file, then default.
type Config struct {
	prefix   string
	settings map[string]Setting
	order    []string
	values   map[string]any
	loaded   bool
}

// NewConfig creates a Config whose environment variables start with prefix and a "_".
func NewConfig(prefix string) *Config {
	c := &Config{prefix: prefix, settings: map[string]Setting{}, values: map[string]any{}}
	// The built-in declaration cannot fail: the key is unique and the default is a string.
	_ = c.Declare(Setting{Key: envFileKey, Kind: KindString, Usage: "path of a KEY=value settings file"})
	return c
}

// EnvName returns the environment variable that overrides key.
func (c *Config) EnvName(key string) string {
	return c.prefix + "_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}

// Declare registers a setting. It fails for empty or duplicate keys, unparsable defaults and after Load.
func (c *Config) Declare(s Setting) error {
	if s.Key == "" {
		return errors.New("config: setting key must not be empty")
	}
	if c.loaded {
		return fmt.Errorf("config: cannot declare %q after Load", s.Key)
	}
	if _, dup := c.settings[s.Key]; dup {
		return fmt.Errorf("config: setting %q declared twice", s.Key)
	}
	value, err := parseValue(s.Kind, s.Default)
	if err != nil {
		return fmt.Errorf("config: default of %q: %w", s.Key, err)
	}
	c.settings[s.Key] = s
	c.order = append(c.order, s.Key)
	c.values[s.Key] = value
	return nil
}

// Load resolves every declared setting from args (without the program name) and the environment.
func (c *Config) Load(args []string, lookupEnv func(string) (string, bool)) error {
	fromFlags, err := c.parseFlags(args)
	if err != nil {
		return err
	}
	fileVars, err := c.readEnvFile(fromFlags, lookupEnv)
	if err != nil {
		return err
	}
	for _, key := range c.order {
		raw, ok := c.pick(key, fromFlags, lookupEnv, fileVars)
		if !ok {
			continue
		}
		value, err := parseValue(c.settings[key].Kind, raw)
		if err != nil {
			if c.settings[key].Secret {
				return fmt.Errorf("config: %q: invalid value", key)
			}
			return fmt.Errorf("config: %q: %w", key, err)
		}
		c.values[key] = value
	}
	c.loaded = true
	return nil
}

// pick returns the highest-precedence raw text for key, if any source provides one.
func (c *Config) pick(key string, flags map[string]string, lookupEnv func(string) (string, bool), file map[string]string) (string, bool) {
	if v, ok := flags[key]; ok {
		return v, true
	}
	if v, ok := lookupEnv(c.EnvName(key)); ok {
		return v, true
	}
	v, ok := file[c.EnvName(key)]
	return v, ok
}

// boolValue lets a flag be given as "--verbose" without an argument.
type boolValue struct{ set func(string) }

func (b boolValue) String() string     { return "" }
func (b boolValue) Set(s string) error { b.set(s); return nil }
func (b boolValue) IsBoolFlag() bool   { return true }

// parseFlags parses args and returns the raw text of the flags that were present.
func (c *Config) parseFlags(args []string) (map[string]string, error) {
	if err := c.rejectSecretFlags(args); err != nil {
		return nil, err
	}
	given := map[string]string{}
	fs := flag.NewFlagSet("radar", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	for _, key := range c.order {
		s := c.settings[key]
		if s.Secret {
			continue
		}
		set := func(v string) { given[s.Key] = v }
		if s.Kind == KindBool {
			fs.Var(boolValue{set: set}, s.Key, s.Usage)
			continue
		}
		fs.Func(s.Key, s.Usage, func(v string) error { set(v); return nil })
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, fmt.Errorf("%w\n%s", ErrHelp, c.Usage())
		}
		return nil, fmt.Errorf("config: %w", err)
	}
	return given, nil
}

// parseValue converts raw text into the typed value of a setting kind.
func parseValue(kind Kind, raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	switch kind {
	case KindInt:
		if raw == "" {
			return 0, nil
		}
		return strconv.Atoi(raw)
	case KindDuration:
		if raw == "" {
			return time.Duration(0), nil
		}
		return time.ParseDuration(raw)
	case KindBool:
		if raw == "" {
			return false, nil
		}
		return strconv.ParseBool(raw)
	}
	return raw, nil
}

// String returns a string setting, or "" for an undeclared key.
func (c *Config) String(key string) string { v, _ := c.values[key].(string); return v }

// Int returns an integer setting, or 0 for an undeclared key.
func (c *Config) Int(key string) int { v, _ := c.values[key].(int); return v }

// Duration returns a duration setting, or 0 for an undeclared key.
func (c *Config) Duration(key string) time.Duration { v, _ := c.values[key].(time.Duration); return v }

// Bool returns a boolean setting, or false for an undeclared key.
func (c *Config) Bool(key string) bool { v, _ := c.values[key].(bool); return v }
