package core

import (
	"errors"
	"fmt"
	"strings"
)

// Placeholders shown instead of secret values.
const (
	maskSet   = "<set>"
	maskUnset = "<unset>"
)

// ErrHelp is wrapped by the error Load returns for -h/--help; its text is the masked usage.
var ErrHelp = errors.New("help requested")

// SecretFlagError says that a secret setting was offered as a flag and names the environment variable.
type SecretFlagError struct{ Flag, Env string }

func (e *SecretFlagError) Error() string {
	return fmt.Sprintf("config: --%s is not accepted on the command line (command lines leak through process lists); set the environment variable %s instead", e.Flag, e.Env)
}

// RejectSecretFlags returns a SecretFlagError when args offer any of the named flags (flag name -> env var).
// Programs that parse their own flags use it so a secret can never be passed on the command line.
func RejectSecretFlags(args []string, envByFlag map[string]string) error {
	for _, a := range args {
		if a == "--" {
			return nil
		}
		name := strings.TrimLeft(a, "-")
		if name == a {
			continue
		}
		name, _, _ = strings.Cut(name, "=")
		if env, ok := envByFlag[name]; ok {
			return &SecretFlagError{Flag: name, Env: env}
		}
	}
	return nil
}

// rejectSecretFlags applies RejectSecretFlags to the secret settings of c.
func (c *Config) rejectSecretFlags(args []string) error {
	byFlag := map[string]string{}
	for _, key := range c.order {
		if c.settings[key].Secret {
			byFlag[key] = c.EnvName(key)
		}
	}
	return RejectSecretFlags(args, byFlag)
}

// mask hides the value of a secret setting.
func mask(s Setting, raw string) string {
	if !s.Secret {
		return raw
	}
	if raw == "" {
		return maskUnset
	}
	return maskSet
}

// Usage lists every setting with its flag, environment variable and default; secrets have no flag and
// their default is shown only as <set> or <unset>.
func (c *Config) Usage() string {
	var b strings.Builder
	b.WriteString("settings (flag > environment > env file > default):\n")
	for _, key := range c.order {
		s := c.settings[key]
		if s.Secret {
			fmt.Fprintf(&b, "  env %s (secret, no flag): %s (default: %s)\n", c.EnvName(key), s.Usage, mask(s, s.Default))
			continue
		}
		fmt.Fprintf(&b, "  --%s [%s]: %s (default: %q)\n", key, c.EnvName(key), s.Usage, s.Default)
	}
	return b.String()
}

// Dump returns "NAME=value" lines of the resolved settings with secrets masked; it is safe to log.
func (c *Config) Dump() []string {
	out := make([]string, 0, len(c.order))
	for _, key := range c.order {
		s := c.settings[key]
		out = append(out, c.EnvName(key)+"="+mask(s, fmt.Sprint(c.values[key])))
	}
	return out
}

// EnvNames returns the environment variable of every declared setting (used to prove each one is documented).
func (c *Config) EnvNames() []string {
	out := make([]string, 0, len(c.order))
	for _, key := range c.order {
		out = append(out, c.EnvName(key))
	}
	return out
}
