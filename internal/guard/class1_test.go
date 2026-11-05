//go:build !windows || tmx_ci

// On Windows only in CI (-tags tmx_ci), never on a dev PC: Windows Defender flags the
// destructive command strings in this file (GenShadowCopy, Hostc 3 Nov 14:37) and kills
// the process tree. They are matched as strings only; nothing is run.

package guard

import (
	"encoding/base64"
	"testing"
	"unicode/utf16"
)

// The architect's precondition for bypass mode (3 Nov): Go's guard covers Python pcguard's
// class-1 set, every rule with a test, each held for the owner (a card) or blocked outright.
// The strings are only parsed here, never run; some are split so that no shell guard on a
// developer's machine mistakes this file's text for a command.

func j(p ...string) string {
	s := ""
	for _, x := range p {
		s += x
	}
	return s
}

func psEncoded(cmd string) string {
	u := utf16.Encode([]rune(cmd))
	b := make([]byte, 0, 2*len(u))
	for _, r := range u {
		b = append(b, byte(r), byte(r>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestClass1Coverage(t *testing.T) {
	ctx := winCtx()
	ctx.Cwd = ctx.Own
	type tc struct {
		py, cmd string
		sh      Shell
		want    Outcome
		rule    string
	}
	ps, sh := ShellPowerShell, ShellPOSIX
	for _, c := range []tc{
		// R01 disk
		{"R01", j("for", "mat C: /q"), ps, Block, RuleDisk},
		{"R01", j("Format", "-Volume -DriveLetter D"), ps, Block, RuleDisk},
		{"R01", j("Clear", "-Disk -Number 1"), ps, Block, RuleDisk},
		{"R01", j("Remove", "-Partition -DiskNumber 1"), ps, Block, RuleDisk},
		{"R01", j("Initialize", "-Disk 1"), ps, Block, RuleDisk},
		{"R01", j("disk", "part /s x.txt"), ps, Block, RuleDisk},
		{"R01", j("ci", "pher /w:C:\\"), ps, Block, RuleDisk},
		{"R01", j("ci", "pher /e /s:C:\\data"), ps, Block, RuleDisk},
		// R02 boot
		{"R02", j("bcd", "edit /set {default} safeboot minimal"), ps, Block, RuleBoot},
		{"R02", j("bcd", "edit /store C:\\x"), ps, Block, RuleBoot},
		{"R02", j("bcd", "boot C:\\Windows"), ps, Block, RuleBoot},
		{"R02", j("mount", "vol X: /d"), ps, Block, RuleBoot},
		{"R02", j("mount", "vol \\\\?\\Volume{abc}\\ /p"), ps, Block, RuleBoot},
		// R03 recovery / restore points / backups
		{"R03", j("rea", "gentc /disable"), ps, Block, RuleBoot},
		{"R03", j("vss", "admin delete shadows /all"), ps, Block, RuleDisk},
		{"R03", j("wb", "admin delete systemstatebackup"), ps, Block, RuleDisk},
		{"R03", j("wm", "ic shadowcopy delete"), ps, Block, RuleDisk},
		{"R03", j("Get-CimInstance Win32_ShadowCopy | ", "Remove-CimInstance"), ps, Block, RuleDisk},
		{"R03", j("Get-WmiObject Win32_ShadowCopy | ForEach-Object { $_.", "Delete() }"), ps, Block, RuleDisk},
		// R03a permissions/ownership on drive roots, system folders, profiles
		{"R03a", j("take", "own /F C:\\ /R"), ps, Block, RulePermsRoot},
		{"R03a", j("ca", "cls C:\\ /G Everyone:F"), ps, Block, RulePermsRoot},
		{"R03a", j("ic", "acls C:\\Users /grant Everyone:F"), ps, Block, RulePermsRoot},
		{"R03a", j("ic", "acls C:\\Windows /reset /T"), ps, Block, RulePermsRoot},
		{"R03a", j("Set", "-Acl -Path C:\\Windows -AclObject $a"), ps, Block, RulePermsRoot},
		{"R03a", j("sub", "inacl /file C:\\ /grant=x"), ps, Block, RulePermsRoot},
		{"R03a", j("set", "acl -on C:\\ -ot file -actn ace"), ps, Block, RulePermsRoot},
		{"R03a", j("Set", "-Acl -Path C:\\Users\\owner\\Documents\\x -AclObject $a"), ps, Hold, RulePermsOutside},
		// R05 recursive deletes of system folders, drive roots, the profile root
		{"R05", j("Remove", "-Item -Recurse -Force C:\\Windows"), ps, Block, RuleDeleteRoot},
		{"R05", j("Remove", "-Item -Recurse -Force C:\\Users"), ps, Block, RuleDeleteRoot},
		{"R05", j("Remove", "-Item -Recurse -Force C:\\"), ps, Block, RuleDeleteRoot},
		{"R05", j("Remove", "-Item -Recurse -Force C:\\Users\\owner"), ps, Block, RuleDeleteRoot},
		{"R05", j("Remove", "-Item -Recurse -Force 'C:\\Program Files'"), ps, Block, RuleDeleteRoot},
		// R07 kill by name
		{"R07", j("task", "kill /IM node.exe /F"), ps, Block, RuleKillByName},
		{"R07", j("Stop", "-Process -Name python"), ps, Block, RuleKillByName},
		{"R07", j("pk", "ill node"), sh, Block, RuleKillByName},
		// R16 elevation
		{"R16", j("Start", "-Process pwsh -Verb Run", "As"), ps, Hold, RuleElevation},
		{"R16", j("ru", "nas /user:admin cmd"), ps, Hold, RuleElevation},
		{"R16", j("gs", "udo whoami"), ps, Hold, RuleElevation},
		{"R16", j("su", "do true"), sh, Hold, RuleElevation},
		// R18 Windows Hello / security keys
		{"R18", j("ssh", "-keygen -t ed25519-sk"), ps, Hold, RuleHelloKey},
		{"R18", j("ssh", "-keygen -t ecdsa-sk -f k"), sh, Hold, RuleHelloKey},
		{"R18", j("ssh", "-keygen -K"), ps, Hold, RuleHelloKey},
		{"R18", j("ssh", "-keygen -O resident -t ecdsa-sk"), ps, Hold, RuleHelloKey},
		{"R18", j("ssh", "-keygen -Y sign -f id_ed25519_sk -n file x"), sh, Hold, RuleHelloKey},
		{"R18", j("fido2", "-token -L"), ps, Hold, RuleHelloKey},
		{"R18", j("fido2", "-assert -G x"), sh, Hold, RuleHelloKey},
		{"R18", j("yk", "man list"), ps, Hold, RuleHelloKey},
		{"R18", j("bash -c 'ssh", "-keygen -t ecdsa-sk'"), sh, Hold, RuleHelloKey},
		{"R18", j("powershell -Command \"ssh", "-keygen -K\""), ps, Hold, RuleHelloKey},
		// tailscale / firewall
		{"TS/FW", j("tailscale ", "down"), ps, Hold, RuleNetwork},
		{"TS/FW", j("tailscale ", "logout"), ps, Hold, RuleNetwork},
		{"TS/FW", j("tailscale ", "up --reset"), ps, Hold, RuleNetwork},
		{"TS/FW", j("tailscale ", "set --ssh=false"), ps, Hold, RuleNetwork},
		{"TS/FW", j("tailscale ", "serve --https=9443 off"), ps, Hold, RuleNetwork},
		{"TS/FW", j("Stop", "-Service Tailscale"), ps, Hold, RuleNetwork},
		{"TS/FW", j("Set", "-Service Tailscale -StartupType Disabled"), ps, Hold, RuleNetwork},
		{"TS/FW", j("sc.exe", " stop Tailscale"), ps, Hold, RuleNetwork},
		{"TS/FW", j("net ", "stop Tailscale"), ps, Hold, RuleNetwork},
		{"TS/FW", j("netsh ", "advfirewall set allprofiles state off"), ps, Hold, RuleNetwork},
		{"TS/FW", j("netsh ", "firewall set opmode disable"), ps, Hold, RuleNetwork},
		{"TS/FW", j("Set", "-NetFirewallProfile -Enabled False"), ps, Hold, RuleNetwork},
		{"TS/FW", j("New", "-NetFirewallRule -DisplayName x -LocalPort 8800"), ps, Hold, RuleNetwork},
		{"TS/FW", j("Remove", "-NetFirewallRule -DisplayName x"), ps, Hold, RuleNetwork},
		{"TS/FW", j("uf", "w disable"), sh, Hold, RuleNetwork},
		// reads stay free (not class-1)
		{"read", j("ssh", "-keygen -t ed25519 -f k"), ps, Allow, ""},
		{"read", "tailscale status", ps, Allow, ""},
		{"read", "netsh advfirewall show allprofiles", ps, Allow, ""},
		{"read", "Get-Service Tailscale", ps, Allow, ""},
	} {
		v := CheckCommand(ctx, c.sh, c.cmd)
		if v.Outcome != c.want || v.Rule != c.rule {
			t.Errorf("%s %q: %v/%s, want %v/%s", c.py, c.cmd, v.Outcome, v.Rule, c.want, c.rule)
		}
	}
	// An encoded PowerShell command is never run unread: with browser automation off (the
	// default) it is blocked whatever it contains. Pinned so it cannot drift to another outcome.
	v := CheckCommand(ctx, ShellPowerShell, "powershell -EncodedCommand "+psEncoded(j("ssh", "-keygen -K")))
	if v.Outcome != Block {
		t.Errorf("encoded command not blocked: %+v", v)
	}
}
