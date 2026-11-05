package api_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
)

func hostPort(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	_, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func registerHost(t *testing.T, home string, h config.Host, token string) {
	t.Helper()
	paths := config.Paths{Home: home}
	c, err := config.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	c.Hosts = append(c.Hosts, h)
	if err := config.Save(paths, c); err != nil {
		t.Fatal(err)
	}
	if token != "" {
		if err := config.WriteSecret(paths.HostTokenFile(h.Name), token); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFleetHosts merges a real second daemon's snapshot/resources/timeline into the first
// daemon's Overview, over loopback only (no p2p, no new network exposure: WP4).
func TestFleetHosts(t *testing.T) {
	remote := apitest.New(t)
	local := apitest.New(t)

	registerHost(t, local.Home, config.Host{Name: "remote", Port: hostPort(t, remote.URL)}, remote.OwnerToken)

	owner := client{t: t, base: local.URL, token: local.OwnerToken}
	var resp struct {
		Hosts []api.HostSummary `json:"hosts"`
	}
	if s := owner.do("GET", "/api/fleet/hosts", nil, &resp); s != 200 {
		t.Fatalf("status %d", s)
	}
	if len(resp.Hosts) != 1 {
		t.Fatalf("hosts: %+v", resp.Hosts)
	}
	h := resp.Hosts[0]
	if !h.Reachable || h.Error != "" {
		t.Fatalf("not reachable: %+v", h)
	}
	if h.Machine != "testbox" || h.HeadName != "head" || h.HeadState == "" {
		t.Fatalf("summary: %+v", h)
	}

	// An agent (not the owner) may not read another daemon's aggregated data.
	agent := client{t: t, base: local.URL, token: local.AgentToken(t, "head")}
	if s := agent.do("GET", "/api/fleet/hosts", nil, nil); s != 403 {
		t.Fatalf("agent read: %d", s)
	}
}

// TestFleetHostsUnreachableAndMissingToken covers the two failure paths: a host configured
// without its token file, and one whose port nothing answers on.
func TestFleetHostsUnreachableAndMissingToken(t *testing.T) {
	local := apitest.New(t)
	l, err := net.Listen("tcp", "127.0.0.1:0") // a port nobody serves once closed
	if err != nil {
		t.Fatal(err)
	}
	closedPort := l.Addr().(*net.TCPAddr).Port
	l.Close()

	registerHost(t, local.Home, config.Host{Name: "no-token", Port: 59998}, "")
	registerHost(t, local.Home, config.Host{Name: "closed", Port: closedPort}, "some-token")

	owner := client{t: t, base: local.URL, token: local.OwnerToken}
	var resp struct {
		Hosts []api.HostSummary `json:"hosts"`
	}
	if s := owner.do("GET", "/api/fleet/hosts", nil, &resp); s != 200 {
		t.Fatalf("status %d", s)
	}
	if len(resp.Hosts) != 2 {
		t.Fatalf("hosts: %+v", resp.Hosts)
	}
	for _, h := range resp.Hosts {
		if h.Reachable || h.Error == "" {
			t.Fatalf("expected an unreachable host with an error: %+v", h)
		}
	}
}

// TestFleetHostsRejectsDisallowedPortsAndRedirects covers the gaps the security review found:
// issue #83's reserved ports and this daemon's own port are refused before any request is
// made, and a host that answers with a redirect is never followed - a 3xx never counts as
// success, so the bearer token is never resent wherever that redirect points.
func TestFleetHostsRejectsDisallowedPortsAndRedirects(t *testing.T) {
	local := apitest.New(t)

	registerHost(t, local.Home, config.Host{Name: "python", Port: 8777}, "tok")
	registerHost(t, local.Home, config.Host{Name: "secondary", Port: 43911}, "tok")
	registerHost(t, local.Home, config.Host{Name: "p2p", Port: 4001}, "tok")
	registerHost(t, local.Home, config.Host{Name: "self", Port: hostPort(t, local.URL)}, "tok")

	var sawAuth string
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Location", "http://127.0.0.1:1/leak")
		w.WriteHeader(http.StatusFound)
	}))
	defer redirecting.Close()
	registerHost(t, local.Home, config.Host{Name: "redirector", Port: hostPort(t, redirecting.URL)}, "secret-token")

	owner := client{t: t, base: local.URL, token: local.OwnerToken}
	var resp struct {
		Hosts []api.HostSummary `json:"hosts"`
	}
	if s := owner.do("GET", "/api/fleet/hosts", nil, &resp); s != 200 {
		t.Fatalf("status %d", s)
	}
	byName := map[string]api.HostSummary{}
	for _, h := range resp.Hosts {
		byName[h.Name] = h
	}
	if h := byName["python"]; h.Reachable || h.Error == "" {
		t.Fatalf("the legacy Python port must be refused: %+v", h)
	}
	if h := byName["secondary"]; h.Reachable || h.Error == "" {
		t.Fatalf("issue #83's reserved secondary port must be refused: %+v", h)
	}
	if h := byName["p2p"]; h.Reachable || h.Error == "" {
		t.Fatalf("issue #83's reserved p2p port must be refused: %+v", h)
	}
	if h := byName["self"]; h.Reachable || h.Error == "" {
		t.Fatalf("this daemon's own port must be refused: %+v", h)
	}
	if h := byName["redirector"]; h.Reachable {
		t.Fatalf("a redirect must never count as success: %+v", h)
	}
	if sawAuth != "Bearer secret-token" {
		t.Fatalf("the configured host's own token should have reached it: %q", sawAuth)
	}
}

