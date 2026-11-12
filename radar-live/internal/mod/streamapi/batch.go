package streamapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"radarlive/internal/core"
)

// handleLines takes one batch, either text/plain (one line per row) or {"lines":["..."]}, within
// the configured line and byte caps, feeds it and replies with the summary.
func (m *Module) handleLines(w http.ResponseWriter, r *http.Request) {
	source, rs, ok := m.params(w, r)
	if !ok {
		return
	}
	cfg := m.host.Config
	r.Body = http.MaxBytesReader(w, r.Body, int64(cfg.Int(SettingLinesBytes)))
	lines, status, err := readBatch(r, cfg.Int(SettingLinesCount))
	if err != nil {
		core.WriteError(w, status, errorCode(status), err.Error())
		return
	}
	s := m.open(w, r, source, rs)
	if s == nil {
		return
	}
	defer m.registry().close(s)
	if ended := m.throttleBatch(r.Context(), s, lines); ended != "" {
		w.Header().Set("Retry-After", "5")
		m.host.Audit.EmitRequest(r, core.AuditEvent{Event: "stream_refused", Class: "ingest", Reason: "rate"})
		core.WriteError(w, http.StatusTooManyRequests, errCodeRate, "ingest rate limit exceeded; slow down and retry")
		return
	}
	for _, line := range lines {
		if line == overlong || len(line) > MaxLineBytes {
			s.lines.Add(1)
			s.rejected.Add(1)
			continue
		}
		m.feed(r.Context(), s, line)
	}
	core.WriteJSON(w, http.StatusOK, s.summary(m.now(), endedEOF))
}

func errorCode(status int) string {
	if status == http.StatusRequestEntityTooLarge {
		return "too_large"
	}
	return "bad_request"
}

// readBatch decodes the body; the returned status is meaningful only with an error.
func readBatch(r *http.Request, maxLines int) ([]string, int, error) {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var lines []string
	var err error
	if media == "application/json" {
		lines, err = readJSONLines(r.Body)
	} else {
		lines, err = readTextLines(r.Body, maxLines)
	}
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return nil, http.StatusRequestEntityTooLarge, errors.New("batch body too large")
	case errors.Is(err, errTooManyLines):
		return nil, http.StatusRequestEntityTooLarge, err
	case err != nil:
		return nil, http.StatusBadRequest, err
	}
	if len(lines) > maxLines {
		return nil, http.StatusRequestEntityTooLarge, errTooManyLines
	}
	return lines, 0, nil
}

// overlong stands in for a text line that exceeded MaxLineBytes (its content was discarded).
const overlong = "<overlong>"

var errTooManyLines = errors.New("too many lines in batch")

func readJSONLines(body io.Reader) ([]string, error) {
	var doc struct {
		Lines []string `json:"lines"`
	}
	if err := json.NewDecoder(body).Decode(&doc); err != nil {
		return nil, err
	}
	return doc.Lines, nil
}

func readTextLines(body io.Reader, maxLines int) ([]string, error) {
	lr := newLineReader(body, MaxLineBytes)
	var lines []string
	for {
		line, tooLong, err := lr.next()
		if errors.Is(err, io.EOF) {
			return lines, nil
		}
		if err != nil {
			return nil, err
		}
		if tooLong {
			line = overlong
		}
		if lines = append(lines, line); len(lines) > maxLines {
			return nil, errTooManyLines
		}
	}
}

// throttleBatch charges the whole batch to the limits and waits when they are short. It returns
// endedRate when the wait would exceed the patience (nothing is fed then).
func (m *Module) throttleBatch(ctx context.Context, s *session, lines []string) string {
	size := 0
	for _, l := range lines {
		size += len(l) + 1
	}
	wait, ok := m.charge(s, float64(len(lines)), float64(size), m.lim.patience)
	if !ok {
		return endedRate
	}
	if wait > 0 {
		s.throttledMs.Add(wait.Milliseconds())
		if m.sleep(ctx, m.running(), wait) != nil {
			return endedRate
		}
	}
	return ""
}
