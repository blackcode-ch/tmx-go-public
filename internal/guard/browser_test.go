package guard

import (
	"encoding/json"
	"regexp"
	"testing"
)

func TestBrowserDispatchRefused(t *testing.T) {
	for _, call := range []Call{
		{Tool: "Bash", Input: json.RawMessage(`{"command":"npx playwright test"}`)},
		{Tool: "PowerShell", Input: json.RawMessage(`{"command":"Start-Process chrome.exe"}`)},
		{Tool: "mcp__playwright__browser_navigate", Input: json.RawMessage(`{}`)},
		{Tool: "mcp__browser__navigate", Input: json.RawMessage(`{}`)},
		{Tool: "mcp__cdp__attach", Input: json.RawMessage(`{}`)},
	} {
		if !regexp.MustCompile(Matcher).MatchString(call.Tool) {
			t.Errorf("hook misses %s", call.Tool)
		}
		if got := Check(Context{OS: "windows"}, call); got.Outcome != Block {
			t.Errorf("allowed %s", call.Tool)
		}
	}
}

// A command that merely mentions a browser or automation tool (as an argument, a file name, a
// test name, a commit message) must not be hard-blocked: the owner can approve it (Hold), not
// the outright refusal a real browser launch gets (Block).
func TestBrowserMentionIsHold(t *testing.T) {
	for _, command := range []string{
		`git grep chrome`,
		`cat internal/guard/browser_test.go`,
		`go test ./internal/guard -run TestBrowser`,
		`npx browserslist`,
		`git commit -m "fix the browser guard"`,
		`echo "check the browser"`,
	} {
		got := CheckCommand(Context{OS: "windows"}, ShellPOSIX, command)
		if got.Outcome != Hold {
			t.Errorf("%q: want Hold, got %v (%s)", command, got.Outcome, got.RuleName)
		}
	}
	if got := CheckCommand(Context{OS: "windows"}, ShellPOSIX, "go test ./internal/agentpolicy"); got.Outcome != Allow {
		t.Errorf("unrelated command: want Allow, got %v", got.Outcome)
	}
}

func TestBrowserPermissionIsWeakening(t *testing.T) {
	s := DefaultSettings()
	var next Settings
	if err := json.Unmarshal([]byte(`{"enabled":true,"allow_browser_automation":true}`), &next); err != nil {
		t.Fatal(err)
	}
	if !s.Weaker(next) {
		t.Fatal("browser permission must require owner gate")
	}
	if next.Weaker(s) {
		t.Fatal("revoking browser permission is not weakening")
	}
}
