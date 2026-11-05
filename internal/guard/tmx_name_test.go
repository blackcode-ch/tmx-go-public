package guard

import "testing"

// Hostb's log, 3 Nov 22:22: a read-only query naming the process tmx-go was blocked as "tmx \s+
// acts as the owner". A process NAME is not a tmx invocation.
func TestTmxProcessNameIsNotACommand(t *testing.T) {
	ctx := Context{OS: "windows", Home: `C:\Users\user`, Own: `C:\Users\user\work\app`, Workspace: `C:\Users\user\work`,
		StateDir: `C:\Users\user\Documents\tmxgo-trial`, States: DefaultStates(),
		TmxNames: []string{`C:\Users\user\Documents\tmxgo-trial\bin\tmx-go.exe`}}
	cmd := `$p=@(Get-Process tmx-go -ErrorAction SilentlyContinue | % Id); netstat -ano -p tcp | Select-String 'LISTENING' | ForEach-Object { $c=($_ -split '\s+'); [pscustomobject]@{Local=$c[2];PID=$c[5]} } | Where-Object { $p -contains [int]$_.PID -or $_.Local -match ':(8787|43911|4001|8797)$' } | Format-Table -AutoSize | Out-String; netstat -ano -p udp | Select-String ($p -join '|') | Out-String`
	if v := CheckCommand(ctx, ShellPowerShell, cmd); v.Outcome != Allow {
		t.Fatalf("got %s/%s (%s)", v.Outcome, v.Rule, v.Detail)
	}
	for _, c := range []string{"Get-Process tmx-go", "Get-Process -Name tmx-go | Select-Object Id", "Get-Process tmxg"} {
		if v := CheckCommand(ctx, ShellPowerShell, c); v.Outcome != Allow {
			t.Errorf("%q: got %s/%s (%s)", c, v.Outcome, v.Rule, v.Detail)
		}
	}
	// A real invocation still counts.
	if v := CheckCommand(ctx, ShellPowerShell, `& C:\Users\user\Documents\tmxgo-trial\bin\tmx-go.exe approve k2`); v.Rule != RuleTmxCLI {
		t.Fatalf("real tmx-go call: %+v", v)
	}
}
