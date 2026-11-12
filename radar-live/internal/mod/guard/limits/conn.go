package limits

import (
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

	"radarlive/internal/core"
)

// capListener refuses connections beyond its cap by closing them right after accept, so a flood
// costs one accept and one close per connection and never a goroutine or a buffer.
type capListener struct {
	net.Listener
	slots   chan struct{}
	refused atomic.Int64
	log     *slog.Logger
	class   string
	audit   *core.Audit
}

func newCapListener(ln net.Listener, max int, log *slog.Logger, class string, audit *core.Audit) *capListener {
	return &capListener{Listener: ln, slots: make(chan struct{}, max), log: log, class: class, audit: audit}
}

// Accept returns the next connection that fits under the cap.
func (l *capListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &capConn{Conn: conn, release: l.release}, nil
		default:
			l.refuse(conn)
		}
	}
}

func (l *capListener) release() { <-l.slots }

// refuse closes an over-cap connection and logs the first one and then every 1000th.
func (l *capListener) refuse(conn net.Conn) {
	_ = conn.Close()
	l.audit.Emit(core.AuditEvent{Event: "conn_refused", IP: peerIP(conn), Class: l.class, Reason: "conn_cap"})
	if n := l.refused.Add(1); n == 1 || n%1000 == 0 {
		l.log.Warn("connection cap reached; refusing connections", "class", l.class, "cap", cap(l.slots), "refused", n)
	}
}

// peerIP is the TCP peer's address without the port (empty when it is not an IP).
func peerIP(conn net.Conn) string {
	if ap, err := netip.ParseAddrPort(conn.RemoteAddr().String()); err == nil {
		return ap.Addr().Unmap().String()
	}
	return ""
}

// capConn gives its slot back exactly once when closed.
type capConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *capConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}
