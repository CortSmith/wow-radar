package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The domain depends on nothing of ours, so the text rules live here; internal/validate keeps an
// equivalent copy for query parameters (both are tested against the same payloads).

// unsafeRune reports control, format (bidi, zero-width), separator, private-use and surrogate runes.
func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) ||
		unicode.Is(unicode.Zp, r) || unicode.Is(unicode.Co, r) || unicode.Is(unicode.Cs, r) || r == utf8.RuneError
}

func isSafeText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unsafeRune(r) {
			return false
		}
	}
	return true
}

// cleanText drops invalid UTF-8 and unsafe runes (whitespace controls become a space), trims, and cuts
// to maxRunes. n counts repairs.
func cleanText(s string, maxRunes int) (clean string, n int) {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size <= 1:
			n++
		case r == '\n' || r == '\r' || r == '\t' || r == '\v' || r == '\f':
			b.WriteByte(' ')
			n++
		case unsafeRune(r):
			n++
		default:
			b.WriteRune(r)
		}
	}
	clean = strings.TrimSpace(b.String())
	if utf8.RuneCountInString(clean) > maxRunes {
		clean = string([]rune(clean)[:maxRunes])
		n++
	}
	return clean, n
}
