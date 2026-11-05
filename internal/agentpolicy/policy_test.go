package agentpolicy

import (
	"reflect"
	"testing"
)

func TestWindowsHarnessAdmission(t *testing.T) {
	for _, h := range []string{"claude", "codex", "opencode", "pi", "shell", "", "unknown", "Claude"} {
		for _, sandbox := range []bool{false, true} {
			p := LaunchPolicy{GOOS: "windows", Harness: h, Sandboxed: sandbox}
			want := h == "claude" || sandbox
			if got := CheckLaunch(p) == nil; got != want {
				t.Errorf("%q sandbox=%v allowed=%v", h, sandbox, got)
			}
		}
	}
	for _, os := range []string{"linux", "darwin"} {
		if err := CheckLaunch(LaunchPolicy{GOOS: os, Harness: "codex"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCodexParityCapabilityIsNotGenericSandbox(t *testing.T) {
	for _, h := range []string{"codex", "agy", "antigravity", "opencode", "pi"} {
		err := CheckLaunch(LaunchPolicy{GOOS: "windows", Harness: h, CodexParityGuard: true})
		if (err == nil) != (h == "codex") {
			t.Fatalf("harness %s: %v", h, err)
		}
	}
}

func TestBrowserPolicy(t *testing.T) {
	for _, allow := range []bool{false, true} {
		p := LaunchPolicy{GOOS: "windows", Harness: "claude", BrowserAutomation: true, AllowBrowserAutomation: allow}
		if got := CheckLaunch(p) == nil; got != allow {
			t.Errorf("allow=%v got=%v", allow, got)
		}
	}
	for _, command := range []string{`chrome.exe`, `"C:\Program Files\Chrome\chrome.exe"`, `Start-Process msedge`, `chromium --headless`, `firefox`, `npx playwright test`, `node -e "require('puppeteer')"`, `python -m playwright`, `tool --remote-debugging-port=9222`, `tool --remote-debugging-pipe`, `browser.connect_over_cdp(endpoint)`, `mcp__playwright__browser_navigate`} {
		if err := CheckCommand("windows", false, command); err == nil {
			t.Errorf("allowed %q", command)
		}
		if err := CheckCommand("windows", true, command); err != nil {
			t.Errorf("host allowed: %v", err)
		}
		if err := CheckCommand("linux", false, command); err != nil {
			t.Errorf("Linux: %v", err)
		}
	}
	if err := CheckCommand("windows", false, "go test ./internal/agentpolicy"); err != nil {
		t.Fatal(err)
	}
}

// A mention of a browser or automation tool in text that is not actually launching or
// targeting one (a grep, a file name, a test name, a commit message, a chat message) must not
// be hard-blocked: the owner can still approve it (Hold), unlike a real launch (Block).
func TestBrowserCommandPositionVsMention(t *testing.T) {
	command := []string{
		`chrome.exe`,
		`"C:\Program Files\Chrome\chrome.exe"`,
		`Start-Process msedge`,
		`chromium --headless`,
		`firefox`,
		`npx playwright test`,
		`node -e "require('puppeteer')"`,
		`python -m playwright`,
		`tool --remote-debugging-port=9222`,
		`tool --remote-debugging-pipe`,
		`browser.connect_over_cdp(endpoint)`,
		`mcp__playwright__browser_navigate`,
	}
	for _, c := range command {
		if got := ClassifyBrowserCommand("windows", false, c); got != BrowserCommand {
			t.Errorf("%q: want BrowserCommand, got %v", c, got)
		}
		if err := CheckCommand("windows", false, c); err == nil {
			t.Errorf("CheckCommand allowed %q", c)
		}
	}
	mention := []string{
		`git grep chrome`,
		`cat internal/guard/browser_test.go`,
		`go test ./internal/guard -run TestBrowser`,
		`npx browserslist`,
		`git commit -m "fix the browser guard"`,
		`tmx send x "check the browser"`,
	}
	for _, c := range mention {
		if got := ClassifyBrowserCommand("windows", false, c); got != BrowserMention {
			t.Errorf("%q: want BrowserMention, got %v", c, got)
		}
		if err := CheckCommand("windows", false, c); err != nil {
			t.Errorf("CheckCommand blocked a mere mention %q: %v", c, err)
		}
	}
	if got := ClassifyBrowserCommand("windows", false, "go test ./internal/agentpolicy"); got != BrowserOK {
		t.Errorf("unrelated command: want BrowserOK, got %v", got)
	}
}

// A launch chained after another command, piped, or wrapped in cmd /c, pwsh -c, powershell -c
// or the PowerShell call operator is still a launch: checking only the first word of the whole
// line let these through as a mere mention.
func TestBrowserLaunchChainedOrWrapped(t *testing.T) {
	for _, c := range []string{
		`git status; chrome.exe`,
		`cd x && npx playwright test`,
		`echo | node -e "require('puppeteer')"`,
		`& "C:\Program Files\Chrome\chrome.exe"`,
		`cmd /c start msedge`,
		`pwsh -c chrome`,
		`powershell -c Start-Process chrome`,
		`Start-Process -FilePath chrome`,
		`a && msedge`,
		`x | y; firefox`,
		`env PATH=x chrome`,
		`timeout 5 chrome.exe`,
	} {
		if got := ClassifyBrowserCommand("windows", false, c); got != BrowserCommand {
			t.Errorf("%q: want BrowserCommand, got %v", c, got)
		}
		if err := CheckCommand("windows", false, c); err == nil {
			t.Errorf("CheckCommand allowed %q", c)
		}
	}
}

// start/Start-Process takes an optional window title before the program, so the program may not
// be the first non-flag word; and cmd/powershell/pwsh may carry other switches before /c|/k or
// -c|-command. Both must still be recognized as a launch, not a mere mention.
func TestBrowserLaunchTitleAndSwitchGaps(t *testing.T) {
	for _, c := range []string{
		`start "" chrome`,
		`cmd /c start "" chrome`,
		`start "x" msedge`,
		`powershell -NoProfile -Command chrome`,
		`pwsh -NoLogo -NonInteractive -c "Start-Process chrome"`,
		`cmd /d /c chrome`,
		`cmd /s /c chrome`,
	} {
		if got := ClassifyBrowserCommand("windows", false, c); got != BrowserCommand {
			t.Errorf("%q: want BrowserCommand, got %v", c, got)
		}
		if err := CheckCommand("windows", false, c); err == nil {
			t.Errorf("CheckCommand allowed %q", c)
		}
	}
}

// An encoded or obfuscated PowerShell command cannot be read into words at all, so it is
// Blocked outright rather than guessed at.
func TestBrowserEncodedPowerShellIsBlocked(t *testing.T) {
	for _, c := range []string{
		`powershell -EncodedCommand BASE64DATAAAAAAAAAAAAAAAAAA==`,
		`pwsh -enc BASE64DATAAAAAAAAAAAAAAAAAA==`,
		`powershell -e BASE64DATAAAAAAAAAAAAAAAAAA==`,
	} {
		if got := ClassifyBrowserCommand("windows", false, c); got != BrowserCommand {
			t.Errorf("%q: want BrowserCommand, got %v", c, got)
		}
	}
}

// powershell/pwsh switches that take a separate value word (-ExecutionPolicy Bypass,
// -WindowStyle Hidden, ...) must not stop the scan for the run switch; / is also a valid switch
// character in Windows PowerShell 5.1; -Command abbreviates; -File runs a script file directly;
// and in Windows PowerShell 5.1 a bare first word is itself the command.
func TestBrowserPowerShellValueSwitchesAndAbbreviations(t *testing.T) {
	for _, c := range []string{
		`powershell -ExecutionPolicy Bypass -Command "Start-Process chrome"`,
		`pwsh -WindowStyle Hidden -c chrome`,
		`powershell -Version 5.1 -c msedge`,
		`pwsh -NoProfile -WindowStyle Hidden -Comm chrome`,
		`powershell /c chrome`,
		`powershell -ec ZQBjAGgAbwA=`,
		`powershell -File C:\x\chrome-launch.ps1`,
		`powershell chrome`,
	} {
		if got := ClassifyBrowserCommand("windows", false, c); got != BrowserCommand {
			t.Errorf("%q: want BrowserCommand, got %v", c, got)
		}
		if err := CheckCommand("windows", false, c); err == nil {
			t.Errorf("CheckCommand allowed %q", c)
		}
	}
}

func TestAgentEnvDenyList(t *testing.T) {
	input := []string{"PATH=bin", "HOME=home", "TMXG_AGENT_TOKEN=own", "GH_TOKEN=owner", "gh_host=host", "OWNER_TOKEN=owner", "OPENAI_API_KEY=key", "AWS_SECRET_ACCESS_KEY=key", "AWS_ACCESS_KEY_ID=id", "AZURE_CLIENT_SECRET=secret", "PASSWORD=password", "CUSTOM_SECRET=secret", "custom_token=secret", "TOKEN=secret", "API_KEY=secret", "SECRET=secret", "SSH_AUTH_SOCK=socket", "GITHUB_TOKEN=secret", "MALFORMED", "=C:=drive", "SAFE=a=b"}
	want := []string{"PATH=bin", "HOME=home", "TMXG_AGENT_TOKEN=own", "SAFE=a=b"}
	copyInput := append([]string(nil), input...)
	if got := FilterEnv(input); !reflect.DeepEqual(got, want) {
		t.Errorf("unexpected retained names/count: %d", len(got))
	}
	if !reflect.DeepEqual(input, copyInput) {
		t.Fatal("mutated input")
	}
}

func TestInheritedFleetIdentity(t *testing.T) {
	for key, want := range map[string]bool{
		"TMX_URL": true, "tmx_agent_token": true, "TMX_AGENT_TOKEN": true, "TMX_RELAY_TOKEN": true,
		"TMX_HOST": false, "TMX_AGENT": false, "TMXG_AGENT_TOKEN": false, "TMXG_URL": false, "GH_TOKEN": false,
	} {
		if got := InheritedFleetIdentity(key); got != want {
			t.Errorf("InheritedFleetIdentity(%q) = %v, want %v", key, got, want)
		}
	}
}
