package streamapi

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

const readBufferBytes = 64 << 10

// lineReader splits a stream into lines of at most max bytes. A longer line is reported once
// (tooLong, empty text) and the rest of it is discarded, so memory stays bounded by max plus the buffer.
type lineReader struct {
	br  *bufio.Reader
	max int
	buf []byte
}

func newLineReader(r io.Reader, max int) *lineReader {
	return &lineReader{br: bufio.NewReaderSize(r, readBufferBytes), max: max}
}

// next returns the next line without its line ending. It returns io.EOF when no data is left.
func (l *lineReader) next() (line string, tooLong bool, err error) {
	l.buf = l.buf[:0]
	got := false
	for {
		chunk, rerr := l.br.ReadSlice('\n')
		if len(chunk) > 0 {
			got = true
			if !tooLong && len(l.buf)+len(chunk) > l.max+len("\r\n") {
				tooLong = true
			}
			if !tooLong {
				l.buf = append(l.buf, chunk...)
			}
		}
		if errors.Is(rerr, bufio.ErrBufferFull) {
			continue
		}
		if rerr != nil && !(rerr == io.EOF && got) {
			return "", false, rerr
		}
		break
	}
	if tooLong {
		return "", true, nil
	}
	text := bytes.TrimRight(l.buf, "\r\n")
	if len(text) > l.max {
		return "", true, nil
	}
	return string(text), false, nil
}
