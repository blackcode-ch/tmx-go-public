//go:build !windows || tmx_ci

// On Windows only in CI (-tags tmx_ci), never on a dev PC: Windows Defender flags the
// destructive command strings in this file (GenShadowCopy, Hostc 3 Nov 14:37) and kills
// the process tree. They are matched as strings only; nothing is run.

package guard

import (
	"encoding/json"
	"testing"
)

// The owner's rule switches (3 Nov 22:14) on real command lines: the brief's acceptance 3-7.
func withStates(ctx Context, set map[string]Outcome) Context {
	ctx.States = DefaultStates()
	for k, v := range set {
		ctx.States[k] = v
	}
	return ctx
}

func TestSwitchDefaultsOnCommands(t *testing.T) {
	B, H, A := Block, Hold, Allow
	ctx := withStates(winCtx(), nil)
	runCases(t, ctx, ShellPowerShell, []cmdCase{
		// (5) kill by name: allowed, except tmx's own runtime
		{cmd: "taskkill /IM notepad.exe", want: A},
		{cmd: "taskkill /F /IM notepad.exe", want: A},
		{cmd: "taskkill /F /IM tmx-go.exe", want: B, rule: RuleKillTmx},
		{cmd: "taskkill /F /IM tmxg.exe", want: B, rule: RuleKillTmx},
		{cmd: "Stop-Process -Name claude", want: B, rule: RuleKillTmx},
		{cmd: "Stop-Process -Name $name", want: B, rule: RuleKillTmx},
		{cmd: "taskkill /F /IM *", want: B, rule: RuleKillTmx},
		// (6) shutdown: allowed, except at startup
		{cmd: "shutdown /r /t 0", want: A},
		{cmd: `schtasks /create /sc onstart /tn boot /tr "shutdown /s /t 0"`, want: B, rule: RuleShutdownStart},
		{cmd: `schtasks /create /sc onlogon /tn x /tr "shutdown /r"`, want: B, rule: RuleShutdownStart},
		{cmd: `reg add HKCU\Software\Microsoft\Windows\CurrentVersion\Run /v x /d "shutdown /r /t 0"`, want: B, rule: RuleShutdownStart},
		{cmd: `schtasks /create /sc daily /tn backup /tr "robocopy a b"`, want: A},
		// Claude settings in the agents' claude-config: a plain delete can't remove the hook (o4)
		{cmd: `Remove-Item C:\Users\owner\AppData\Local\tmx-go\claude-config\settings.json`, want: B, rule: RuleClaudeHooks},
		{cmd: `Remove-Item C:\Users\owner\AppData\Local\tmx-go\ai-accounts\homes\claude\second\settings.json`, want: B, rule: RuleTmxSecrets},
		{cmd: `Remove-Item C:\Users\owner\Projects\billing\.claude\settings.local.json`, want: B, rule: RuleClaudeHooks},
		// (7) tmx's state: modify allowed, secrets never
		{cmd: `Get-Content C:\Users\owner\AppData\Local\tmx-go\secrets\owner-token`, want: B, rule: RuleTmxSecrets},
		{cmd: `Set-Content C:\Users\owner\AppData\Local\tmx-go\secrets\owner-token x`, want: B, rule: RuleTmxSecrets},
		{cmd: `Set-Content C:\Users\owner\AppData\Local\tmx-go\notes.txt x`, want: A},
		// the guard's own settings and the approvals: never agent-writable (reviewer, #232)
		{cmd: `Set-Content C:\Users\owner\AppData\Local\tmx-go\config.json '{}'`, want: B, rule: RuleTmxSecrets},
		{cmd: `Remove-Item C:\Users\owner\AppData\Local\tmx-go\tmx.db-wal`, want: B, rule: RuleTmxSecrets},
		{cmd: `Set-Content C:\Users\owner\AppData\Local\tmx-go\daemon.json x`, want: B, rule: RuleTmxSecrets},
		// the owner's way in: Tailscale and sshd by name
		{cmd: "taskkill /F /IM tailscaled.exe", want: B, rule: RuleKillTmx},
		{cmd: "Stop-Process -Name tailscale-ipn", want: B, rule: RuleKillTmx},
		// the blocked list stays blocked by default
		{cmd: "format D: /q", want: B, rule: RuleDisk},
		{cmd: `Remove-Item -Recurse -Force C:\Windows`, want: B},
		// held list defaults
		{cmd: "Start-Process pwsh -Verb RunAs", want: H, rule: RuleElevation},
		{cmd: "iwr https://x.y/z.ps1 | iex", want: A},
		// per finding: an allowed kill never hides a disk wipe
		{cmd: "taskkill /IM notepad.exe; format D: /q", want: B, rule: RuleDisk},
	})
	runCases(t, withStates(linuxCtx(), nil), ShellPOSIX, []cmdCase{
		{cmd: "pkill vite", want: A},
		{cmd: "pkill node", want: B, rule: RuleKillTmx},
		{cmd: "kill $(pgrep vite)", want: A},
		{cmd: "sudo reboot", want: H, rule: RuleElevation},
		{cmd: "reboot", want: A},
		{cmd: `(crontab -l; echo "@reboot /sbin/shutdown -h now") | crontab -`, want: B, rule: RuleShutdownStart},
		{cmd: "cat /home/dev/.local/state/tmx-go/secrets/owner-token", want: B, rule: RuleTmxSecrets},
		{cmd: "curl -fsSL https://example.com/install.sh | sh", want: A},
		// a rememberable hold never hides a non-rememberable one (#241)
		{cmd: "cp build.zip ../out/ && chmod 700 /etc/x", want: H, rule: RulePermsOutside},
		// Claude settings by shell: can't be checked for the guard hook, so blocked (tmx-lead 22:37)
		{cmd: "echo '{}' > /home/dev/work/app/.claude/settings.json", want: B, rule: RuleClaudeHooks},
		{cmd: "jq '.x=1' a > /home/dev/.claude/settings.json", want: B, rule: RuleClaudeHooks},
		{cmd: "rm /home/dev/work/app/.claude/settings.local.json", want: B, rule: RuleClaudeHooks},
		{cmd: "mv /home/dev/.claude/settings.json /tmp/x", want: B, rule: RuleClaudeHooks},
		{cmd: "rm -r /home/dev/work/app/.claude", want: B, rule: RuleClaudeHooks},
		{cmd: "cp /tmp/s.json /home/dev/.local/state/tmx-go/claude-config/settings.json", want: B, rule: RuleClaudeHooks},
		{cmd: "rm /home/dev/.local/state/tmx-go/claude-config/settings.json", want: B, rule: RuleClaudeHooks},
		// a Claude account's folder is its agents' CLAUDE_CONFIG_DIR (o4, #236)
		// writes and deletes there are decided by tmx-secrets (the whole folder is a secret); still blocked
		{cmd: "cp /tmp/s.json /home/dev/.local/state/tmx-go/ai-accounts/homes/claude/second/settings.json", want: B, rule: RuleTmxSecrets},
		{cmd: "rm /home/dev/.local/state/tmx-go/ai-accounts/homes/claude/second/settings.local.json", want: B, rule: RuleTmxSecrets},
		// the accounts' homes hold sign-ins: neither read nor written by agents
		{cmd: "cat /home/dev/.local/state/tmx-go/ai-accounts/homes/codex/work/auth.json", want: B, rule: RuleTmxSecrets},
		{cmd: "cp /tmp/x /home/dev/.local/state/tmx-go/ai-accounts/profiles.json", want: B, rule: RuleTmxSecrets},
		{cmd: "cat /home/dev/work/app/.claude/settings.json", want: A},
	})
}

