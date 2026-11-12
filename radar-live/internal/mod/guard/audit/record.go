package audit

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"time"

	"radarlive/internal/core"
)

// Field limits. Every field is reduced to a short token so request data cannot ride along even if
// an emitter passes some by mistake.
const (
	maxEventLen  = 32
	maxClassLen  = 24
	maxReasonLen = 48
)

// clean reduces s to [A-Za-z0-9_.:-] (anything else becomes '_') and at most max bytes.
func clean(s string, max int) string {
	if len(s) > max {
		s = s[:max]
	}
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == ':', c == '-':
		default:
			b[i] = '_'
		}
	}
	return string(b)
}

// cleanIP returns the canonical text of a literal address, "invalid" for anything else, "" for nothing.
func cleanIP(s string) string {
	if s == "" {
		return ""
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return "invalid"
	}
	return a.Unmap().WithZone("").String()
}

// record is the sanitised event that gets counted and logged.
type record struct {
	event, ip, class, reason string
	count                    int
}

func sanitize(ev core.AuditEvent) record {
	r := record{event: clean(ev.Event, maxEventLen), ip: cleanIP(ev.IP), class: clean(ev.Class, maxClassLen),
		reason: clean(ev.Reason, maxReasonLen), count: ev.Count}
	if r.event == "" {
		r.event = "unknown"
	}
	if r.count < 1 {
		r.count = 1
	}
	return r
}

// newLogger builds the JSON-lines logger: {"ts":...,"event":...,"ip":...,"class":...,"reason":...,"count":N}
// in that order, UTC seconds, no level, empty fields omitted.
func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.TimeKey:
				return slog.String("ts", a.Value.Time().UTC().Format(time.RFC3339))
			case slog.MessageKey:
				a.Key = "event"
			case slog.LevelKey:
				return slog.Attr{}
			}
			return a
		},
	}))
}

func writeRecord(log *slog.Logger, r record) {
	attrs := make([]slog.Attr, 0, 4)
	if r.ip != "" {
		attrs = append(attrs, slog.String("ip", r.ip))
	}
	if r.class != "" {
		attrs = append(attrs, slog.String("class", r.class))
	}
	if r.reason != "" {
		attrs = append(attrs, slog.String("reason", r.reason))
	}
	attrs = append(attrs, slog.Int("count", r.count))
	log.LogAttrs(context.Background(), slog.LevelInfo, r.event, attrs...)
}
