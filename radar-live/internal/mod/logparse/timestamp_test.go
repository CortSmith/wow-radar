package logparse

import (
	"errors"
	"testing"
	"time"
)

func TestParseTimestamp(t *testing.T) {
	est := time.FixedZone("EST", -5*3600)
	cases := []struct {
		name string
		in   string
		loc  *time.Location
		want time.Time
	}{
		{"offset hours", "11/5/2026 20:15:30.123-5", nil, time.Date(2026, 11, 6, 1, 15, 30, 123e6, time.UTC)},
		{"offset with minutes", "1/2/2026 10:00:00.000+5:30", nil, time.Date(2026, 1, 2, 4, 30, 0, 0, time.UTC)},
		{"negative minutes", "1/2/2026 10:00:00.500-3:30", nil, time.Date(2026, 1, 2, 13, 30, 0, 500e6, time.UTC)},
		{"zero offset", "6/15/2026 00:00:00.000+0", nil, time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)},
		{"no offset uses fallback", "11/5/2026 20:15:30.123", est, time.Date(2026, 11, 6, 1, 15, 30, 123e6, time.UTC)},
		{"no offset nil fallback is utc", "11/5/2026 20:15:30.123", nil, time.Date(2026, 11, 5, 20, 15, 30, 123e6, time.UTC)},
		{"own offset beats fallback", "11/5/2026 20:15:30.000-0", est, time.Date(2026, 11, 5, 20, 15, 30, 0, time.UTC)},
		{"no fraction", "2/3/2026 01:02:03", nil, time.Date(2026, 2, 3, 1, 2, 3, 0, time.UTC)},
		{"short fraction", "2/3/2026 01:02:03.5", nil, time.Date(2026, 2, 3, 1, 2, 3, 500e6, time.UTC)},
		{"long fraction truncated", "2/3/2026 01:02:03.1234567891", nil, time.Date(2026, 2, 3, 1, 2, 3, 123456789, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseTimestamp(c.in, c.loc)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestParseTimestampRejects(t *testing.T) {
	bad := []string{
		"", "garbage", "11/5/2026", "11/5 20:00:00.000", "13/5/2026 20:00:00.000", "0/5/2026 20:00:00.000",
		"2/30/2026 20:00:00.000", "11/5/2026 25:00:00.000", "11/5/2026 20:61:00.000", "11/5/2026 20:00",
		"11/5/2026 20:00:00.abc", "11/5/2026 20:00:00.000-x", "11/5/2026 20:00:00.000-5:y", "a/b/c 1:2:3",
		"11/5/2026 aa:00:00", "11/5/2026 20:00:00.000+99",
	}
	for _, in := range bad {
		if _, err := parseTimestamp(in, nil); !errors.Is(err, ErrBadLine) {
			t.Errorf("%q: want ErrBadLine, got %v", in, err)
		}
	}
}
