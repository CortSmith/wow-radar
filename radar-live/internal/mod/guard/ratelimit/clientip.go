package ratelimit

import (
	"crypto/sha256"
	"net/http"
	"net/netip"

	"radarlive/internal/netx"
)

// The trusted-proxy rules live in netx so that every guard resolves client addresses identically.
var (
	parseTrusted = netx.ParseTrusted
	presentedKey = netx.PresentedKey
)

func clientIP(r *http.Request, trusted []netip.Prefix) netip.Addr { return netx.ClientIP(r, trusted) }

// ipKey is the table id of an address: IPv4 individually, IPv6 aggregated to its /64.
func ipKey(a netip.Addr) [16]byte { return netx.Aggregate(a).As16() }

// keyID derives the table identity of an API key: the first 16 bytes of its SHA-256. The raw key is
// never stored or logged.
func keyID(raw string) [16]byte {
	sum := sha256.Sum256([]byte(raw))
	var id [16]byte
	copy(id[:], sum[:])
	return id
}
