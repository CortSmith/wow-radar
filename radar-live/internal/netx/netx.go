// Package netx holds the small, pure network helpers shared by the guard modules: parsing trusted
// proxy lists, resolving the client address of a request under the trusted-proxy rules, aggregating
// IPv6 addresses, and finding the API key a request carries. It is not a module (modules may not
// import each other), so the rate limiter and the abuse guard resolve client addresses identically.
package netx

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

const (
	headerCF          = "CF-Connecting-IP"
	headerXFF         = "X-Forwarded-For"
	headerView        = "X-View-Key"
	headerIngest      = "X-Ingest-Key"
	headerAuth        = "Authorization"
	bearerPrefix      = "Bearer "
	ipv6AggregateBits = 64
)

// ParseTrusted parses a comma, space or semicolon separated list of CIDRs and bare addresses.
func ParseTrusted(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q is neither an address nor a CIDR", part)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	if strings.Contains(s, ",,") || strings.HasSuffix(strings.TrimSpace(s), ",") {
		return nil, fmt.Errorf("trusted proxies %q has an empty entry", s)
	}
	return out, nil
}

// IsTrusted reports whether a is inside one of the prefixes.
func IsTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// PeerAddr returns the TCP peer address of a request (unmapped); the zero Addr means unparsable.
func PeerAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// ClientIP returns the address a request is attributed to. The TCP peer is the answer unless the
// peer is a configured trusted proxy; only then are CF-Connecting-IP and X-Forwarded-For believed
// (the latter read right to left, skipping further trusted proxies). The zero Addr means the peer
// address could not be parsed.
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	peer := PeerAddr(r)
	if !peer.IsValid() || !IsTrusted(peer, trusted) {
		return peer
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get(headerCF))); err == nil {
		return a.Unmap()
	}
	var chain []string
	for _, line := range r.Header.Values(headerXFF) {
		chain = append(chain, strings.Split(line, ",")...)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			break // an unparsable hop makes everything left of it untrustworthy
		}
		a = a.Unmap()
		if !IsTrusted(a, trusted) {
			return a
		}
	}
	return peer
}

// Aggregate maps an address to its accounting identity: IPv4 individually, IPv6 to its /64 so that a
// host cannot multiply its budget by rotating addresses inside its own prefix.
func Aggregate(a netip.Addr) netip.Addr {
	if a.Is6() {
		if p, err := a.Prefix(ipv6AggregateBits); err == nil {
			return p.Addr()
		}
	}
	return a
}

// PresentedKey returns the API key a request carries, if any.
func PresentedKey(r *http.Request) (string, bool) {
	for _, h := range []string{headerIngest, headerView} {
		if v := r.Header.Get(h); v != "" {
			return v, true
		}
	}
	if auth := r.Header.Get(headerAuth); strings.HasPrefix(auth, bearerPrefix) {
		if v := strings.TrimSpace(strings.TrimPrefix(auth, bearerPrefix)); v != "" {
			return v, true
		}
	}
	return "", false
}
