package api_test

import (
	"net/http"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/guard"
)

// The daemon fixture belongs in CI; do not run on production Windows hosts.
func TestBrowserHostPolicyOwnerGate(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	agent := tc.New(d.URL, d.AgentToken(t, "head"))
	var policy struct {
		Allow  bool                     `json:"allow_browser_automation"`
		States map[string]guard.Outcome `json:"states"`
	}
	if s, _ := call(t, agent, "GET", "/api/guard/policy", nil, &policy); s != 0 || policy.Allow || policy.States[guard.RuleBrowser] != guard.Allow {
		t.Fatalf("default policy: %d %+v", s, policy)
	}
	next := guard.DefaultSettings()
	next.AllowBrowserAutomation = true
	if s, _ := call(t, agent, "PUT", "/api/safety/guard", next, nil); s != http.StatusForbidden {
		t.Fatalf("agent changed policy: %d", s)
	}
	on := map[string]bool{"enabled": true}
	s, conf := call(t, owner, "PUT", "/api/gate", on, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("gate enable: %d", s)
	}
	call(t, confirmed(t, owner, conf), "PUT", "/api/gate", on, nil)
	s, conf = call(t, owner, "PUT", "/api/safety/guard", next, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("browser permission without proof: %d", s)
	}
	if s, _ := call(t, confirmed(t, owner, conf), "PUT", "/api/safety/guard", next, nil); s != 0 {
		t.Fatalf("browser permission with proof: %d", s)
	}
	if s, _ := call(t, agent, "GET", "/api/guard/policy", nil, &policy); s != 0 || !policy.Allow {
		t.Fatalf("updated policy: %d %+v", s, policy)
	}
	if s, _ := call(t, tc.New(d.URL, "invalid-fixture"), "GET", "/api/guard/policy", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("unauthenticated policy: %d", s)
	}
}