// TestFleetHostsIncludesPeers: ONE FLEET (3 Nov) - a real other machine (Python fleet interop,
// over its own pair token, never this daemon's bearer token) shows up in the same Overview list
// as a loopback host, kind "peer", with counts derived from GET /api/sessions's session states.
func TestFleetHostsIncludesPeers(t *testing.T) {
	ps := newPeerSetup(t)
	ps.pc.PollAll(context.Background())

	owner := client{t: t, base: ps.url, token: ps.d.OwnerToken}
	var resp struct {
		Hosts []api.HostSummary `json:"hosts"`
	}
	if s := owner.do("GET", "/api/fleet/hosts", nil, &resp); s != 200 {
		t.Fatalf("status %d", s)
	}
	var peer api.HostSummary
	found := false
	for _, h := range resp.Hosts {
		if h.Kind == "peer" && h.Name == "hosta" {
			peer, found = h, true
		}
	}
	if !found {
		t.Fatalf("no hosta peer row: %+v", resp.Hosts)
	}
	if peer.Name != "hosta" || !peer.Reachable || peer.Error != "" || peer.Agents != 1 {
		t.Fatalf("peer row: %+v", peer)
	}
	if peer.Port != 0 || peer.CPU != 0 || peer.MemFreeMB != 0 {
		t.Fatalf("a peer row must never claim loopback-only fields: %+v", peer)
	}

	// An agent (not the owner) may not read another machine's data through this either.
	agent := client{t: t, base: ps.url, token: ps.d.AgentToken(t, "head")}
	if s := agent.do("GET", "/api/fleet/hosts", nil, nil); s != 403 {
		t.Fatalf("agent read: %d", s)
	}
}

// TestFleetHostsPeerUnreachable: a configured peer this daemon has never successfully polled
// (OK stays false, the zero-value Statuses() entry made when the peer was configured) is still
// listed, as an unreachable row - the Overview must not hide a real machine just because it
// has not answered yet.
func TestFleetHostsPeerUnreachable(t *testing.T) {
	ps := newPeerSetup(t) // never polled: Statuses() still holds each peer's zero-value seed

	owner := client{t: t, base: ps.url, token: ps.d.OwnerToken}
	var resp struct {
		Hosts []api.HostSummary `json:"hosts"`
	}
	if s := owner.do("GET", "/api/fleet/hosts", nil, &resp); s != 200 {
		t.Fatalf("status %d", s)
	}
	for _, h := range resp.Hosts {
		if h.Kind != "peer" {
			t.Fatalf("unexpected row kind: %+v", h)
		}
		if h.Reachable {
			t.Fatalf("a never-polled peer must not claim reachable: %+v", h)
		}
	}
	if len(resp.Hosts) == 0 {
		t.Fatal("configured peers must still be listed before the first poll")
	}
}
