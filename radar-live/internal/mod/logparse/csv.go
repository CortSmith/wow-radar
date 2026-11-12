package logparse

import (
	"fmt"
	"strings"
)

// initialFieldCap is a typical field count of an advanced log record.
const initialFieldCap = 40

// splitCSV splits a record on commas, honouring double quotes ("" escapes a quote).
func splitCSV(s string) ([]string, error) {
	out := make([]string, 0, initialFieldCap)
	for pos := 0; ; {
		if pos < len(s) && s[pos] == '"' {
			field, next, err := quotedField(s, pos)
			if err != nil {
				return nil, err
			}
			out = append(out, field)
			if next >= len(s) {
				return out, nil
			}
			pos = next + 1 // skip the comma
			continue
		}
		comma := strings.IndexByte(s[pos:], ',')
		if comma < 0 {
			return append(out, s[pos:]), nil
		}
		out = append(out, s[pos:pos+comma])
		pos += comma + 1
	}
}

// quotedField reads the quoted field starting at s[start]. It returns the unquoted text and the
// index just past the closing quote, which must be the end of s or a comma.
func quotedField(s string, start int) (string, int, error) {
	var b strings.Builder
	pos := start + 1
	for pos < len(s) {
		c := s[pos]
		if c != '"' {
			b.WriteByte(c)
			pos++
			continue
		}
		if pos+1 < len(s) && s[pos+1] == '"' {
			b.WriteByte('"')
			pos += 2
			continue
		}
		pos++
		if pos < len(s) && s[pos] != ',' {
			return "", 0, fmt.Errorf("%w: garbage after quote", ErrBadLine)
		}
		return b.String(), pos, nil
	}
	return "", 0, fmt.Errorf("%w: unterminated quote", ErrBadLine)
}
