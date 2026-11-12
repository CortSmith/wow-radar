package validate

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func vals(t *testing.T, raw string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not *validate.Error", err)
	}
	return e.Code
}

func TestQueryRejectsDuplicatesAndUnknown(t *testing.T) {
	q := NewQuery(vals(t, "rs=rp&rs=pvp"), "rs")
	if got := codeOf(t, q.Err()); got != CodeDuplicate {
		t.Fatalf("code = %q", got)
	}
	q = NewQuery(vals(t, "rs=rp&evil=1"), "rs")
	if got := codeOf(t, q.Err()); got != CodeUnknown {
		t.Fatalf("code = %q", got)
	}
	if NewQuery(vals(t, "rs=rp"), "rs").Err() != nil {
		t.Fatal("clean query rejected")
	}
}

func TestErrorsNeverEchoInput(t *testing.T) {
	payload := "<script>alert(1)</script>"
	for _, q := range []*Query{
		NewQuery(url.Values{"grp": {payload}}, "grp"),
		NewQuery(url.Values{payload: {"1"}}, "grp"),
	} {
		q.Enum("grp", "any", true, "any", "friendly")
		if err := q.Err(); err == nil || strings.Contains(err.Error(), "script") {
			t.Fatalf("error %v echoes input", err)
		}
	}
}

func TestQueryText(t *testing.T) {
	bad := map[string]string{
		"nul":       "a\x00b",
		"newline":   "a\nb",
		"cr":        "a\rb",
		"tab":       "a\tb",
		"del":       "a\x7fb",
		"c1":        "a\u0085b",
		"bidi":      "a‮b",
		"isolate":   "a⁦b",
		"zwsp":      "a​b",
		"linesep":   "a b",
		"badutf8":   "a\xffb",
		"too long":  strings.Repeat("a", 65),
		"surrogate": "a\xed\xa0\x80b",
	}
	for name, v := range bad {
		q := NewQuery(url.Values{"q": {v}}, "q")
		q.Text("q", 64)
		if q.Err() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, v := range []string{"", "Ann", "Ann-Realm", "Jörg", "名前", "a b", "O'Neil", "%_\\"} {
		q := NewQuery(url.Values{"q": {v}}, "q")
		if got := q.Text("q", 64); q.Err() != nil || got != strings.TrimSpace(v) {
			t.Errorf("%q: got %q err %v", v, got, q.Err())
		}
	}
}

func TestQueryEnum(t *testing.T) {
	q := NewQuery(url.Values{"grp": {"Hostile"}}, "grp")
	if got := q.Enum("grp", "any", true, "any", "hostile"); got != "hostile" || q.Err() != nil {
		t.Fatalf("got %q %v", got, q.Err())
	}
	q = NewQuery(url.Values{}, "grp")
	if got := q.Enum("grp", "any", true, "any", "hostile"); got != "any" {
		t.Fatalf("default = %q", got)
	}
	q = NewQuery(url.Values{"scope": {"All"}}, "scope")
	q.Enum("scope", "all", false, "all", "zone")
	if codeOf(t, q.Err()) != CodeEnum {
		t.Fatal("case-sensitive enum accepted wrong case")
	}
}

func TestQueryInt(t *testing.T) {
	tests := []struct {
		in      string
		ok      bool
		want    int64
		present bool
	}{
		{"", true, 0, false}, {"0", true, 0, true}, {"10", true, 10, true}, {"100", true, 100, true},
		{"101", false, 0, true}, {"-1", false, 0, true}, {"+5", false, 0, true}, {"05", false, 0, true},
		{"1e2", false, 0, true}, {"1.0", false, 0, true}, {" 5", false, 0, true}, {"0x10", false, 0, true},
		{"99999999999999999999", false, 0, true}, {"５", false, 0, true},
	}
	for _, tt := range tests {
		q := NewQuery(url.Values{"n": {tt.in}}, "n")
		got, present := q.Int("n", 0, 100)
		if (q.Err() == nil) != tt.ok {
			t.Errorf("%q: err = %v, want ok=%v", tt.in, q.Err(), tt.ok)
			continue
		}
		if tt.ok && (got != tt.want || present != tt.present) {
			t.Errorf("%q: got %d,%v", tt.in, got, present)
		}
	}
}

func TestQueryFlagAndGUID(t *testing.T) {
	for in, want := range map[string]bool{"": false, "0": false, "1": true} {
		q := NewQuery(url.Values{"trail": {in}}, "trail")
		if got := q.Flag("trail"); got != want || q.Err() != nil {
			t.Errorf("flag %q = %v %v", in, got, q.Err())
		}
	}
	for _, in := range []string{"true", "yes", "2", "01"} {
		q := NewQuery(url.Values{"trail": {in}}, "trail")
		q.Flag("trail")
		if q.Err() == nil {
			t.Errorf("flag %q accepted", in)
		}
	}
	good := []string{"Player-1", "Player-1234-0ABCDEF1", "Player-" + strings.Repeat("A", 40)}
	bad := []string{"Creature-1", "player-1", "Player-", "Player-1 ", "Player-1\n", "Player-1;DROP", "Player-" + strings.Repeat("A", 41),
		"Player-1'--", "Player-%00", "Player-é", "../Player-1"}
	for _, g := range good {
		q := NewQuery(url.Values{"guid": {g}}, "guid")
		if got := q.GUID("guid", true); got != g || q.Err() != nil {
			t.Errorf("guid %q: %v", g, q.Err())
		}
	}
	for _, g := range bad {
		q := NewQuery(url.Values{"guid": {g}}, "guid")
		q.GUID("guid", true)
		if q.Err() == nil {
			t.Errorf("guid %q accepted", g)
		}
	}
	q := NewQuery(url.Values{}, "guid")
	q.GUID("guid", true)
	if codeOf(t, q.Err()) != CodeRequired {
		t.Fatal("missing required guid")
	}
	q = NewQuery(url.Values{}, "guid")
	if q.GUID("guid", false) != "" || q.Err() != nil {
		t.Fatal("optional guid")
	}
}

func TestFirstErrorSticks(t *testing.T) {
	q := NewQuery(url.Values{"a": {"x"}, "b": {"y"}}, "a", "b")
	q.Int("a", 0, 5)
	first := q.Err()
	q.Int("b", 0, 5)
	if q.Err() != first {
		t.Fatal("error replaced")
	}
}

func TestQueryTotalSizeCap(t *testing.T) {
	v := url.Values{}
	allowed := []string{}
	for i := 0; i < 40; i++ {
		k := "k" + strings.Repeat("x", i)
		v.Set(k, strings.Repeat("a", 100))
		allowed = append(allowed, k)
	}
	if NewQuery(v, allowed...).Err() == nil {
		t.Fatal("oversized query accepted")
	}
}

func TestSafeText(t *testing.T) {
	if got, n := CleanText("A‮b\x00c\nd\te", 48); got != "Abc d e" || n == 0 {
		t.Fatalf("CleanText = %q,%d", got, n)
	}
	if got, n := CleanText("  ok  ", 48); got != "ok" || n != 0 {
		t.Fatalf("trim = %q,%d", got, n)
	}
	if got, n := CleanText(strings.Repeat("é", 100), 10); len([]rune(got)) != 10 || n == 0 {
		t.Fatalf("truncate = %q,%d", got, n)
	}
	if got, _ := CleanText("a\xffb", 10); got != "ab" {
		t.Fatalf("utf8 = %q", got)
	}
	if !IsSafeText("fine é 名") || IsSafeText("a\nb") || IsSafeText("a‮b") || IsSafeText("a\xff") {
		t.Fatal("IsSafeText")
	}
}
