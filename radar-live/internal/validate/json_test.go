package validate

import (
	"errors"
	"strings"
	"testing"
)

type sample struct {
	A    int      `json:"a"`
	List []int    `json:"list"`
	Sub  *sample  `json:"sub"`
	S    []string `json:"s"`
}

func TestDecodeJSONStrict(t *testing.T) {
	lim := JSONLimits{MaxDepth: 4, MaxArray: 3, MaxString: 8}
	good := []string{`{"a":1}`, `{"list":[1,2,3]}`, `{"sub":{"a":2}}`, ` {"a":1} `, `{"s":["12345678"]}`}
	bad := map[string]string{
		"unknown field":   `{"zzz":1}`,
		"trailing object": `{"a":1}{"a":2}`,
		"trailing junk":   `{"a":1} x`,
		"trailing nul":    "{\"a\":1}\x00",
		"deep":            `{"sub":{"sub":{"sub":{"sub":{"a":1}}}}}`,
		"array too big":   `{"list":[1,2,3,4]}`,
		"string too long": `{"s":["123456789"]}`,
		"key too long":    `{"123456789":1}`,
		"wrong type":      `{"a":"x"}`,
		"empty":           ``,
		"not object":      `[1]`,
		"float to int":    `{"a":1.5}`,
		"truncated":       `{"a":`,
		"nested arrays":   `{"list":[[[[[1]]]]]}`,
	}
	for _, s := range good {
		var v sample
		if err := DecodeJSON(strings.NewReader(s), &v, lim); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for name, s := range bad {
		var v sample
		err := DecodeJSON(strings.NewReader(s), &v, lim)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if codeOf(t, err) != CodeJSON {
			t.Errorf("%s: code %q", name, codeOf(t, err))
		}
	}
}

type failReader struct{}

var errBoom = errors.New("boom")

func (failReader) Read([]byte) (int, error) { return 0, errBoom }

func TestDecodeJSONPassesReadErrors(t *testing.T) {
	var v sample
	err := DecodeJSON(failReader{}, &v, JSONLimits{})
	if !errors.Is(err, errBoom) {
		t.Fatalf("read error lost: %v", err)
	}
	var e *Error
	if errors.As(err, &e) {
		t.Fatal("read error must not be reported as a validation error")
	}
}

func TestDecodeJSONErrorDoesNotEchoBody(t *testing.T) {
	var v sample
	err := DecodeJSON(strings.NewReader(`{"<img src=x onerror=alert(1)>":1}`), &v, JSONLimits{})
	if err == nil || strings.Contains(err.Error(), "img") {
		t.Fatalf("echo: %v", err)
	}
}