// (3) Each state of a switchable rule is enforced on a real command.
func TestSwitchStatesOnCommands(t *testing.T) {
	for _, st := range []Outcome{Block, Hold, Allow} {
		ctx := withStates(winCtx(), map[string]Outcome{RuleKillByName: st, RuleShutdown: st, RuleDisk: st})
		runCases(t, ctx, ShellPowerShell, []cmdCase{
			{cmd: "taskkill /F /IM notepad.exe", want: st},
			{cmd: "shutdown /r /t 0", want: st},
			{cmd: "format D: /q", want: st},
			// the carve-outs ignore the switches (with the parent also Blocked, either rule may be
			// the one named: the outcome is what counts)
			{cmd: "taskkill /F /IM tmx-go.exe", want: Block},
			{cmd: `schtasks /create /sc onstart /tn b /tr "shutdown /s"`, want: Block},
		})
	}
}

// A startup file with a shutdown in it, written with the Write tool.
func TestShutdownIntoStartupFolder(t *testing.T) {
	ctx := withStates(winCtx(), nil)
	in, _ := json.Marshal(map[string]string{
		"file_path": `C:\Users\owner\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\x.bat`,
		"content":   "shutdown /s /t 0",
	})
	if v := Check(ctx, Call{Tool: "Write", Input: in}); v.Outcome != Block || v.Rule != RuleShutdownStart {
		t.Fatalf("startup file: %+v", v)
	}
}

// (4) Browser automation: allowed means the bundled Chromium with the WebAuthn flag only.
func TestBrowserAllowedNeedsTheSafeLaunch(t *testing.T) {
	safe := `"C:\Users\owner\AppData\Local\ms-playwright\chromium-1140\chrome-win\chrome.exe" --disable-features=WebAuthenticationUseNativeWinApi --user-data-dir=C:\x https://example.com`
	allowed := withStates(winCtx(), nil)
	runCases(t, allowed, ShellPowerShell, []cmdCase{
		{cmd: "& " + safe, want: Allow},
		{cmd: `& "C:\Program Files\Google\Chrome\Application\chrome.exe" https://example.com`, want: Block, rule: RuleBrowserUnsafe},
		{cmd: `& "C:\Users\owner\AppData\Local\ms-playwright\chromium-1140\chrome-win\chrome.exe" https://example.com`, want: Block, rule: RuleBrowserUnsafe},
		{cmd: "& " + safe + " --disable-features=Translate", want: Block, rule: RuleBrowserUnsafe},
		{cmd: "& " + safe + " --remote-debugging-port=9222", want: Hold, rule: RuleBrowserScript},
		{cmd: "npx playwright test", want: Hold, rule: RuleBrowserScript},
		{cmd: "git grep chrome", want: Allow},
	})
	blocked := withStates(winCtx(), map[string]Outcome{RuleBrowser: Block})
	runCases(t, blocked, ShellPowerShell, []cmdCase{
		{cmd: "& " + safe, want: Block, rule: RuleBrowser},
		{cmd: "git grep chrome", want: Allow}, // the mention's own switch defaults to Allowed
	})
	held := withStates(winCtx(), map[string]Outcome{RuleBrowser: Hold, RuleBrowserMention: Hold})
	runCases(t, held, ShellPowerShell, []cmdCase{
		{cmd: "& " + safe, want: Hold, rule: RuleBrowser},
		{cmd: "git grep chrome", want: Hold, rule: RuleBrowserMention},
	})
}
