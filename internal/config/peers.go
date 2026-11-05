package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

// Peer is another machine of the fleet that runs the Python tmx daemon (config.json "peers",
// production profile only; DESIGN.md "Python fleet interop"). This daemon reads its agents
// (GET /api/sessions) and exchanges mail with it over its pair-token API. URL is that daemon's
// HTTPS address on the tailnet (tailscale serve): a pair token never travels in clear, and no
// raw LAN or 100.x address is accepted. The pair tokens live in the secrets folder (peers.json),
// never here; the fleet token is retired fleet-wide and not used.
type Peer struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// PeerURL validates a peer URL: https, a DNS name (ValidTrustedHost, and no IP literal), no
// credentials, path, query or fragment. It returns the URL without a trailing slash.
func PeerURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("peer url %q: %w", raw, err)
	}
	if u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" || !ValidTrustedHost(u.Host) {
		return "", fmt.Errorf("peer url %q: want https://<tailnet name>[:port] with nothing after it", raw)
	}
	if net.ParseIP(u.Hostname()) != nil {
		return "", fmt.Errorf("peer url %q: a tailnet name, never a raw IP address", raw)
	}
	return "https://" + NormalizeTrustedHost(u.Host), nil
}

// validatePeers checks "peers" and normalises their names and URLs. Peers are the production
// profile's: the trial stays local-only.
func (c *Config) validatePeers() error {
	if len(c.Peers) == 0 {
		return nil
	}
	if !c.Production() {
		return fmt.Errorf(`"peers" needs the %q profile: the trial stays local-only`, ProfileProduction)
	}
	seen := map[string]bool{}
	for i := range c.Peers {
		p := &c.Peers[i]
		p.Name = strings.ToLower(p.Name)
		if !ValidHostName(p.Name) || p.Name == strings.ToLower(c.Machine) || seen[p.Name] {
			return fmt.Errorf("peer name %q: a unique machine name other than this one (%s)", p.Name, c.Machine)
		}
		seen[p.Name] = true
		u, err := PeerURL(p.URL)
		if err != nil {
			return err
		}
		p.URL = u
	}
	return nil
}

// PeersFile holds the pair tokens (one per peer, both sides store the same one).
func (p Paths) PeersFile() string { return filepath.Join(p.SecretsDir(), "peers.json") }
