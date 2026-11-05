package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// call does one request and returns the HTTP status (0 = success) and, for a 428, the
// confirmation the daemon is waiting on.
func call(t *testing.T, c *tc.Client, method, path string, body, out any) (int, gate.Confirmation) {
	t.Helper()
	err := c.Do(context.Background(), method, path, body, out)
	if err == nil {
		return 0, gate.Confirmation{}
	}
	var ae *tc.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	var need struct {
		Confirmation gate.Confirmation `json:"confirmation"`
	}
	_ = json.Unmarshal(ae.Body, &need)
	return ae.Status, need.Confirmation
}

func confirmed(t *testing.T, owner *tc.Client, conf gate.Confirmation) *tc.Client {
	t.Helper()
	if conf.ID == "" || conf.Reason == "" {
		t.Fatalf("428 without a confirmation: %+v", conf)
	}
	var c struct {
		gate.Confirmation
		Grace string `json:"grace_credential"`
	}
	if err := owner.Get(context.Background(), "/api/gate/confirmations/"+conf.ID+"?wait=3000", &c); err != nil {
		t.Fatal(err)
	}
	if c.State != gate.StateConfirmed {
		t.Fatalf("confirmation: %+v", c)
	}
	retry := *owner
	if owner.Header == nil {
		owner.Header = http.Header{}
	}
	if c.Grace != "" {
		owner.Header.Set(api.GraceHeader, c.Grace)
	}
	retry.Header = owner.Header.Clone()
	retry.Header.Set(api.ConfirmHeader, c.ID)
	return &retry
}

