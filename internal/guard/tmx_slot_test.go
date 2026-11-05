package guard

import "testing"

// Hostb's log (3 Nov): `tmx slot -- <command>` (the heavy-command slot) was blocked as an owner
// command. The guard now checks the wrapped command instead; other tmx commands are unchanged.
func TestTmxSlotChecksTheWrappedCommand(t *testing.T) {
	ctx := Context{OS: "linux", Home: "/home/dev", Own: "/home/dev/work/app", Workspace: "/home/dev/work",
		StateDir: "/home/dev/.local/state/tmx-go", Temp: []string{"/tmp"}, States: DefaultStates()}
	for _, tc := range []struct {
		cmd  string
		want Outcome
		rule string
	}{
		{"tmx slot -- go test ./internal/fleet", Allow, ""},
		{"tmx slot --kind cpu -- go build ./...", Allow, ""},
		{"tmxg slot -- go vet ./...", Allow, ""},
		{"tmx slot -- sudo true", Hold, RuleElevation},   // the wrapped command is still checked
		{"tmx slot -- tmx approve k2", Hold, RuleTmxCLI}, // also a tmx command inside it
		{"tmx slot", Hold, RuleTmxCLI},                   // no command: not the wrapper form
		{"tmx approve k2m4", Hold, RuleTmxCLI},
		{"tmx status", Allow, ""},
	} {
		v := CheckCommand(ctx, ShellPOSIX, tc.cmd)
		if v.Outcome != tc.want || tc.rule != "" && v.Rule != tc.rule {
			t.Errorf("%q: got %s/%s, want %s/%s", tc.cmd, v.Outcome, v.Rule, tc.want, tc.rule)
		}
	}
	// Strict (no owner states): the owner command is still a block, the slot wrapper still passes.
	strict := ctx
	strict.States = nil
	if v := CheckCommand(strict, ShellPOSIX, "tmx slot -- go test ./x"); v.Outcome != Allow {
		t.Errorf("strict slot: %+v", v)
	}
}
