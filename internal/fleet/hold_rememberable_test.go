package fleet

import (
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// P0, 4 Nov 04:27 (tmx-lead, go-reviewer-o4): only the Rememberable rules may be remembered.
// Every other rule, with its switch set to Held, still brings a card after an Allow, needs the
// proof again, and is never written to allowed_before. One case per rule, so a future rule is
// covered by default.
func TestOnlyRememberableRulesAreRemembered(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	me := callerFor(t, f, e.chat, head.ID)

	var all []guard.Rule
	all = append(all, guard.Rules()...)
	all = append(all, guard.FixedRules()...)
	checked := 0
	for _, r := range all {
		if guard.Rememberable[r.ID] {
			continue
		}
		if !r.Fixed {
			if _, err := f.SetRuleState(OwnerCaller, r.ID, guard.Hold); err != nil {
				t.Fatalf("%s: switch to Held: %v", r.ID, err)
			}
		} else if r.Outcome != guard.Hold {
			continue // a fixed Block never becomes a hold at all
		}
		req := HoldRequest{Tool: "Bash", Command: "placeholder-" + r.ID, Rule: r.ID, Cwd: "/w"}
		first, err := f.Hold(me, req)
		if err != nil || first.State != model.ApprovalPending {
			t.Fatalf("%s: first hold: %+v %v", r.ID, first, err)
		}
		if _, err := f.Decide(OwnerCaller, first.ID, DecideRequest{Decision: "allow"}); err != nil {
			t.Fatalf("%s: allow: %v", r.ID, err)
		}
		again, err := f.Hold(me, req)
		if err != nil || again.State != model.ApprovalPending {
			t.Fatalf("%s: the same call after an Allow did not bring a card: %+v %v", r.ID, again, err)
		}
		if f.AllowedBefore(again) {
			t.Fatalf("%s: the same call after an Allow skips the proof", r.ID)
		}
		f.CancelHold(me, again.ID)
		checked++
	}
	if n := len(f.GuardSettings().AllowedBefore); n != 0 {
		t.Fatalf("non-rememberable rules were recorded: %d entries", n)
	}
	// The 9 Sep class and the lockout rows are among them, by name.
	for _, id := range []string{guard.RuleDisk, guard.RuleDeleteRoot, guard.RulePermsRoot, guard.RuleBoot, guard.RulePermsOutside,
		guard.RuleElevation, guard.RuleHelloKey, guard.RuleNetwork, guard.RuleTmxCLI, guard.RuleBrowserScript} {
		if guard.Rememberable[id] {
			t.Errorf("%s is rememberable", id)
		}
	}
	if checked < 10 {
		t.Fatalf("only %d rules checked", checked)
	}
}
