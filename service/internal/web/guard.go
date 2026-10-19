package web

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"syscall"
)

// errBlocked marks an address web_read refuses to connect to.
var errBlocked = errors.New("that address is on a private or internal network, so it will not be fetched")

// blocked lists ranges the standard library's classifiers do not cover.
var blocked = mustPrefixes(
	"0.0.0.0/8",       // "this network"
	"100.64.0.0/10",   // carrier-grade NAT, which includes Tailscale
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"240.0.0.0/4",     // reserved, includes the broadcast address
	"64:ff9b::/96",    // NAT64 (embeds an IPv4 address)
	"64:ff9b:1::/48",  // local-use NAT64
	"100::/64",        // discard-only
	"2001::/32",       // Teredo (embeds an IPv4 address)
	"2001:db8::/32",   // documentation
	"2002::/16",       // 6to4 (embeds an IPv4 address)
	"::/96",           // IPv4-compatible, deprecated
)

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, v := range s {
		out[i] = netip.MustParsePrefix(v)
	}
	return out
}

// checkIP returns an error wrapping errBlocked unless ip is a public unicast
// address. IPv4-mapped IPv6 addresses (::ffff:10.0.0.1) are judged as the IPv4
// address they carry.
func checkIP(ip netip.Addr) error {
	ip = ip.Unmap().WithZone("")
	switch {
	case !ip.IsValid():
		return fmt.Errorf("%w: invalid address", errBlocked)
	case ip.IsUnspecified():
		return fmt.Errorf("%w: %s is unspecified", errBlocked, ip)
	case ip.IsLoopback():
		return fmt.Errorf("%w: %s is loopback", errBlocked, ip)
	case ip.IsPrivate():
		return fmt.Errorf("%w: %s is private", errBlocked, ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast():
		return fmt.Errorf("%w: %s is link-local", errBlocked, ip)
	case ip.IsMulticast():
		return fmt.Errorf("%w: %s is multicast", errBlocked, ip)
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return fmt.Errorf("%w: %s is in %s", errBlocked, ip, p)
		}
	}
	return nil
}

// guardFunc decides whether a connection to addr may be made.
type guardFunc func(addr netip.AddrPort) error

// publicOnly is the production guard.
func publicOnly(addr netip.AddrPort) error { return checkIP(addr.Addr()) }

// control is a net.Dialer.Control hook. It runs after DNS resolution with the
// IP the socket is about to connect to, so a name that resolves differently
// between a check and the connection (DNS rebinding) cannot get through.
func (g guardFunc) control(_, address string, _ syscall.RawConn) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %w", errBlocked, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %w", errBlocked, err)
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return fmt.Errorf("%w: %w", errBlocked, err)
	}
	return g(netip.AddrPortFrom(ip, uint16(p)))
}