func TestOwnerGateOnGuardedActions(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	agent := tc.New(d.URL, d.AgentToken(t, "head"))

	var st gate.Status
	if s, _ := call(t, owner, "GET", "/api/gate", nil, &st); s != 0 || st.Enabled || !st.Available {
		t.Fatalf("gate status: %d %+v", s, st)
	}
	if s, _ := call(t, agent, "GET", "/api/gate", nil, nil); s != http.StatusForbidden {
		t.Fatalf("agent read the gate: %d", s)
	}
	if s, _ := call(t, agent, "PUT", "/api/gate", map[string]bool{"enabled": false}, nil); s != http.StatusForbidden {
		t.Fatalf("agent changed the gate: %d", s)
	}

	// Turning it on needs a proof although it is off.
	on := map[string]bool{"enabled": true}
	s, conf := call(t, owner, "PUT", "/api/gate", on, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("enable without proof: %d", s)
	}
	if s, _ := call(t, confirmed(t, owner, conf), "PUT", "/api/gate", on, &st); s != 0 || !st.Enabled {
		t.Fatalf("enable with proof: %d %+v", s, st)
	}

	// A guard hold: allowing needs a proof, bound to exactly that request; denying does not.
	hold := map[string]any{"tool": "Bash", "command": "sudo true", "rule": guard.RuleElevation, "match": "sudo true"}
	var ap model.Approval
	if s, _ := call(t, owner, "POST", "/api/guard/holds", hold, nil); s != http.StatusForbidden {
		t.Fatalf("owner created a hold: %d", s)
	}
	if s, _ := call(t, agent, "POST", "/api/guard/holds", hold, &ap); s != 0 || ap.Kind != model.ApprovalGuard {
		t.Fatalf("hold: %d %+v", s, ap)
	}
	allow := map[string]string{"decision": "allow"}
	s, conf = call(t, owner, "POST", "/api/approvals/"+ap.ID, allow, nil)
	if s != http.StatusPreconditionRequired || conf.Reason != "allow “sudo true” for head" {
		t.Fatalf("allow without proof: %d %+v", s, conf)
	}
	proof := confirmed(t, owner, conf)
	if s, _ := call(t, proof, "POST", "/api/approvals/"+ap.ID, map[string]string{"decision": "allow", "message": "other body"}, nil); s != http.StatusPreconditionRequired {
		t.Fatalf("a proof for one body let another through: %d", s)
	}
	var out model.Approval
	if s, _ := call(t, proof, "POST", "/api/approvals/"+ap.ID, allow, &out); s != 0 || out.State != model.ApprovalAllowed {
		t.Fatalf("allow with proof: %d %+v", s, out)
	}
	if got := d.GateAuth.Prompts(); len(got) == 0 || got[0] != "turn on the owner gate" {
		t.Fatalf("prompts: %q", got)
	}
	var ap2 model.Approval
	call(t, agent, "POST", "/api/guard/holds", hold, &ap2)
	if s, _ := call(t, owner, "POST", "/api/approvals/"+ap2.ID, map[string]string{"decision": "deny"}, &out); s != 0 || out.State != model.ApprovalDenied {
		t.Fatalf("deny needs no proof: %d %+v", s, out)
	}
	// An agent waits on its hold and sees the decision.
	var seen model.Approval
	if s, _ := call(t, agent, "GET", "/api/guard/holds/"+ap2.ID+"?wait=10", nil, &seen); s != 0 || seen.State != model.ApprovalDenied {
		t.Fatalf("agent wait: %d %+v", s, seen)
	}

	// Bypass permissions needs a proof; other policy changes do not.
	var pol model.Policy
	call(t, owner, "GET", "/api/policy", nil, &pol)
	lp := pol.Levels[model.LevelWorker]
	lp.PermissionMode = "acceptEdits"
	pol.Levels[model.LevelWorker] = lp
	if s, _ := call(t, owner, "PUT", "/api/policy", pol, nil); s != 0 {
		t.Fatalf("plain policy change: %d", s)
	}
	lp.PermissionMode = "bypassPermissions"
	pol.Levels[model.LevelWorker] = lp
	s, conf = call(t, owner, "PUT", "/api/policy", pol, nil)
	if s != http.StatusPreconditionRequired || conf.Reason != "let worker agents bypass permissions" {
		t.Fatalf("bypass without proof: %d %+v", s, conf)
	}
	if s, _ := call(t, confirmed(t, owner, conf), "PUT", "/api/policy", pol, nil); s != 0 {
		t.Fatalf("bypass with proof: %d", s)
	}

	// Switching the guard off needs a proof; on again does not.
	s, conf = call(t, owner, "PUT", "/api/safety/guard", guard.Settings{Enabled: false}, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("guard off without proof: %d", s)
	}
	if s, _ := call(t, confirmed(t, owner, conf), "PUT", "/api/safety/guard", guard.Settings{Enabled: false}, nil); s != 0 {
		t.Fatalf("guard off with proof: %d", s)
	}
	if s, _ := call(t, owner, "PUT", "/api/safety/guard", guard.Settings{Enabled: true}, nil); s != 0 {
		t.Fatalf("guard on: %d", s)
	}
	var info api.SafetyInfo
	if s, _ := call(t, owner, "GET", "/api/safety", nil, &info); s != 0 || !info.Guard.Enabled || len(info.Rules) < 10 || !info.Gate.Enabled {
		t.Fatalf("safety: %d %+v", s, info)
	}

	// Deleting a project needs a proof.
	var p model.Project
	call(t, owner, "POST", "/api/projects", map[string]string{"name": "Doomed"}, &p)
	s, conf = call(t, owner, "DELETE", "/api/projects/"+p.ID, nil, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("delete without proof: %d", s)
	}
	var c0 gate.Confirmation
	owner.Get(context.Background(), "/api/gate/confirmations/"+conf.ID+"?wait=3000", &c0)
	if s, _ := call(t, owner, "DELETE", "/api/projects/"+p.ID+"?"+api.ConfirmParam+"="+c0.ID, nil, nil); s != 0 {
		t.Fatalf("delete with proof as a query parameter: %d", s)
	}

	// A refused prompt lets nothing through; the gate is turned off with a proof.
	d.GateAuth.SetAnswer(func(context.Context, string) error { return errors.New("Authentication failed") })
	s, conf = call(t, owner, "PUT", "/api/gate", map[string]bool{"enabled": false}, nil)
	var c gate.Confirmation
	owner.Get(context.Background(), "/api/gate/confirmations/"+conf.ID+"?wait=3000", &c)
	if s != http.StatusPreconditionRequired || c.State != gate.StateFailed {
		t.Fatalf("refused: %d %+v", s, c)
	}
	retry := *owner
	retry.Header = http.Header{api.ConfirmHeader: {c.ID}}
	if s, _ := call(t, &retry, "PUT", "/api/gate", map[string]bool{"enabled": false}, nil); s != http.StatusPreconditionRequired {
		t.Fatalf("a failed proof was accepted: %d", s)
	}
	waitIdle(t, d.Gate, owner)
	d.GateAuth.SetAnswer(nil)
	s, conf = call(t, owner, "PUT", "/api/gate", map[string]bool{"enabled": false}, nil)
	if s, _ := call(t, confirmed(t, owner, conf), "PUT", "/api/gate", map[string]bool{"enabled": false}, &st); s != 0 || st.Enabled {
		t.Fatalf("disable: %d %+v", s, st)
	}
	// Off again: nothing is gated.
	call(t, owner, "POST", "/api/projects", map[string]string{"name": "Free"}, &p)
	if s, _ := call(t, owner, "DELETE", "/api/projects/"+p.ID, nil, nil); s != 0 {
		t.Fatalf("delete with the gate off: %d", s)
	}
}

