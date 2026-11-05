package api_test

import (
	"net/http"
	"testing"

	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/guard"
)

type ruleRow struct {
	ID         string        `json:"id"`
	State      guard.Outcome `json:"state"`
	Switchable bool          `json:"switchable"`
	AlwaysOn   string        `json:"always_on"`
}

// The owner's rule switches (3 Nov 22:14), acceptance 1, 2, 9 at the API: the defaults are listed;
// a flip needs the owner and a fresh proof, even with the gate off; agents and peers can't;
// fixed carve-outs have no switch; the hook sees the flip.
func TestRuleSwitchAPI(t *testing.T) {
	s := newPeerSetup(t)
	d := s.d
	owner := tc.New(d.URL, d.OwnerToken)
	agent := tc.New(d.URL, d.AgentToken(t, "head"))

	var info struct {
		Rules []ruleRow `json:"rules"`
	}
	if st, _ := call(t, owner, "GET", "/api/safety", nil, &info); st != 0 {
		t.Fatalf("safety: %d", st)
	}
	rows := map[string]ruleRow{}
	for _, r := range info.Rules {
		rows[r.ID] = r
	}
	if r := rows[guard.RuleElevation]; r.State != guard.Hold || !r.Switchable {
		t.Fatalf("elevation row: %+v", r)
	}
	if r := rows[guard.RuleKillByName]; r.State != guard.Allow || r.AlwaysOn == "" {
		t.Fatalf("kill-by-name row: %+v", r)
	}
	if r := rows[guard.RuleKillTmx]; r.State != guard.Block || r.Switchable {
		t.Fatalf("fixed carve-out row: %+v", r)
	}

	path := "/api/safety/guard/rules/" + guard.RuleElevation
	allow := map[string]string{"state": "allow"}
	if st, _ := call(t, agent, "PUT", path, allow, nil); st != http.StatusForbidden {
		t.Fatalf("an agent flipped a rule: %d", st)
	}
	if got := peerDo(t, s.url, "PUT", path, "hosta", s.pair, nil, allow, nil); got < 400 {
		t.Fatalf("a peer flipped a rule: %d", got)
	}
	if st, _ := call(t, owner, "PUT", "/api/safety/guard/rules/"+guard.RuleKillTmx, allow, nil); st != http.StatusBadRequest {
		t.Fatalf("a fixed rule took a state: %d", st)
	}
	if st, _ := call(t, owner, "PUT", path, map[string]string{"state": "maybe"}, nil); st != http.StatusBadRequest {
		t.Fatalf("an invalid state: %d", st)
	}

	// The gate is off in this fixture, yet a flip still asks for a proof.
	st, conf := call(t, owner, "PUT", path, allow, nil)
	if st != http.StatusPreconditionRequired {
		t.Fatalf("flip without proof: %d", st)
	}
	var out struct {
		Guard guard.Settings `json:"guard"`
		Rules []ruleRow      `json:"rules"`
	}
	if st, _ := call(t, confirmed(t, owner, conf), "PUT", path, allow, &out); st != 0 {
		t.Fatalf("flip with proof: %d", st)
	}
	last := out.Guard.RuleLog[len(out.Guard.RuleLog)-1]
	if last.Rule != guard.RuleElevation || last.Old != guard.Hold || last.New != guard.Allow || last.By != "owner" {
		t.Fatalf("log: %+v", last)
	}

	// The hook's policy carries the flip.
	var policy struct {
		States map[string]guard.Outcome `json:"states"`
	}
	if st, _ := call(t, agent, "GET", "/api/guard/policy", nil, &policy); st != 0 || policy.States[guard.RuleElevation] != guard.Allow {
		t.Fatalf("policy after the flip: %d %v", st, policy.States)
	}
	// A hold for the now-allowed rule is refused.
	hold := map[string]any{"tool": "Bash", "command": "sudo true", "rule": guard.RuleElevation}
	if st, _ := call(t, agent, "POST", "/api/guard/holds", hold, nil); st != http.StatusBadRequest {
		t.Fatalf("hold for an allowed rule: %d", st)
	}
}
