package p2p

import (
	"net"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// AssertLoopbackAddrs fails t if any addr (as returned by Status.Addrs / P2PStatus.Addrs)
// is not bound to loopback (127.0.0.1/::1). Binding 0.0.0.0/:: in a test triggers the
// Windows Firewall prompt, so every test host across packages must stay on loopback.
// Relay circuit addresses (/p2p-circuit) are skipped: they have no IP and bind no socket
// of their own. Exported so internal/api's tests (and others outside this package) can
// apply the same guard as internal/p2p's own tests without duplicating the logic.
func AssertLoopbackAddrs(t testingTB, addrs []string) {
	t.Helper()
	for _, s := range addrs {
		a, err := ma.NewMultiaddr(s)
		if err != nil {
			t.Fatalf("test address %q: %v", s, err)
		}
		if isRelayAddr(a) {
			continue
		}
		if manet.IsIPLoopback(a) {
			continue
		}
		host, err := a.ValueForProtocol(ma.P_IP4)
		if err != nil {
			host, err = a.ValueForProtocol(ma.P_IP6)
		}
		if err == nil {
			if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
				continue
			}
		}
		t.Fatalf("test host bound to non-loopback address %s (must be 127.0.0.1/::1)", s)
	}
}

// testingTB is the subset of testing.TB this file needs, so it does not import "testing"
// into the non-test build of this package.
type testingTB interface {
	Helper()
	Fatalf(format string, args ...any)
}
