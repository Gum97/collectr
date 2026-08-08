package domain

import (
	"net"
	"testing"
)

// TestIsPrivateIPBlocksTheUnspecifiedAddress pins the bypass an audit found:
// "::" is the IPv6 unspecified address, no CIDR in the list covered it, and
// connecting to it lands on loopback. Its IPv4 twin 0.0.0.0 was caught only
// because 0.0.0.0/8 happened to be listed -- a coincidence, not a defence.
func TestIsPrivateIPBlocksTheUnspecifiedAddress(t *testing.T) {
	t.Parallel()

	blocked := []string{
		// The reported bypass, in both spellings.
		"::", "0:0:0:0:0:0:0:0",
		"0.0.0.0",
		"127.0.0.1", "::1", "::ffff:127.0.0.1",
		"169.254.169.254", "::ffff:169.254.169.254",
		"10.0.0.5", "172.16.0.1", "192.168.1.1", "100.64.0.1",
		"fe80::1", "fc00::1", "ff02::1",
		// IPv4 addresses wearing a translation format. Each reaches the address
		// inside it wherever a route exists.
		"2002:7f00:1::",    // 6to4 carrying 127.0.0.1
		"2002:a9fe:a9fe::", // 6to4 carrying 169.254.169.254
		"64:ff9b::7f00:1",  // NAT64 carrying 127.0.0.1
		"::127.0.0.1",      // deprecated IPv4-compatible form
	}
	for _, s := range blocked {
		if !IsPrivateIP(net.ParseIP(s)) {
			t.Errorf("IsPrivateIP(%q) = false, want blocked", s)
		}
	}

	// Over-blocking is its own failure: a webhook that cannot reach the public
	// internet is a feature that does not work.
	allowed := []string{
		"93.184.216.34",
		"2606:2800:220:1:248:1893:25c8:1946",
		"2002:5db8:d822::", // 6to4 carrying a public address
		"64:ff9b::808:808", // NAT64 carrying 8.8.8.8
	}
	for _, s := range allowed {
		if IsPrivateIP(net.ParseIP(s)) {
			t.Errorf("IsPrivateIP(%q) = true, want allowed", s)
		}
	}
}
