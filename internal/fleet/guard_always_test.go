package fleet

import (
	"encoding/json"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// The owner, 3 Nov: "I need to be able to allow always". A standing allow covers one rule + the
// exact command + tool, for the agent's profile or any agent; only the owner sets or revokes it.
func TestGuardAllowAlways(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	me := callerFor(t, f, e.chat, head.ID)
	e.chat.Last(head.ID).State(model.StateWorking)
	waitState(t, f, "head", model.StateWorking)
	if _, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	lead := mustAgent(t, f, "alpha-lead")
	other := callerFor(t, f, e.chat, lead.ID)
	if alwaysKey(&head) == alwaysKey(&lead) {
		t.Fatalf("head and lead share the key %q", alwaysKey(&head))
	}

	req := HoldRequest{Tool: "Bash", Command: "sudo true", Rule: guard.RuleElevation, Match: "sudo true", Cwd: "/work/a"}
	hold := func(c Caller, r HoldRequest) model.Approval {
		t.Helper()
		ap, err := f.Hold(c, r)
		if err != nil {
			t.Fatal(err)
		}
		if ap.State == model.ApprovalPending {
			if _, err := f.CancelHold(c, ap.ID); err != nil {
				t.Fatal(err)
			}
		}
		return ap
	}

	ap, err := f.Hold(me, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: DecisionAllowAlways}); KindOf(err) != KindInvalid {
		t.Fatal("allow_always without a scope accepted")
	}
	if a, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: DecisionAllowAlways, Scope: "agent"}); err != nil || a.State != model.ApprovalAllowed {
		t.Fatalf("allow always: %+v %v", a, err)
	}
	list := f.GuardSettings().AlwaysAllow
	if len(list) != 1 || list[0].Rule != guard.RuleElevation || list[0].Command != "sudo true" ||
		list[0].Profile != alwaysKey(&head) || list[0].From != "head" || list[0].RuleName == "" {
		t.Fatalf("remembered: %+v", list)
	}

	// The same call by the same kind of agent runs without a card; anything else still asks.
	if a := hold(me, req); a.State != model.ApprovalAllowed || a.Answer != AnswerAlways {
		t.Fatalf("standing allow not applied: %+v", a)
	}
	for _, r := range []HoldRequest{
		{Tool: "Bash", Command: "sudo  true", Rule: guard.RuleElevation},
		{Tool: "Bash", Command: "sudo true; sudo rm x", Rule: guard.RuleElevation},
		{Tool: "PowerShell", Command: "sudo true", Rule: guard.RuleElevation, Cwd: "/work/a"},
		{Tool: "Bash", Command: "sudo true", Rule: guard.RuleElevation, Cwd: "/work/b"}, // another folder
	} {
		if a := hold(me, r); a.State != model.ApprovalPending {
			t.Fatalf("%+v let through: %+v", r, a)
		}
	}
	if a := hold(other, req); a.State != model.ApprovalPending {
		t.Fatal("an agent-scoped allow covered another kind of agent")
	}

	// A command whose meaning is only known at run time is never remembered.
	for _, cmd := range []string{"sudo cat $target", "sudo ls ${T}", "sudo cat `pwd`/x", "sudo ls *.log", "sudo type %TEMP%\\x", "sudo $(which x)"} {
		ap, err := f.Hold(me, HoldRequest{Tool: "Bash", Command: cmd, Rule: guard.RuleElevation, Cwd: "/work/a"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: DecisionAllowAlways, Scope: "any"}); KindOf(err) != KindInvalid {
			t.Fatalf("%q was remembered: %v", cmd, err)
		}
		f.CancelHold(me, ap.ID)
	}
	if n := len(f.GuardSettings().AlwaysAllow); n != 1 {
		t.Fatalf("run-time commands added entries: %d", n)
	}

	// A peer reaching the hold route is refused, not a nil dereference.
	if _, err := f.Hold(Caller{Peer: "hostb"}, req); KindOf(err) != KindForbidden {
		t.Fatalf("peer hold: %v", err)
	}
	if err := f.ReportBlock(Caller{Peer: "hostb"}, HoldRequest{Tool: "Bash", Command: "x", Rule: guard.RuleKillTmx}); KindOf(err) != KindForbidden {
		t.Fatalf("peer block report: %v", err)
	}

	// PUT of the settings neither adds nor drops standing allows.
	inject := guard.Settings{Enabled: true, AlwaysAllow: []guard.AlwaysAllow{{ID: "x", Rule: guard.RuleElevation, Tool: "Bash", Command: "sudo evil"}}}
	if s, err := f.SetGuardSettings(OwnerCaller, inject); err != nil || len(s.AlwaysAllow) != 1 || s.AlwaysAllow[0].ID == "x" || s.AlwaysAllow[0].Cwd != "/work/a" {
		t.Fatalf("settings PUT changed the allows: %+v %v", s.AlwaysAllow, err)
	}

	// Only the owner revokes; then it asks again.
	id := f.GuardSettings().AlwaysAllow[0].ID
	if _, err := f.RevokeAlways(me, id); KindOf(err) != KindForbidden {
		t.Fatal("an agent revoked an allow")
	}
	if _, err := f.RevokeAlways(OwnerCaller, "nope"); KindOf(err) != KindNotFound {
		t.Fatal("revoked an unknown allow")
	}
	if s, err := f.RevokeAlways(OwnerCaller, id); err != nil || len(s.AlwaysAllow) != 0 {
		t.Fatalf("revoke: %+v %v", s.AlwaysAllow, err)
	}
	if a := hold(me, req); a.State != model.ApprovalPending {
		t.Fatal("still allowed after revoke")
	}

	// "Any agent" covers the lead too.
	ap, _ = f.Hold(me, req)
	if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: DecisionAllowAlways, Scope: "any"}); err != nil {
		t.Fatal(err)
	}
	if a := hold(other, req); a.State != model.ApprovalAllowed {
		t.Fatalf("any-agent allow not applied to the lead: %+v", a)
	}
	var info model.GuardHold
	if a := hold(other, req); json.Unmarshal(a.Input, &info) != nil || info.Command != "sudo true" {
		t.Fatalf("auto-allowed approval input: %s", a.Input)
	}
}
