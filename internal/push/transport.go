package push

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Match the existing Python transport's public push-service allowlist.
func endpointURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return nil, errors.New("invalid push endpoint")
	}
	h := strings.ToLower(u.Hostname())
	allowed := false
	for _, host := range []string{"fcm.googleapis.com", "android.googleapis.com", "updates.push.services.mozilla.com", "push.services.mozilla.com", "web.push.apple.com"} {
		allowed = allowed || h == host
	}
	allowed = allowed || strings.HasSuffix(h, ".notify.windows.com") || strings.HasSuffix(h, ".push.apple.com")
	if !allowed {
		return nil, errors.New("unknown push service")
	}
	return u, nil
}

// IsGlobalUnicast alone includes private/documentation space. Reject special-use
// ranges too, including IPv4-mapped addresses and IPv6 transition mechanisms.
func publicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	if a.Is6() && !netip.MustParsePrefix("2000::/3").Contains(a) {
		return false
	}
	for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "::/96", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		if netip.MustParsePrefix(raw).Contains(a) {
			return false
		}
	}
	return true
}

type lookupIPs func(context.Context, string) ([]net.IPAddr, error)

func publicAddresses(ctx context.Context, host string, lookup lookupIPs) ([]net.IPAddr, error) {
	addrs, err := lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		return nil, errors.New("push service DNS failed")
	}
	for _, addr := range addrs {
		if addr.Zone != "" || !publicIP(addr.IP) {
			return nil, errors.New("push service resolved to a non-public address")
		}
	}
	return addrs, nil
}

func newHTTPClient(lookup lookupIPs) *http.Client {
	// No environment proxy, redirects or second DNS resolution: validate and dial
	// the exact public IP while net/http preserves the hostname for TLS/SNI.
	tr := &http.Transport{TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" {
			return nil, errors.New("invalid push destination")
		}
		if _, err := endpointURL("https://" + host); err != nil {
			return nil, err
		}
		addrs, err := publicAddresses(ctx, host, lookup)
		if err != nil {
			return nil, err
		}
		for _, addr := range addrs {
			conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(addr.IP.String(), "443"))
			if err == nil {
				return conn, nil
			}
		}
		return nil, errors.New("push service connection failed")
	}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
