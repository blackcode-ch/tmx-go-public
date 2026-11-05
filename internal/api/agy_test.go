package api_test

import (
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"net/http"
	"testing"
)

func TestAgyFullAccessRequiresOwnerGate(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	agent := tc.New(d.URL, d.AgentToken(t, "head"))
	gateOn(t, owner)
	req := map[string]any{"name": "full-agy", "harness": "agy", "agy_full_access": true}
	if status, _ := call(t, agent, "POST", "/api/agents", req, nil); status != http.StatusForbidden {
		t.Fatalf("agent opt-in: %d", status)
	}
	status, proof := call(t, owner, "POST", "/api/agents", req, nil)
	if status != http.StatusPreconditionRequired {
		t.Fatalf("owner without proof: %d", status)
	}
	if _, err := d.Fleet.Agent("full-agy"); fleet.KindOf(err) != fleet.KindNotFound {
		t.Fatal("unconfirmed launch created agent")
	}
	// Even a confirmed owner cannot bypass the daemon's unsupported-harness guard.
	if status, _ = call(t, confirmed(t, owner, proof), "POST", "/api/agents", req, nil); status != http.StatusForbidden {
		t.Fatalf("opt-in bypassed guard: %d", status)
	}
}
