package api_test

import (
	"net/http"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// With the gate on, every allow of an elevation asks for the proof, also for the same call again
// (not Rememberable: P0, 4 Nov). An agent withdraws its own holds through the API.
func TestAllowAgainWithoutPINAndWithdraw(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	agent := tc.New(d.URL, d.AgentToken(t, "head"))
	on := map[string]bool{"enabled": true}
	_, conf := call(t, owner, "PUT", "/api/gate", on, nil)
	call(t, confirmed(t, owner, conf), "PUT", "/api/gate", on, nil)

	hold := map[string]any{"tool": "Bash", "command": "sudo true", "rule": guard.RuleElevation}
	allow := map[string]string{"decision": "allow"}
	var ap model.Approval
	call(t, agent, "POST", "/api/guard/holds", hold, &ap)
	s, conf := call(t, owner, "POST", "/api/approvals/"+ap.ID, allow, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("first allow without proof: %d", s)
	}
	if s, _ := call(t, confirmed(t, owner, conf), "POST", "/api/approvals/"+ap.ID, allow, nil); s != 0 {
		t.Fatalf("first allow with proof: %d", s)
	}
	var again model.Approval
	call(t, agent, "POST", "/api/guard/holds", hold, &again)
	// Elevation is not Rememberable (P0, 4 Nov): allowing the same call again still needs the proof.
	if s, _ := call(t, owner, "POST", "/api/approvals/"+again.ID, allow, nil); s != http.StatusPreconditionRequired {
		t.Fatalf("second allow of an elevation skipped the proof: %d", s)
	}
	if s, _ := call(t, agent, "POST", "/api/approvals/"+again.ID, allow, nil); s != http.StatusForbidden {
		t.Fatalf("an agent allowed: %d", s)
	}
	// A different command still asks.
	var diff model.Approval
	call(t, agent, "POST", "/api/guard/holds", map[string]any{"tool": "Bash", "command": "sudo id", "rule": guard.RuleElevation}, &diff)
	if s, _ := call(t, owner, "POST", "/api/approvals/"+diff.ID, allow, nil); s != http.StatusPreconditionRequired {
		t.Fatalf("a new command skipped the proof: %d", s)
	}

	// Withdraw: the agent's own pending holds; the owner's route can't.
	var list []model.Approval
	if s, _ := call(t, agent, "POST", "/api/guard/holds/withdraw", nil, &list); s != 0 || len(list) != 2 { // again + diff, both still pending
		t.Fatalf("withdraw all: %d %+v", s, list)
	}
	if s, _ := call(t, owner, "POST", "/api/guard/holds/"+diff.ID+"/withdraw", nil, nil); s != http.StatusForbidden {
		t.Fatalf("the owner used the agent's withdraw: %d", s)
	}
}
