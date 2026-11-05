package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/peers"
)

func TestRemoteAuthenticatedProvenance(t *testing.T) {
	d := apitest.New(t)
	var behalf string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		behalf = r.Header.Get(peers.HeaderOnBehalf)
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer srv.Close()
	pc := peers.New(config.Config{Profile: config.ProfileProduction, Machine: "hostb", Peers: []config.Peer{{Name: "hosta", URL: srv.URL}}}, config.Paths{Home: d.Home}, nil)
	pc.SetTransport(srv.Client().Transport)
	pair := peers.NewPairToken()
	if _, err := pc.Tokens().Store("hosta", pair, "test"); err != nil {
		t.Fatal(err)
	}
	base := d.WithPeers(t, pc)
	req := map[string]any{"host": "hosta", "path": "/api/tasks/capabilities"}
	owner := client{t: t, base: base, token: d.OwnerToken}
	if got := owner.do("POST", "/api/remote", req, nil); got != 200 || behalf != "operator" {
		t.Fatal(got, behalf)
	}
	agent := client{t: t, base: base, token: d.AgentToken(t, "head")}
	if got := agent.do("POST", "/api/remote", req, nil); got != 200 || behalf != "agent:head" {
		t.Fatal(got, behalf)
	}
	companionToken, err := config.CompanionToken(config.Paths{Home: d.Home}, true)
	if err != nil {
		t.Fatal(err)
	}
	companion := client{t: t, base: base, token: companionToken}
	if got := companion.do("POST", "/api/remote", req, nil); got != 403 {
		t.Fatal(got)
	}
	// The main branch's peer boundary does not grant a peer another relay hop.
	if got := peerDo(t, base, "POST", "/api/remote", "hosta", pair, map[string]string{peers.HeaderOnBehalf: "agent:worker@hostc"}, req, nil); got != 403 || behalf != "agent:head" {
		t.Fatal(got, behalf)
	}
	if got := peerDo(t, base, "POST", "/api/remote", "hosta", "invalid", nil, req, nil); got != 401 {
		t.Fatal(got)
	}
	worker := client{t: t, base: base, token: d.AgentToken(t, mkWorker(t, d))}
	if got := worker.do("POST", "/api/remote", req, nil); got != 403 {
		t.Fatalf("worker can relay an otherwise allowed request: %d", got)
	}
	for _, path := range []string{"/api/config", "/api/gate/pin/record", "/api/approvals/0123456789ab", "/api/sessions/head/stop", "/api/remote", "/api/tasks/../config", "/api/tasks/%2e%2e/config", "/api/tasks%2f..%2fconfig", "/api/health?path=/api/config", "/api/health?=x"} {
		before := behalf
		if got := worker.do("POST", "/api/remote", map[string]any{"host": "hosta", "method": "POST", "path": path}, nil); got != 403 || behalf != before {
			t.Fatalf("worker relay %q: %d", path, got)
		}
	}
	for _, path := range []string{"/api/config", "/api/gate/pin/record", "/api/health?path=/api/config", "/api/sessions/head/screen?history=1&history=2", "/api/tasks/%2e%2e/config"} {
		if got := owner.do("POST", "/api/remote", map[string]any{"host": "hosta", "path": path}, nil); got != 403 {
			t.Fatalf("owner relay outside protocol %q: %d", path, got)
		}
	}
	if got := owner.do("POST", "/api/remote", map[string]any{"host": "hosta", "path": "/api/sessions/head/screen?history=100"}, nil); got != 200 {
		t.Fatal(got)
	}
}
