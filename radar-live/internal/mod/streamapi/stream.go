package streamapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"radarlive/internal/core"
)

// Reasons a connection ended, reported in the summary.
const (
	endedEOF      = "eof"
	endedIdle     = "idle"
	endedMaxBytes = "max-bytes"
	endedShutdown = "shutdown"
	endedError    = "error"
)

// idleReader arms a read deadline before every read, so a silent client is cut off after idle. A line
// that is still unfinished lineTimeout after its first byte also cuts the stream (slowloris: one byte
// now and then keeps an idle timer alive forever).
type idleReader struct {
	r           io.Reader
	rc          *http.ResponseController
	idle        time.Duration
	lineTimeout time.Duration   // 0 = off
	done        context.Context // run context; when it ends the deadline is pulled to "now"
	n           int64
	partial     time.Time // when the unfinished line began, zero between lines
	slow        bool      // the stream was cut by lineTimeout
}

func (i *idleReader) Read(p []byte) (int, error) {
	now := time.Now()
	deadline := now.Add(i.idle)
	if i.lineTimeout > 0 && !i.partial.IsZero() {
		if limit := i.partial.Add(i.lineTimeout); limit.Before(deadline) {
			deadline = limit
		}
		if !now.Before(i.partial.Add(i.lineTimeout)) {
			i.slow = true
			return 0, os.ErrDeadlineExceeded
		}
	}
	_ = i.rc.SetReadDeadline(deadline)
	if i.done.Err() != nil {
		_ = i.rc.SetReadDeadline(time.Now())
	}
	n, err := i.r.Read(p)
	i.n += int64(n)
	if n > 0 {
		i.track(p[:n])
	}
	if err != nil && errors.Is(err, os.ErrDeadlineExceeded) && !i.partial.IsZero() &&
		i.lineTimeout > 0 && !time.Now().Before(i.partial.Add(i.lineTimeout)) {
		i.slow = true
	}
	return n, err
}

// track notes whether the bytes just read leave a line unfinished.
func (i *idleReader) track(b []byte) {
	if k := bytes.LastIndexByte(b, '\n'); k >= 0 {
		i.partial = time.Time{}
		b = b[k+1:]
	}
	if len(b) > 0 && i.partial.IsZero() {
		i.partial = time.Now()
	}
}

// handleStream reads a long-lived body line by line until the client closes it, the connection
// goes idle, the byte cap is reached or the server shuts down, then replies with the summary.
func (m *Module) handleStream(w http.ResponseWriter, r *http.Request) {
	source, rs, ok := m.params(w, r)
	if !ok {
		return
	}
	s := m.open(w, r, source, rs)
	if s == nil {
		return
	}
	defer m.registry().close(s)
	cfg := m.host.Config
	rc := http.NewResponseController(w)
	run := m.running()
	stop := context.AfterFunc(run, func() { _ = rc.SetReadDeadline(time.Now()) })
	defer stop()
	body := &idleReader{r: r.Body, rc: rc, idle: cfg.Duration(SettingStreamIdle), lineTimeout: m.lim.lineTimeout, done: run}
	ended := m.pump(r.Context(), s, body, int64(cfg.Int(SettingStreamBytes)), run)
	_ = rc.SetReadDeadline(time.Time{})
	core.WriteJSON(w, http.StatusOK, s.summary(m.now(), ended))
}

// pump feeds lines from body to the parsers and returns why it stopped. Every line is charged to the
// source and key limits; when they are short the reader sleeps, which stops reading and so pushes
// back on the sender through TCP. Throttling that never lets up for the patience ends the stream.
func (m *Module) pump(ctx context.Context, s *session, body *idleReader, maxBytes int64, run context.Context) string {
	lr := newLineReader(body, MaxLineBytes)
	var charged int64
	for {
		line, tooLong, err := lr.next()
		if err != nil {
			if body.slow {
				return endedSlow
			}
			return endReason(err, run)
		}
		if ended := m.throttle(ctx, s, float64(body.n-charged)); ended != "" {
			return ended
		}
		charged = body.n
		if tooLong {
			s.rejected.Add(1)
			s.lines.Add(1)
		} else {
			m.feed(ctx, s, line)
		}
		if body.n > maxBytes {
			return endedMaxBytes
		}
	}
}

// throttle charges one line of size bytes and waits if needed. It returns endedRate when the
// stream has been slowed for longer than the patience, or the end reason when the wait was cut short.
func (m *Module) throttle(ctx context.Context, s *session, size float64) string {
	wait, _ := m.charge(s, 1, size, 0)
	if wait <= 0 {
		s.throttleSince = time.Time{}
		return ""
	}
	now := m.now()
	if s.throttleSince.IsZero() {
		s.throttleSince = now
	}
	if m.lim.patience > 0 && now.Add(wait).Sub(s.throttleSince) > m.lim.patience {
		return endedRate
	}
	s.throttledMs.Add(wait.Milliseconds())
	if err := m.sleep(ctx, m.running(), wait); err != nil {
		if m.running().Err() != nil {
			return endedShutdown
		}
		return endedError
	}
	return ""
}

func endReason(err error, run context.Context) string {
	switch {
	case errors.Is(err, io.EOF):
		return endedEOF
	case run.Err() != nil:
		return endedShutdown
	case errors.Is(err, os.ErrDeadlineExceeded):
		return endedIdle
	}
	return endedError
}
