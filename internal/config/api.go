package config

import (
	"fmt"
	"strconv"
	"strings"
)

// APIConfig opts into exact HTTPS hostnames served by a loopback reverse proxy.
// The empty allowlist preserves local-only access.
type APIConfig struct {
	TrustedHosts []string `json:"trusted_hosts,omitempty"`
}

// NormalizeTrustedHost lowercases a DNS authority and removes the HTTPS default port.
// Call ValidTrustedHost before trusting the result.
func NormalizeTrustedHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ":443")
}

// ValidTrustedHost accepts DNS names with an optional canonical numeric port.
// Patterns, URLs and malformed authorities are rejected.
func ValidTrustedHost(host string) bool {
	if name, port, found := strings.Cut(host, ":"); found {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return false
		}
		host = name
	}
	host = strings.ToLower(host)
	if len(host) == 0 || len(host) > 253 || !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func (a APIConfig) Validate() error {
	for _, host := range a.TrustedHosts {
		if !ValidTrustedHost(host) {
			return fmt.Errorf("api.trusted_hosts requires exact DNS names with optional ports (1-65535), without schemes or wildcards: %q", host)
		}
	}
	return nil
}
