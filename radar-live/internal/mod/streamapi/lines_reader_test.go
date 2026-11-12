package streamapi

import (
	"io"
	"strings"
	"testing"
)

func TestLineReader(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		max     int
		want    []string
		tooLong []bool
	}{
		{"lf", "a\nb\n", 10, []string{"a", "b"}, []bool{false, false}},
		{"crlf", "a\r\nb\r\n", 10, []string{"a", "b"}, []bool{false, false}},
		{"no trailing newline", "a\nb", 10, []string{"a", "b"}, []bool{false, false}},
		{"blank lines kept", "a\n\nb\n", 10, []string{"a", "", "b"}, []bool{false, false, false}},
		{"too long is flagged and skipped", "aaaaaaaaaaaa\nok\n", 5, []string{"", "ok"}, []bool{true, false}},
		{"too long at eof", "aaaaaaaaaaaa", 5, []string{""}, []bool{true}},
		{"exactly max", "aaaaa\n", 5, []string{"aaaaa"}, []bool{false}},
		{"empty", "", 5, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lr := newLineReader(strings.NewReader(tc.in), tc.max)
			var got []string
			var long []bool
			for {
				line, tl, err := lr.next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				got, long = append(got, line), append(long, tl)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") || len(long) != len(tc.tooLong) {
				t.Fatalf("got %q %v want %q %v", got, long, tc.want, tc.tooLong)
			}
			for i := range long {
				if long[i] != tc.tooLong[i] {
					t.Fatalf("tooLong %v want %v", long, tc.tooLong)
				}
			}
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestLineReaderPropagatesErrors(t *testing.T) {
	lr := newLineReader(io.MultiReader(strings.NewReader("partial"), errReader{}), 100)
	if _, _, err := lr.next(); err != io.ErrUnexpectedEOF {
		t.Fatalf("err %v", err)
	}
}
