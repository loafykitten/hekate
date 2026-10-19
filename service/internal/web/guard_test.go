package web

import (
	"errors"
	"net/netip"
	"testing"
)

func TestCheckIP(t *testing.T) {
	blockedAddrs := []string{
		"127.0.0.1", "127.255.255.254", "::1",
		"10.0.0.1", "172.16.5.4", "172.31.255.255", "192.168.1.1", "fd00::1", "fc00::1",
		"169.254.169.254", "169.254.0.1", "fe80::1", "fe80::1%eth0",
		"100.64.0.1", "100.100.100.100", "100.127.255.255", // CGNAT and Tailscale
		"224.0.0.1", "239.255.255.250", "ff02::1", "ff05::2",
		"0.0.0.0", "::", "0.1.2.3",
		"255.255.255.255", "240.0.0.1",
		"::ffff:127.0.0.1", "::ffff:10.1.2.3", "::ffff:169.254.169.254", "::ffff:100.64.0.1", // IPv4-mapped
		"64:ff9b::7f00:1", "2002:7f00:1::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "::127.0.0.1",
		"192.0.2.1", "198.18.0.1", "2001:db8::1",
	}
	for _, s := range blockedAddrs {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if err := checkIP(ip); !errors.Is(err, errBlocked) {
			t.Errorf("%s: err = %v, want blocked", s, err)
		}
	}
	allowed := []string{
		"93.184.216.34", "8.8.8.8", "1.1.1.1", "100.63.255.255", "100.128.0.1", "172.15.0.1", "172.32.0.1",
		"2606:2800:220:1:248:1893:25c8:1946", "2001:4860:4860::8888", "::ffff:8.8.8.8",
	}
	for _, s := range allowed {
		if err := checkIP(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s: err = %v, want allowed", s, err)
		}
	}
}

func TestControlChecksTheDialedAddress(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:80", "[::1]:443", "10.0.0.5:8080", "[::ffff:192.168.0.1]:80", "not-an-address", "1.2.3.4:notaport"} {
		if err := publicOnlyGuard().control("tcp", addr, nil); !errors.Is(err, errBlocked) {
			t.Errorf("%s: err = %v", addr, err)
		}
	}
	if err := publicOnlyGuard().control("tcp", "93.184.216.34:443", nil); err != nil {
		t.Errorf("public address refused: %v", err)
	}
}

func publicOnlyGuard() guardFunc { return publicOnly }
