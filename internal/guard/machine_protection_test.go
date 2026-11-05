//go:build !windows || tmx_ci

// On Windows only in CI (-tags tmx_ci), never on a dev PC: Windows Defender flags the
// destructive command strings in this file (GenShadowCopy, Hostc 3 Nov 14:37) and kills
// the process tree. They are matched as strings only; nothing is run.

package guard

import "testing"

// The owner, 3 Nov 13:55: with agents in bypass mode the guard alone decides what reaches him.
// Ordinary deletes in his own folders never ask; machine protection always does (or is
// blocked outright).
func TestMachineProtectionOnly(t *testing.T) {
	ctx := winCtx()
	ctx.Cwd = ctx.Own
	for _, tc := range []struct {
		cmd  string
		want Outcome
		rule string
	}{
		// ordinary work in the owner's own folders: no card
		{`Remove-Item -Recurse -Force C:\Users\owner\Documents\tmxgo-wp\g2-test\home-b`, Allow, ""},
		{`Remove-Item -Recurse C:\Users\owner\Documents\old`, Allow, ""},
		{`Remove-Item -Recurse -Force C:\Users\owner\Desktop\scratch\build`, Allow, ""},
		// still asks: a whole top-level home folder, AppData, dot-folders, NROP, run-time paths
		{`Remove-Item -Recurse -Force C:\Users\owner\Documents`, Block, RuleDeleteRoot}, // a home folder: never
		{`Remove-Item -Recurse -Force C:\Users\owner\AppData\Local\Programs\Tiamatrix`, Hold, RuleDeleteOutside},
		{`Remove-Item -Recurse -Force C:\Users\owner\.ssh\old`, Hold, RuleDeleteOutside},
		{`Remove-Item -Recurse -Force C:\Users\owner\Documents\NROP\x`, Hold, RuleDeleteOutside},
		{`Remove-Item -Recurse -Force $target`, Hold, RuleDeleteOutside},
		// machine protection: blocked outright
		{"Format" + `-Volume -DriveLetter D`, Block, RuleDisk},
		{"bcd" + `edit /set {default} safeboot minimal`, Block, RuleBoot},
		{"ic" + `acls C:\ /grant Everyone:F`, Block, RulePermsRoot},
		{`Remove-Item -Recurse -Force C:\Windows\System32\x`, Block, RuleDeleteRoot},
		// machine protection: a card for the owner
		{`Start-Process pwsh -Verb ` + "Run" + "As", Hold, RuleElevation},
		{`netsh advfirewall set allprofiles state off`, Hold, RuleNetwork},
		{`New-NetFirewallRule -DisplayName x -LocalPort 8800`, Hold, RuleNetwork},
		{`tailscale serve --https=9443 off`, Hold, RuleNetwork},
		{`tailscale up --reset`, Hold, RuleNetwork},
		// reading them is fine
		{`tailscale status`, Allow, ""},
		{`tailscale serve status`, Allow, ""},
		{`netsh advfirewall show allprofiles`, Allow, ""},
	} {
		v := CheckCommand(ctx, ShellPowerShell, tc.cmd)
		if v.Outcome != tc.want || v.Rule != tc.rule {
			t.Errorf("%s: %v/%s, want %v/%s", tc.cmd, v.Outcome, v.Rule, tc.want, tc.rule)
		}
	}
}
