package api_test

import (
	"net/http"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Allow always outlives the hold, so setting and revoking it ask for a fresh proof even with the
// gate off; agents can do neither.
func TestGuardAllowAlwaysNeedsOwnerProof(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	agent := tc.New(d.URL, d.AgentToken(t, "head"))

	hold := map[string]any{"tool": "Bash", "command": "sudo true", "rule": guard.RuleElevation, "match": "sudo true"}
	var ap model.Approval
	if s, _ := call(t, agent, "POST", "/api/guard/holds", hold, &ap); s != 0 || ap.State != model.ApprovalPending {
		t.Fatalf("hold: %d %+v", s, ap)
	}
	always := map[string]string{"decision": "allow_always", "scope": "agent"}
	if s, _ := call(t, agent, "POST", "/api/approvals/"+ap.ID, always, nil); s != http.StatusForbidden {
		t.Fatalf("an agent answered Allow always: %d", s)
	}
	s, conf := call(t, owner, "POST", "/api/approvals/"+ap.ID, always, nil)
	if s != http.StatusPreconditionRequired || conf.Reason != "always allow “sudo true” for agents like head" {
		t.Fatalf("allow always without proof (gate off): %d %+v", s, conf)
	}
	var out model.Approval
	if s, _ := call(t, confirmed(t, owner, conf), "POST", "/api/approvals/"+ap.ID, always, &out); s != 0 || out.State != model.ApprovalAllowed {
		t.Fatalf("allow always with proof: %d %+v", s, out)
	}

	// The same command now runs without a card.
	var next model.Approval
	if s, _ := call(t, agent, "POST", "/api/guard/holds", hold, &next); s != 0 || next.State != model.ApprovalAllowed || next.Answer != "always" {
		t.Fatalf("standing allow: %d %+v", s, next)
	}

	var safety struct {
		Guard guard.Settings `json:"guard"`
	}
	call(t, owner, "GET", "/api/safety", nil, &safety)
	if len(safety.Guard.AlwaysAllow) != 1 {
		t.Fatalf("listed: %+v", safety.Guard.AlwaysAllow)
	}
	id := safety.Guard.AlwaysAllow[0].ID
	if s, _ := call(t, agent, "DELETE", "/api/safety/guard/always/"+id, nil, nil); s != http.StatusForbidden {
		t.Fatalf("an agent revoked: %d", s)
	}
	s, conf = call(t, owner, "DELETE", "/api/safety/guard/always/"+id, nil, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("revoke without proof: %d", s)
	}
	var g guard.Settings
	if s, _ := call(t, confirmed(t, owner, conf), "DELETE", "/api/safety/guard/always/"+id, nil, &g); s != 0 || len(g.AlwaysAllow) != 0 {
		t.Fatalf("revoke with proof: %d %+v", s, g)
	}
	if s, _ := call(t, agent, "POST", "/api/guard/holds", hold, &next); s != 0 || next.State != model.ApprovalPending {
		t.Fatalf("after revoke: %d %+v", s, next)
	}
}
