package validate

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzQuery feeds arbitrary raw query strings through every parser: no panic, accepted text is safe,
// accepted numbers are in range, and errors never repeat the input.
func FuzzQuery(f *testing.F) {
	for _, s := range []string{
		"q=Ann&rs=pvp&mapId=1", "q=%00", "q=%e2%80%ae", "mapId=99999999999999999999", "guid=Player-1&guid=Player-2",
		"limit=-1&offset=%2b1", "grp=%3Cscript%3E", "trail=1&since=0", "q=%27%20OR%201%3D1--", "%zz=1", "q=a%0d%0ab",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		v, err := url.ParseQuery(raw)
		if err != nil {
			return
		}
		q := NewQuery(v, "q", "rs", "mapId", "guid", "grp", "limit", "offset", "since", "trail")
		text := q.Text("q", 64)
		q.Enum("grp", "any", true, "any", "friendly", "hostile", "other")
		n, _ := q.Int("mapId", 0, 65535)
		guid := q.GUID("guid", false)
		q.Flag("trail")
		if q.Err() != nil {
			for _, vs := range v {
				for _, s := range vs {
					if len(s) >= 4 && strings.Contains(q.Err().Error(), s) {
						t.Fatalf("error %q echoes input %q", q.Err(), s)
					}
				}
			}
			return
		}
		if !IsSafeText(text) || len(text) > 64 || n < 0 || n > 65535 || (guid != "" && !IsGUID(guid)) {
			t.Fatalf("accepted unsafe values text=%q n=%d guid=%q", text, n, guid)
		}
	})
}

// FuzzDecodeJSON checks the strict decoder never panics and that accepted documents honour the limits.
func FuzzDecodeJSON(f *testing.F) {
	for _, s := range []string{`{"a":1}`, `{"list":[1,2,3]}`, `{"sub":{"sub":{"a":1}}}`, `[`, `{"a":1}{`, "\x00", `{"s":["x"]}`, `{"a":1e999}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, doc string) {
		var v sample
		if err := DecodeJSON(strings.NewReader(doc), &v, JSONLimits{MaxDepth: 4, MaxArray: 8, MaxString: 16}); err != nil {
			return
		}
		if len(v.List) > 8 || len(v.S) > 8 {
			t.Fatalf("limits bypassed: %+v", v)
		}
	})
}
