package armoryclient

import (
	"fmt"
	"log/slog"
)

// redactedText is what a Secret prints instead of its value.
const redactedText = "<redacted>"

// Secret holds a client id, a client secret or an access token. Every formatting path (%v, %+v, %#v, %s, JSON, text, slog) prints
// "<redacted>"; only Reveal returns the value, and only the transport calls it.
//
// The core package has no value type for secrets (only Setting.Secret for configuration entries), so
// this module carries its own small one; cmd/radar-armory converts the environment values with NewSecret.
type Secret struct{ v string }

// NewSecret wraps a secret value.
func NewSecret(v string) Secret { return Secret{v: v} }

// Reveal returns the value. Use it only to build the outbound request.
func (s Secret) Reveal() string { return s.v }

// IsSet reports whether a value is present.
func (s Secret) IsSet() bool { return s.v != "" }

// String implements fmt.Stringer.
func (s Secret) String() string { return redactedText }

// GoString implements fmt.GoStringer (%#v).
func (s Secret) GoString() string { return redactedText }

// Format implements fmt.Formatter so no verb can print the value.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redactedText)) }

// MarshalJSON implements json.Marshaler.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redactedText + `"`), nil }

// MarshalText implements encoding.TextMarshaler.
func (s Secret) MarshalText() ([]byte, error) { return []byte(redactedText), nil }

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redactedText) }
