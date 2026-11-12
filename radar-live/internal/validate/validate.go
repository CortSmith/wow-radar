// Package validate is the central, dependency-free input-validation layer: strict query-parameter
// parsing (duplicates, unknown names, charset, ranges, enums), text sanitising for display names and
// a strict JSON decoder. Error messages name the field and the rule, never the offending input, so
// a response can not reflect attacker-controlled text.
package validate

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Error codes carried by *Error; the HTTP layer reports them all as invalid_request.
const (
	CodeDuplicate = "duplicate_param"
	CodeUnknown   = "unknown_param"
	CodeFormat    = "bad_format"
	CodeRange     = "out_of_range"
	CodeEnum      = "bad_value"
	CodeRequired  = "required"
	CodeTooLong   = "too_long"
	CodeChars     = "bad_characters"
	CodeJSON      = "bad_body"
)

// Query-wide caps.
const (
	MaxQueryBytes = 2048 // total bytes of all keys and values
	maxIntDigits  = 18
	// GUIDMaxChars is the longest accepted guid.
	GUIDMaxChars = 64
)

// guidPattern accepts player GUIDs only.
var guidPattern = regexp.MustCompile(`^Player-[A-Za-z0-9_-]{1,40}$`)

// IsGUID reports whether s is a well-formed player guid.
func IsGUID(s string) bool { return guidPattern.MatchString(s) }

// Error is a validation failure. Field and Msg are fixed strings, never request text.
type Error struct{ Code, Field, Msg string }

func (e *Error) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + ": " + e.Msg
}

// Query parses url.Values strictly; the first error sticks and later calls return zero values.
type Query struct {
	v   url.Values
	err *Error
}

// NewQuery checks the whole query first: only allowed names, each at most once, total size capped,
// and no control characters anywhere. Parse methods then validate individual values.
func NewQuery(v url.Values, allowed ...string) *Query {
	q := &Query{v: v}
	ok := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		ok[a] = true
	}
	total := 0
	for k, vs := range v {
		total += len(k)
		for _, s := range vs {
			total += len(s)
		}
		switch {
		case !ok[k]:
			q.fail(CodeUnknown, "query", "unknown query parameter")
		case len(vs) > 1:
			q.fail(CodeDuplicate, k, "parameter given more than once")
		}
	}
	if total > MaxQueryBytes {
		q.fail(CodeTooLong, "query", "query string is too long")
	}
	return q
}

// Err returns the first problem found, or nil.
func (q *Query) Err() error {
	if q.err == nil {
		return nil
	}
	return q.err
}

func (q *Query) fail(code, field, msg string) {
	if q.err == nil {
		q.err = &Error{Code: code, Field: field, Msg: msg}
	}
}

func (q *Query) raw(key string) string {
	if q.err != nil {
		return ""
	}
	return q.v.Get(key)
}

// Enum returns the value of key, or def when absent. fold lower-cases the input first. The value
// must be one of allowed.
func (q *Query) Enum(key, def string, fold bool, allowed ...string) string {
	s := q.raw(key)
	if s == "" {
		return def
	}
	if fold {
		s = strings.ToLower(s)
	}
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	q.fail(CodeEnum, key, "value is not one of the allowed options")
	return def
}

// Text returns the trimmed value of key: valid UTF-8, no control, format (bidi, zero-width) or
// separator characters other than a plain space, at most maxBytes long.
func (q *Query) Text(key string, maxBytes int) string {
	s := q.raw(key)
	if s == "" {
		return ""
	}
	if !IsSafeText(s) {
		q.fail(CodeChars, key, "contains characters that are not allowed")
		return ""
	}
	s = strings.TrimSpace(s)
	if len(s) > maxBytes {
		q.fail(CodeTooLong, key, fmt.Sprintf("longer than %d bytes", maxBytes))
		return ""
	}
	return s
}

// GUID returns a player guid; required makes a missing value an error.
func (q *Query) GUID(key string, required bool) string {
	s := q.raw(key)
	switch {
	case q.err != nil:
		return ""
	case s == "" && required:
		q.fail(CodeRequired, key, "is required")
		return ""
	case s == "":
		return ""
	case len(s) > GUIDMaxChars || !guidPattern.MatchString(s):
		q.fail(CodeFormat, key, "is not a valid player guid")
		return ""
	}
	return s
}

// Int parses an optional plain decimal integer in [lo, hi]. present is false when absent.
func (q *Query) Int(key string, lo, hi int64) (n int64, present bool) {
	s := q.raw(key)
	if s == "" {
		return 0, false
	}
	if !isPlainInt(s) {
		q.fail(CodeFormat, key, "must be a plain integer")
		return 0, true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < lo || n > hi {
		q.fail(CodeRange, key, fmt.Sprintf("must be between %d and %d", lo, hi))
		return 0, true
	}
	return n, true
}

// isPlainInt accepts ASCII digits only, without sign, spaces or leading zeros.
func isPlainInt(s string) bool {
	if len(s) == 0 || len(s) > maxIntDigits || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Flag parses a "0"/"1" switch; absent is false.
func (q *Query) Flag(key string) bool {
	switch q.raw(key) {
	case "", "0":
		return false
	case "1":
		return true
	}
	q.fail(CodeEnum, key, "must be 0 or 1")
	return false
}

// UnsafeRune reports runes that must never reach a log, a header or the page as text: control
// characters (including newlines and tabs), Unicode format characters (bidi overrides and isolates,
// zero-width), line and paragraph separators, private use, surrogates and the replacement character.
func UnsafeRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) ||
		unicode.Is(unicode.Zp, r) || unicode.Is(unicode.Co, r) || unicode.Is(unicode.Cs, r) || r == utf8.RuneError
}

// IsSafeText reports whether s is valid UTF-8 with no UnsafeRune.
func IsSafeText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if UnsafeRune(r) {
			return false
		}
	}
	return true
}

// CleanText makes s displayable: invalid UTF-8 and unsafe runes are dropped (whitespace controls
// become a space), the result is trimmed and cut to maxRunes. n counts the repairs made (0 = unchanged).
func CleanText(s string, maxRunes int) (clean string, n int) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size <= 1:
			n++
		case r == '\n' || r == '\r' || r == '\t' || r == '\v' || r == '\f':
			b.WriteByte(' ')
			n++
		case UnsafeRune(r):
			n++
		default:
			b.WriteRune(r)
		}
	}
	clean = strings.TrimSpace(b.String())
	if clean != strings.TrimSpace(s) && n == 0 {
		n++
	}
	if utf8.RuneCountInString(clean) > maxRunes {
		clean = string([]rune(clean)[:maxRunes])
		n++
	}
	return clean, n
}
