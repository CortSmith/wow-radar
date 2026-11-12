package logparse

import (
	"errors"
	"slices"
	"testing"
)

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{`a,"b,c",d`, []string{"a", "b,c", "d"}},
		{`"say ""hi""",x`, []string{`say "hi"`, "x"}},
		{"a,,c", []string{"a", "", "c"}},
		{"a,", []string{"a", ""}},
		{`"",x`, []string{"", "x"}},
		{"single", []string{"single"}},
		{`x,"last"`, []string{"x", "last"}},
	}
	for _, c := range cases {
		got, err := splitCSV(c.in)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%q: got %q err %v want %q", c.in, got, err, c.want)
		}
	}
}

func TestSplitCSVRejects(t *testing.T) {
	for _, in := range []string{`a,"unterminated`, `a,"b"c,d`} {
		if _, err := splitCSV(in); !errors.Is(err, ErrBadLine) {
			t.Errorf("%q: want ErrBadLine, got %v", in, err)
		}
	}
}
