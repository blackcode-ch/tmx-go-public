package fleet

import (
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// The owner's rule switches (3 Nov 22:14): only the owner flips a rule; fixed carve-outs have no
// switch; every flip is logged; holds and blocks follow the current state; a restart keeps it.
func TestRuleSwitches(t *testing.T) {
	e := newEnv(t)
	f := e.started()

	// Upgrade: the defaults are applied once and logged by tmx.
	s := f.GuardSettings()
	if s.DefaultsVersion != guard.DefaultsVersion || len(s.RuleLog) == 0 {
		t.Fatalf("defaults not recorded: v%d, %d log entries", s.DefaultsVersion, len(s.RuleLog))
	}
	for _, ch := range s.RuleLog {
		if ch.By != "tmx" || ch.Old == ch.New {
			t.Fatalf("upgrade log entry: %+v", ch)
		}
	}
	upgradeLog := len(s.RuleLog)
	f.mu.Lock()
	f.applyRuleDefaultsLocked()
	f.mu.Unlock()
	if len(f.GuardSettings().RuleLog) != upgradeLog {
		t.Fatal("the defaults were logged twice")
	}

	// Who may flip.
	for _, c := range []Caller{AgentCaller("head"), PeerCaller("hostb"), CompanionCaller} {
		if _, err := f.SetRuleState(c, guard.RuleElevation, guard.Allow); KindOf(err) != KindForbidden {
			t.Fatalf("%+v flipped a rule: %v", c, err)
		}
	}
	if _, err := f.SetRuleState(OwnerCaller, guard.RuleKillTmx, guard.Allow); KindOf(err) != KindInvalid {
		t.Fatal("a fixed carve-out got a switch")
	}
	if _, err := f.SetRuleState(OwnerCaller, guard.RuleElevation, "sometimes"); KindOf(err) != KindInvalid {
		t.Fatal("an invalid state was taken")
	}

	// A hold is accepted only while the rule is held.
	head := mustAgent(t, f, "head")
	me := callerFor(t, f, e.chat, head.ID)
	e.chat.Last(head.ID).State(model.StateWorking)
	waitState(t, f, "head", model.StateWorking)
	req := HoldRequest{Tool: "Bash", Command: "sudo true", Rule: guard.RuleElevation, Cwd: "/w"}
	ap, err := f.Hold(me, req)
	if err != nil || ap.State != model.ApprovalPending {
		t.Fatalf("held by default: %+v %v", ap, err)
	}
	f.CancelHold(me, ap.ID)

	out, err := f.SetRuleState(OwnerCaller, guard.RuleElevation, guard.Allow)
	if err != nil || out.Effective()[guard.RuleElevation] != guard.Allow {
		t.Fatalf("flip: %v %v", err, out.RuleStates)
	}
	last := out.RuleLog[len(out.RuleLog)-1]
	if last.Rule != guard.RuleElevation || last.Old != guard.Hold || last.New != guard.Allow || last.By != "owner" || last.At == "" {
		t.Fatalf("log: %+v", last)
	}
	if f.GuardStates()[guard.RuleElevation] != guard.Allow {
		t.Fatal("the hook would not see the flip")
	}
	if _, err := f.Hold(me, req); KindOf(err) != KindInvalid {
		t.Fatalf("a hold for an allowed rule was accepted: %v", err)
	}
	// Blocked: a block report is accepted, a hold is not.
	if _, err := f.SetRuleState(OwnerCaller, guard.RuleElevation, guard.Block); err != nil {
		t.Fatal(err)
	}
	if err := f.ReportBlock(me, req); err != nil {
		t.Fatalf("block report for a blocked rule: %v", err)
	}
	if _, err := f.Hold(me, req); KindOf(err) != KindInvalid {
		t.Fatal("a hold for a blocked rule was accepted")
	}

	// The general settings PUT can neither set nor drop switches.
	inject := guard.Settings{Enabled: true, RuleStates: map[string]guard.Outcome{guard.RuleDisk: guard.Allow}}
	if s, err := f.SetGuardSettings(OwnerCaller, inject); err != nil || s.Effective()[guard.RuleDisk] != guard.Block || s.Effective()[guard.RuleElevation] != guard.Block {
		t.Fatalf("settings PUT changed switches: %v %v", err, s.RuleStates)
	}

	// A restart keeps the switches and the log and doesn't re-log the defaults.
	saved := f.GuardSettings()
	e.guard = &saved
	g := e.fleet()
	if err := g.Start(); err != nil {
		t.Fatal(err)
	}
	if g.GuardStates()[guard.RuleElevation] != guard.Block || len(g.GuardSettings().RuleLog) != len(saved.RuleLog) {
		t.Fatalf("after restart: %v, %d log entries (was %d)", g.GuardStates()[guard.RuleElevation], len(g.GuardSettings().RuleLog), len(saved.RuleLog))
	}
}
