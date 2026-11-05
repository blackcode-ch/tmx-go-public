package fleet

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// The owner, 3 Nov 22:30 ("too safe"): an agent takes back its own pending hold; something allowed
// once is remembered so allowing it again needs no PIN.
func TestWithdrawHoldAndAllowedBefore(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	me := callerFor(t, f, e.chat, head.ID)
	e.chat.Last(head.ID).State(model.StateWorking)
	waitState(t, f, "head", model.StateWorking)
	req := HoldRequest{Tool: "Bash", Command: "sudo true", Rule: guard.RuleElevation, Cwd: "/w"}

	ap, err := f.Hold(me, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WithdrawHold(OwnerCaller, ap.ID); KindOf(err) != KindForbidden {
		t.Fatal("the owner 'withdrew' a hold")
	}
	if _, err := f.WithdrawHold(AgentCaller("someone"), ap.ID); KindOf(err) != KindForbidden {
		t.Fatal("another agent withdrew the hold")
	}
	if got := f.PendingHolds(me); len(got) != 1 || got[0].ID != ap.ID {
		t.Fatalf("pending holds: %+v", got)
	}
	w, err := f.WithdrawHold(me, ap.ID)
	if err != nil || w.State != model.ApprovalExpired || w.Answer != AnswerWithdrawn {
		t.Fatalf("withdraw: %+v %v", w, err)
	}
	if _, err := f.WithdrawHold(me, ap.ID); KindOf(err) != KindConflict {
		t.Fatal("withdrew twice")
	}
	if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: "allow"}); err == nil {
		t.Fatal("a withdrawn hold was allowed")
	}
	tl, _ := f.Timeline("head", 0, 100)
	if !strings.Contains(tl[len(tl)-1].Text, "Withdrawn by the agent") {
		t.Fatalf("timeline: %q", tl[len(tl)-1].Text)
	}
	waitState(t, f, "head", model.StateWorking)

	// Allowed once → remembered; another command is not.
	ap2, _ := f.Hold(me, req)
	if f.AllowedBefore(ap2) {
		t.Fatal("remembered before any allow")
	}
	if _, err := f.Decide(OwnerCaller, ap2.ID, DecideRequest{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	// Elevation is not Rememberable (P0, 4 Nov): after an Allow it still asks, with the PIN.
	ap3, _ := f.Hold(me, req)
	if f.AllowedBefore(ap3) || ap3.State != model.ApprovalPending {
		t.Fatalf("an elevation was remembered: allowedBefore=%v state=%s", f.AllowedBefore(ap3), ap3.State)
	}
	other, _ := f.Hold(me, HoldRequest{Tool: "Bash", Command: "sudo id", Rule: guard.RuleElevation, Cwd: "/w"})
	f.CancelHold(me, ap3.ID)
	f.CancelHold(me, other.ID)

	// Any other held rule: the same call allowed once is allowed again without a card.
	push := HoldRequest{Tool: "Bash", Command: "git push --force origin main", Rule: guard.RuleGitRewrite, Cwd: "/w"}
	first, _ := f.Hold(me, push)
	if _, err := f.Decide(OwnerCaller, first.ID, DecideRequest{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	second, err := f.Hold(me, push)
	if err != nil || second.State != model.ApprovalAllowed || second.Answer != AnswerSame {
		t.Fatalf("same call again: %+v %v", second, err)
	}
	tl, _ = f.Timeline("head", 0, 200)
	if !strings.Contains(tl[len(tl)-1].Text, "Allowed: same as approved") {
		t.Fatalf("timeline: %q", tl[len(tl)-1].Text)
	}
	if third, _ := f.Hold(me, HoldRequest{Tool: "Bash", Command: "git push --force origin dev", Rule: guard.RuleGitRewrite, Cwd: "/w"}); third.State != model.ApprovalPending {
		t.Fatal("a different command was auto-allowed")
	}
	// go-reviewer-o4: as narrow as Allow always. Another folder (relative paths mean other files)
	// asks again, and so does another kind of agent.
	elsewhere := push
	elsewhere.Cwd = "/other"
	if h, _ := f.Hold(me, elsewhere); h.State != model.ApprovalPending {
		t.Fatal("the same command in another folder was auto-allowed")
	}
	sudoElsewhere, _ := f.Hold(me, HoldRequest{Tool: "Bash", Command: "sudo true", Rule: guard.RuleElevation, Cwd: "/other"})
	if f.AllowedBefore(sudoElsewhere) {
		t.Fatal("an elevation allowed in /w skips the PIN in /other")
	}
	if _, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "beta"}); err != nil {
		t.Fatal(err)
	}
	lead := mustAgent(t, f, "beta-lead")
	leadCaller := callerFor(t, f, e.chat, lead.ID)
	if h, _ := f.Hold(leadCaller, push); h.State != model.ApprovalPending {
		t.Fatal("another kind of agent got the auto-allow")
	}

	// The settings PUT keeps the memory: only the git push (elevation is never recorded).
	if s, _ := f.SetGuardSettings(OwnerCaller, guard.Settings{Enabled: true}); len(s.AllowedBefore) != 1 {
		t.Fatalf("settings PUT dropped the memory: %v", s.AllowedBefore)
	}
}