func waitIdle(t *testing.T, g *gate.Gate, _ *tc.Client) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := g.Require(gate.Action{Reason: "probe", Key: "probe"}, ""); !errors.Is(err, gate.ErrBusy) {
			var need *gate.NeedConfirmation
			if errors.As(err, &need) {
				g.Cancel(need.Confirmation.ID)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a prompt stayed open")
}

func TestGateUnavailable(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	d.GateAuth.Unavail = errors.New("no window server")
	if s, _ := call(t, owner, "PUT", "/api/gate", map[string]bool{"enabled": true}, nil); s != http.StatusServiceUnavailable {
		t.Fatalf("enable without an authenticator: %d", s)
	}
	var st gate.Status
	call(t, owner, "GET", "/api/gate", nil, &st)
	if st.Enabled || st.Available || st.Unavailable == "" {
		t.Fatalf("status: %+v", st)
	}
}

func TestCORSAllowsConfirmationHeader(t *testing.T) {
	d := apitest.New(t)
	req, _ := http.NewRequest(http.MethodOptions, d.URL+"/api/gate", nil)
	req.Header.Set("Origin", "wails://wails")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if h := resp.Header.Get("Access-Control-Allow-Headers"); h == "" || !strings.Contains(h, api.ConfirmHeader) {
		t.Fatalf("allow headers: %q", h)
	}
}

// Profiles grant bypassPermissions like the policy does, so the same gate applies (merge of the
// profiles and safety features, 22 Oct). Its own daemon: the gate allows only a few prompts a minute.
func TestOwnerGateOnProfiles(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	on := map[string]bool{"enabled": true}
	s, conf := call(t, owner, "PUT", "/api/gate", on, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("enable without proof: %d", s)
	}
	if s, _ := call(t, confirmed(t, owner, conf), "PUT", "/api/gate", on, nil); s != 0 {
		t.Fatalf("enable with proof: %d", s)
	}

	var rev model.Profile
	call(t, owner, "GET", "/api/profiles/reviewer", nil, &rev)
	rev.Description = "plain edit"
	if s, _ := call(t, owner, "PUT", "/api/profiles/reviewer", rev, nil); s != 0 {
		t.Fatalf("plain profile edit: %d", s)
	}
	rev.PermissionMode = "bypassPermissions"
	s, conf = call(t, owner, "PUT", "/api/profiles/reviewer", rev, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("profile bypass without proof: %d", s)
	}
	if s, _ := call(t, confirmed(t, owner, conf), "PUT", "/api/profiles/reviewer", rev, nil); s != 0 {
		t.Fatalf("profile bypass with proof: %d", s)
	}
	rev.Description = "edited, still bypass"
	if s, _ := call(t, owner, "PUT", "/api/profiles/reviewer", rev, nil); s != 0 {
		t.Fatalf("a profile already bypassing needs no new proof: %d", s)
	}
	waitIdle(t, d.Gate, owner)
	np := model.Profile{Slug: "yolo", Name: "Yolo", Level: model.LevelWorker, Model: "claude-sonnet-5-5",
		AllowedModels: []string{"claude-sonnet-5-5"}, PermissionMode: "bypassPermissions"}
	s, conf = call(t, owner, "POST", "/api/profiles", np, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("new bypass profile without proof: %d", s)
	}
	if s, _ := call(t, confirmed(t, owner, conf), "POST", "/api/profiles", np, nil); s != 0 {
		t.Fatalf("new bypass profile with proof: %d", s)
	}
}
