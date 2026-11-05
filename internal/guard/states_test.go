package guard

import "testing"

// The owner's rule switches (3 Nov 22:14): every switchable rule × every state, and the fixed
// carve-outs whatever the switches say. Findings are injected directly: no command strings here
// (those live in switches_cmd_test.go, Linux CI only).

func TestEveryRuleEveryState(t *testing.T) {
	for _, r := range Rules() {
		for _, st := range []Outcome{Block, Hold, Allow} {
			states := DefaultStates()
			states[r.ID] = st
			a := newAnalyzer(Context{States: states})
			a.found = append(a.found, finding{rule: r.ID, detail: "x"})
			v := a.verdict()
			if v.Outcome != st {
				t.Errorf("%s set to %s: got %s", r.ID, st, v.Outcome)
			}
			if st != Allow && v.Rule != r.ID {
				t.Errorf("%s set to %s: verdict names %q", r.ID, st, v.Rule)
			}
		}
	}
}

// Two holds: the non-rememberable one names the verdict, in either order (go-reviewer-o4, #241).
// write-outside defaults to Allowed, so it is switched to Held here to make a real tie.
func TestHoldTiePrefersTheNonRememberable(t *testing.T) {
	held := DefaultStates()
	held[RuleWriteOutside] = Hold
	for _, order := range [][]string{{RuleWriteOutside, RulePermsOutside}, {RulePermsOutside, RuleWriteOutside}} {
		a := newAnalyzer(Context{States: held})
		for _, r := range order {
			a.found = append(a.found, finding{rule: r})
		}
		if v := a.verdict(); v.Outcome != Hold || v.Rule != RulePermsOutside {
			t.Fatalf("%v: got %s/%s, want hold/%s", order, v.Outcome, v.Rule, RulePermsOutside)
		}
	}
	// Two rememberable holds: the first stays.
	a := newAnalyzer(Context{States: held})
	a.found = []finding{{rule: RuleWriteOutside}, {rule: RuleGitRewrite}}
	if v := a.verdict(); v.Outcome != Hold || v.Rule != RuleWriteOutside {
		t.Fatalf("two rememberable holds: %s/%s", v.Outcome, v.Rule)
	}
}

// An allowed rule never hides another rule's block or hold in the same call.
func TestPerFindingNotPerCall(t *testing.T) {
	states := DefaultStates()
	states[RuleKillByName] = Allow
	states[RuleDisk] = Block
	states[RuleElevation] = Hold
	a := newAnalyzer(Context{States: states})
	a.found = []finding{{rule: RuleKillByName}, {rule: RuleElevation}, {rule: RuleDisk}}
	if v := a.verdict(); v.Outcome != Block || v.Rule != RuleDisk {
		t.Fatalf("got %+v, want the disk block", v)
	}
	a.found = []finding{{rule: RuleKillByName}, {rule: RuleElevation}}
	if v := a.verdict(); v.Outcome != Hold || v.Rule != RuleElevation {
		t.Fatalf("got %+v, want the elevation hold", v)
	}
}

// Fixed carve-outs ignore every switch, including one that names them.
func TestFixedRulesIgnoreStates(t *testing.T) {
	for _, r := range FixedRules() {
		states := DefaultStates()
		states[r.ID] = Allow // forged: a fixed rule has no switch
		for id := range states {
			states[id] = Allow
		}
		a := newAnalyzer(Context{States: states})
		a.found = []finding{{rule: r.ID}}
		if v := a.verdict(); v.Outcome != r.Outcome {
			t.Errorf("fixed %s: got %s, want %s", r.ID, v.Outcome, r.Outcome)
		}
		if _, ok := DefaultStates()[r.ID]; ok {
			t.Errorf("fixed %s has a switch", r.ID)
		}
	}
}

// No states (nil) = the strict built-in outcomes; the brief's defaults where it says so.
func TestDefaultsAndStrict(t *testing.T) {
	want := map[string]Outcome{
		RuleBrowser: Allow, RuleDisk: Block, RuleDeleteRoot: Block, RulePermsRoot: Block, RuleBoot: Block,
		RuleKillByName: Allow, RuleShutdown: Allow, RuleTmxState: Allow, RuleTmxCLI: Hold,
		RuleElevation: Hold, RulePipeShell: Allow,
		// The owner's correction (22:16): the held list, Held for me / Allowed.
		RuleBrowserMention: Allow, RuleWriteOutside: Allow, RuleClaudeConfig: Allow, RuleDesktop: Allow,
		RuleHelloKey: Hold, RuleNetwork: Hold, RuleDeleteOutside: Hold, RulePermsOutside: Hold, RuleGitRewrite: Hold,
	}
	d := DefaultStates()
	for id, o := range want {
		if d[id] != o {
			t.Errorf("default of %s = %s, want %s", id, d[id], o)
		}
	}
	for _, r := range Rules() {
		if (Context{}).StateOf(r.ID) != r.Outcome {
			t.Errorf("strict %s changed", r.ID)
		}
	}
	if (Context{States: map[string]Outcome{RuleDisk: "maybe"}}).StateOf(RuleDisk) != Block {
		t.Error("an invalid state was taken")
	}
	if (Context{}).StateOf("no-such-rule") != Block {
		t.Error("an unknown rule did not block")
	}
}

// Settings: switches overlay the defaults; fixed rules and junk never enter.
func TestEffectiveStates(t *testing.T) {
	s := Settings{RuleStates: map[string]Outcome{RuleDisk: Allow, RuleKillTmx: Allow, "nope": Allow, RuleElevation: "x"}}
	e := s.Effective()
	if e[RuleDisk] != Allow || e[RuleElevation] != Hold {
		t.Fatalf("effective: %v", e)
	}
	if _, ok := e[RuleKillTmx]; ok {
		t.Fatal("a fixed rule got a state")
	}
	if _, ok := e["nope"]; ok {
		t.Fatal("an unknown rule got a state")
	}
}
