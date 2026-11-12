package netx

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	got, err := ParseTrusted(" 127.0.0.1 , 10.0.0.0/8,::1,2001:db8::/32 ")
	if err != nil || len(got) != 4 {
		t.Fatalf("got %v, %v", got, err)
	}
	if got[0] != netip.MustParsePrefix("127.0.0.1/32") || got[2] != netip.MustParsePrefix("::1/128") {
		t.Fatalf("bare addresses must become host prefixes: %v", got)
	}
	if got, err := ParseTrusted(""); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	for _, bad := range []string{"nope", "10.0.0.0/33", "1.2.3.4,,x", "1.2.3.4/"} {
		if _, err := ParseTrusted(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestTrustedProxyClientIP(t *testing.T) {
	trusted, _ := ParseTrusted("127.0.0.1,10.0.0.0/8")
	req := func(remote string, hdr ...string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Add(hdr[i], hdr[i+1])
		}
		return r
	}
	cases := []struct {
		name string
		r    *http.Request
		want string
	}{
		{"no proxies configured ignores headers", req("203.0.113.5:1", "X-Forwarded-For", "1.1.1.1", "CF-Connecting-IP", "2.2.2.2"), "203.0.113.5"},
		{"spoofed XFF from untrusted peer", req("203.0.113.5:1", "X-Forwarded-For", "9.9.9.9"), "203.0.113.5"},
		{"spoofed CF header from untrusted peer", req("203.0.113.5:1", "CF-Connecting-IP", "9.9.9.9"), "203.0.113.5"},
		{"trusted peer, CF header", req("127.0.0.1:1", "CF-Connecting-IP", "198.51.100.20"), "198.51.100.20"},
		{"CF header wins over XFF", req("127.0.0.1:1", "CF-Connecting-IP", "198.51.100.20", "X-Forwarded-For", "198.51.100.30"), "198.51.100.20"},
		{"trusted peer, XFF", req("10.1.2.3:1", "X-Forwarded-For", "198.51.100.30"), "198.51.100.30"},
		{"XFF chain: rightmost untrusted is the client", req("10.1.2.3:1", "X-Forwarded-For", "6.6.6.6, 198.51.100.30, 10.9.9.9"), "198.51.100.30"},
		{"XFF split over header lines", req("10.1.2.3:1", "X-Forwarded-For", "6.6.6.6", "X-Forwarded-For", "198.51.100.31"), "198.51.100.31"},
		{"garbage XFF falls back to peer", req("10.1.2.3:1", "X-Forwarded-For", "not-an-ip"), "10.1.2.3"},
		{"garbage CF falls back to XFF", req("127.0.0.1:1", "CF-Connecting-IP", "zzz", "X-Forwarded-For", "198.51.100.32"), "198.51.100.32"},
		{"4in6 peer is unmapped", req("[::ffff:127.0.0.1]:1", "CF-Connecting-IP", "198.51.100.20"), "198.51.100.20"},
		{"unparsable RemoteAddr", req("garbage", "X-Forwarded-For", "1.1.1.1"), "invalid IP"},
	}
	for _, c := range cases {
		var tr []netip.Prefix
		if !strings.HasPrefix(c.name, "no proxies") {
			tr = trusted
		}
		if got := ClientIP(c.r, tr).String(); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestAggregateIPv6To64(t *testing.T) {
	a := Aggregate(netip.MustParseAddr("2001:db8:1:2:aaaa:bbbb:cccc:dddd"))
	b := Aggregate(netip.MustParseAddr("2001:db8:1:2:1111:2222:3333:4444"))
	c := Aggregate(netip.MustParseAddr("2001:db8:1:3::1"))
	if a != b || a == c {
		t.Fatalf("same /64 must match, different must not: %v %v %v", a, b, c)
	}
	v4 := netip.MustParseAddr("192.0.2.1")
	if Aggregate(v4) != v4 {
		t.Fatal("IPv4 is unchanged")
	}
}

func TestPresentedKey(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if _, ok := PresentedKey(r); ok {
		t.Fatal("no key")
	}
	r.Header.Set("X-View-Key", "abc")
	if v, ok := PresentedKey(r); !ok || v != "abc" {
		t.Fatal(v, ok)
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer  tok ")
	if v, ok := PresentedKey(r); !ok || v != "tok" {
		t.Fatal(v, ok)
	}
	r.Header.Set("Authorization", "Bearer ")
	if _, ok := PresentedKey(r); ok {
		t.Fatal("empty bearer is no key")
	}
}
