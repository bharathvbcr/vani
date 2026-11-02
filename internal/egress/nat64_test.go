package egress

import (
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

// nat64WellKnown is the RFC 6052 embedding of v4 in 64:ff9b::/96 (prefix
// length 96: the IPv4 address is the last 32 bits).
func nat64WellKnown(v4 string) string {
	ip := netip.MustParseAddr(v4).As4()
	var b [16]byte
	b[0], b[1], b[2], b[3] = 0x00, 0x64, 0xff, 0x9b
	copy(b[12:], ip[:])
	return netip.AddrFrom16(b).String()
}

// nat64LocalUse is the RFC 6052 embedding of v4 in 64:ff9b:1::/48 (prefix
// length 48). u is the octet that must be zero; a non-zero u is not an
// embedding. Suffix bits are left zero here.
func nat64LocalUse(v4 string, u byte) string {
	ip := netip.MustParseAddr(v4).As4()
	var b [16]byte
	b[0], b[1], b[2], b[3], b[4], b[5] = 0x00, 0x64, 0xff, 0x9b, 0x00, 0x01
	b[6], b[7] = ip[0], ip[1]
	b[8] = u
	b[9], b[10] = ip[2], ip[3]
	return netip.AddrFrom16(b).String()
}

// DNS64 synthesizes 64:ff9b::/96 from a public A record. That address is the
// public host, so it is reachable. The same prefix synthesized from a blocked
// v4 (loopback, private, link-local, CGNAT, documentation, unspecified,
// reserved) stays blocked, and so does a local-use embedding whose u octet is
// not zero. 6to4 and Teredo stay blocked outright: this does not unwrap them.
func TestNAT64ReachesOnlyAPublicEmbeddedV4(t *testing.T) {
	public := nat64WellKnown("3.161.225.10")
	same := nat64WellKnown(publicIP)
	if reason, ok := blockedAddr(netip.MustParseAddr(public)); ok {
		t.Errorf("%s (3.161.225.10) blocked as %q", public, reason)
	}
	if reason, ok := blockedAddr(netip.MustParseAddr(nat64LocalUse("8.8.8.8", 0))); ok {
		t.Errorf("local-use embedding of 8.8.8.8 blocked as %q", reason)
	}
	// A non-zero suffix is still that v4 (RFC 6052 says the translator ignores it).
	for _, base := range []string{public, nat64LocalUse("8.8.8.8", 0)} {
		withSuffix := netip.MustParseAddr(base)
		b := withSuffix.As16()
		b[15] = 1
		if reason, ok := blockedAddr(netip.AddrFrom16(b)); ok {
			t.Errorf("%s with a non-zero suffix blocked as %q", base, reason)
		}
	}
	privSuffix := netip.MustParseAddr(nat64LocalUse("10.1.2.3", 0))
	pb := privSuffix.As16()
	pb[15] = 0xff
	if _, ok := blockedAddr(netip.AddrFrom16(pb)); !ok {
		t.Error("local-use NAT64 of a private address with a non-zero suffix was allowed")
	}

	blocked := []struct{ addr, why string }{
		{nat64WellKnown("127.0.0.1"), "loopback"},
		{nat64WellKnown("10.0.0.1"), "private"},
		{nat64WellKnown("169.254.169.254"), "link-local"},
		{nat64WellKnown("100.64.0.1"), "carrier-grade"},
		{nat64WellKnown("192.0.2.1"), "documentation"},
		{nat64WellKnown("0.0.0.0"), "unspecified"},
		{nat64WellKnown("240.0.0.1"), "reserved"},
		{nat64WellKnown("224.0.0.1"), "link-local"},
		{nat64WellKnown("239.255.255.255"), "multicast"},
		{nat64LocalUse("10.0.0.1", 0), "private"},
		{nat64LocalUse("127.0.0.1", 0), "loopback"},
		{nat64LocalUse("8.8.8.8", 1), "u octet"},
		{"2002:5db8:d822::1", "6to4"},
		{"2001::1", "teredo"},
	}
	for _, tc := range blocked {
		reason, ok := blockedAddr(netip.MustParseAddr(tc.addr))
		if !ok {
			t.Errorf("%s (%s) was allowed", tc.addr, tc.why)
			continue
		}
		if tc.why != "6to4" && tc.why != "teredo" && !strings.Contains(reason, tc.why) && tc.why != "u octet" {
			t.Errorf("%s reason %q does not name %s", tc.addr, reason, tc.why)
		}
		if tc.why == "u octet" && !strings.Contains(reason, "u octet") {
			t.Errorf("%s reason %q does not name the u octet", tc.addr, reason)
		}
	}

	// A name whose only answer is NAT64 of a public v4 is dialed.
	dns := &fakeDNS{answers: map[string][]string{"example.com": {public}}}
	srv := okServer(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	c, dialed := newTestClient(t, Config{AllowedHosts: []string{"example.com"}}, dns, srv)
	resp, err := get(t, c, "https://example.com/")
	if err != nil {
		t.Fatalf("public NAT64: %v", err)
	}
	resp.Body.Close()
	if len(*dialed) != 1 || !strings.Contains((*dialed)[0], public) {
		t.Fatalf("dialed %v, want %s", *dialed, public)
	}

	// Public v4 beside NAT64 of that same public is one host, not a rebind.
	dns = &fakeDNS{answers: map[string][]string{"example.com": {publicIP, same}}}
	c, dialed = newTestClient(t, Config{AllowedHosts: []string{"example.com"}}, dns, srv)
	resp, err = get(t, c, "https://example.com/")
	if err != nil {
		t.Fatalf("public v4 plus its NAT64: %v", err)
	}
	resp.Body.Close()
	if len(*dialed) == 0 {
		t.Fatal("public v4 plus its NAT64 dialed nothing")
	}

	// Any blocked answer, including NAT64 of one, refuses the whole name.
	for _, order := range [][]string{
		{publicIP, nat64WellKnown("127.0.0.1")},
		{nat64WellKnown("10.0.0.1"), publicIP},
		{public, nat64LocalUse("8.8.8.8", 1)},
		{nat64WellKnown("192.0.2.1")},
	} {
		dns = &fakeDNS{answers: map[string][]string{"example.com": order}}
		c, dialed = newTestClient(t, Config{AllowedHosts: []string{"example.com"}}, dns, nil)
		if _, err := get(t, c, "https://example.com/"); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%v: err = %v, want ErrBlockedAddress", order, err)
		}
		if len(*dialed) != 0 {
			t.Errorf("%v: dialed %v", order, *dialed)
		}
	}

	// A rebind between the check and the dial still refuses the new answer.
	dns = &fakeDNS{
		answers:     map[string][]string{"example.com": {public}},
		rebindAfter: 1,
		rebindTo:    []string{nat64WellKnown("169.254.169.254")},
	}
	c, dialed = newTestClient(t, Config{AllowedHosts: []string{"example.com"}}, dns, nil)
	if _, err := get(t, c, "https://example.com/"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("rebind to NAT64 of link-local: %v", err)
	}
	if len(*dialed) != 0 {
		t.Fatalf("rebind dialed %v", *dialed)
	}
}
