package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// JSONLimits bound the shape of a decoded document. Zero fields use the defaults.
type JSONLimits struct {
	MaxDepth  int // nesting of objects and arrays
	MaxArray  int // elements in any one array
	MaxString int // bytes in any string or object key
}

// Defaults suit a ping: arrays up to the trail cap, strings well above any name.
const (
	DefaultMaxDepth  = 8
	DefaultMaxArray  = 5000
	DefaultMaxString = 1024
)

func (l JSONLimits) withDefaults() JSONLimits {
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	if l.MaxArray <= 0 {
		l.MaxArray = DefaultMaxArray
	}
	if l.MaxString <= 0 {
		l.MaxString = DefaultMaxString
	}
	return l
}

// DecodeJSON reads one JSON document from r into dst. It rejects unknown fields, trailing data,
// documents deeper than MaxDepth, arrays longer than MaxArray and strings longer than MaxString.
// Failures to read r (for example http.MaxBytesError) are returned unchanged; every other failure
// is a *Error with CodeJSON whose message never repeats the body.
func DecodeJSON(r io.Reader, dst any, lim JSONLimits) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	lim = lim.withDefaults()
	if err := scanJSON(data, lim); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return jsonErr(classify(err))
	}
	return nil
}

func jsonErr(msg string) *Error { return &Error{Code: CodeJSON, Field: "body", Msg: msg} }

// classify maps a decoder error to a fixed message so the body is never echoed.
func classify(err error) string {
	var typeErr *json.UnmarshalTypeError
	var syntax *json.SyntaxError
	switch {
	case errors.As(err, &typeErr):
		return "a field has the wrong type"
	case errors.As(err, &syntax), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "malformed JSON"
	}
	return "unknown field or invalid value"
}

type frame struct {
	array     bool
	count     int
	expectKey bool
}

// scanJSON walks the token stream once to enforce the shape limits and the single-document rule.
func scanJSON(data []byte, lim JSONLimits) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var stack []frame
	started := false
	// value is called when a value (scalar or container start) begins.
	value := func() error {
		if n := len(stack); n > 0 && stack[n-1].array {
			stack[n-1].count++
			if stack[n-1].count > lim.MaxArray {
				return jsonErr("an array has too many elements")
			}
		}
		return nil
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			if !started || len(stack) > 0 {
				return jsonErr("malformed JSON")
			}
			return nil
		}
		if err != nil {
			return jsonErr("malformed JSON")
		}
		if started && len(stack) == 0 {
			return jsonErr("unexpected data after the document")
		}
		started = true
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				if err := value(); err != nil {
					return err
				}
				if len(stack) >= lim.MaxDepth {
					return jsonErr("the document is nested too deeply")
				}
				stack = append(stack, frame{array: t == '[', expectKey: t == '{'})
			default:
				stack = stack[:len(stack)-1]
				if n := len(stack); n > 0 && !stack[n-1].array {
					stack[n-1].expectKey = true
				}
			}
		case string:
			if len(t) > lim.MaxString {
				return jsonErr("a string is too long")
			}
			if n := len(stack); n > 0 && !stack[n-1].array && stack[n-1].expectKey {
				stack[n-1].expectKey = false
				continue
			}
			if err := value(); err != nil {
				return err
			}
			markValueDone(stack)
		default:
			if err := value(); err != nil {
				return err
			}
			markValueDone(stack)
		}
	}
}

func markValueDone(stack []frame) {
	if n := len(stack); n > 0 && !stack[n-1].array {
		stack[n-1].expectKey = true
	}
}
