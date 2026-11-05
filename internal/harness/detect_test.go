package harness

import (
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner/term/screen"
)

// Screens from v2's tests/test_detect.py (real captures, words neutralised) and the live
// calibration against Claude Code (see DESIGN.md "Screen detection").

var claudeTrust = strings.Split(` Accessing workspace:

 C:\Users\x\proj

 Quick safety check: Is this a project you created or one you trust? (Like your own code)

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel`, "\n")

var codexTrust = strings.Split(`  Trust this folder? Codex can read, edit, and run files here.

› 1. Trust and continue
  2. Quit

  enter continue · esc quit`, "\n")

var claudePermission = strings.Split(`╭──────────────────────────────────────╮
│ Bash command
│   rm -rf build
│ Do you want to proceed?
│ ❯ 1. Yes
│   2. Yes, and don't ask again for rm commands in C:\x
│   3. No, and tell Claude what to do differently (esc)
╰──────────────────────────────────────╯`, "\n")

var claudeRealApproval = []string{"  ⎿  $ echo second >> gamma.txt", "", strings.Repeat("─", 120), " Bash command",
	` Tip: auto mode handles these prompts for you — choose "switch to auto mode" below`, "",
	"   echo second >> gamma.txt", `   Append "second" to gamma.txt`, "", " Do you want to proceed?",
	" ❯ 1. Yes",
	`   2. Yes, and always allow access to C:\Users\user\Documents\tmx-sandbox from this project`,
	"   3. Yes, and switch to auto mode · auto mode handles these prompts for you", "   4. No", "",
	" Esc to cancel · Tab to amend"}

var claudeAutoModeNag = strings.Split(` Make auto mode your default permission mode?

   Auto mode lets Claude handle permission prompts automatically.

   ❯ Yes, set auto mode as my default permission mode
     No, keep don't ask`, "\n")

func pats(t *testing.T, h model.Harness) (*screen.Patterns, screen.Synonyms) {
	t.Helper()
	i, ok := Get(h)
	if !ok {
		t.Fatalf("no harness %s", h)
	}
	return i.Patterns, i.Synonyms
}

func classify(t *testing.T, h model.Harness, lines []string) screen.Detection {
	t.Helper()
	p, _ := pats(t, h)
	return screen.Classify(lines, p, 0)
}

func plan(t *testing.T, h model.Harness, lines []string, choice string) string {
	t.Helper()
	p, syn := pats(t, h)
	keys, err := screen.PlanAnswer(screen.Classify(lines, p, 0), choice, syn)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestClaudeTrustNavigatesToYes(t *testing.T) {
	d := classify(t, model.HarnessClaude, claudeTrust)
	if d.State != screen.Trust || strings.Join(d.Labels(), "|") != "No, exit|Yes, I trust this folder" || d.Selected != 1 {
		t.Fatalf("%+v", d)
	}
	if k := plan(t, model.HarnessClaude, claudeTrust, "Yes, I trust this folder"); k != "down enter" {
		t.Fatal(k)
	}
}

func TestCodexTrust(t *testing.T) {
	d := classify(t, model.HarnessCodex, codexTrust)
	if d.State != screen.Trust || d.Selected != 1 {
		t.Fatalf("%+v", d)
	}
	if k := plan(t, model.HarnessCodex, codexTrust, "Trust"); k != "enter" {
		t.Fatal(k)
	}
}

func TestClaudePermissionAnswers(t *testing.T) {
	d := classify(t, model.HarnessClaude, claudePermission)
	if d.State != screen.Approval || !strings.Contains(d.Prompt, "Do you want to proceed?") {
		t.Fatalf("%+v", d)
	}
	for choice, want := range map[string]string{"approve": "enter", "approve_always": "down enter", "deny": "down down enter", "3": "down down enter"} {
		if k := plan(t, model.HarnessClaude, claudePermission, choice); k != want {
			t.Errorf("%s: %q, want %q", choice, k, want)
		}
	}
	_, err := screen.PlanAnswer(d, "maybe", nil)
	if err == nil || !strings.Contains(err.Error(), "the options are") {
		t.Fatalf("err %v", err)
	}
}

func TestClaudeRealApproval(t *testing.T) {
	d := classify(t, model.HarnessClaude, claudeRealApproval)
	if d.State != screen.Approval || !strings.Contains(d.Prompt, "echo second >> gamma.txt") || !strings.Contains(d.Prompt, "Do you want to proceed?") {
		t.Fatalf("%+v", d)
	}
	// approve_always never picks "switch to auto mode"
	if k := plan(t, model.HarnessClaude, claudeRealApproval, "approve_always"); k != "down enter" {
		t.Fatal(k)
	}
	if k := plan(t, model.HarnessClaude, claudeRealApproval, "deny"); k != "down down down enter" {
		t.Fatal(k)
	}
}

func TestStates(t *testing.T) {
	if d := classify(t, model.HarnessClaude, []string{"  ✻ Thinking… (12s · esc to interrupt)"}); d.State != screen.Working {
		t.Fatal(d.State)
	}
	if d := classify(t, model.HarnessClaude, []string{"", ">", "  ? for shortcuts"}); d.State != screen.Idle {
		t.Fatal(d.State)
	}
	if d := classify(t, model.HarnessClaude, []string{"", "", ""}); d.State != screen.Starting {
		t.Fatal(d.State)
	}
	p, _ := pats(t, model.HarnessClaude)
	if d := screen.Classify([]string{"some output"}, p, time.Second); d.State != screen.Working {
		t.Fatal("recent unknown output", d.State)
	}
	if d := screen.Classify([]string{"some output"}, p, 10*time.Second); d.State != screen.Idle {
		t.Fatal("quiet unknown screen", d.State)
	}
}

func TestUnknownMenuIsDialogAndGateAnswersNo(t *testing.T) {
	d := classify(t, model.HarnessClaude, claudeAutoModeNag)
	if d.State != screen.Dialog {
		t.Fatal(d.State)
	}
	info, _ := Get(model.HarnessClaude)
	var label string
	for _, g := range info.Gates {
		if g.Rx.MatchString(strings.Join(claudeAutoModeNag, "\n")) {
			label = g.Label
		}
	}
	if label != "No" {
		t.Fatalf("gate %q", label)
	}
	if k, _ := screen.PlanAnswer(d, label, nil); k != "down enter" {
		t.Fatal(k)
	}
}

func TestCodexLockScreen(t *testing.T) {
	lines := []string{"  🔒  This conversation is open in another app", "      Close it there and press R to continue here.",
		"   r retry   f fork   esc/ctrl+c/q exit   ctrl+t transcript"}
	if d := classify(t, model.HarnessCodex, lines); d.State != screen.Locked {
		t.Fatal(d.State)
	}
}

var claudeIdleAfterWrapped = []string{
	"❯ wait you're connected to it no? Just paste it yourself no? The whole point of this was for me to not have to do",
	"  that",
	"",
	"● You're right, I can send it straight to Hosta myself. Do you want to proceed? 1. Yes 2. No",
	"",
	"✻ Cooked for 11s · done 2:44 PM",
	strings.Repeat("─", 80),
	"❯",
	strings.Repeat("─", 80),
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents",
}

func TestHistoryIsNotADialogWhenTheFooterIsLive(t *testing.T) {
	if d := classify(t, model.HarnessClaude, claudeIdleAfterWrapped); d.State != screen.Idle {
		t.Fatal(d.State)
	}
	working := append(append([]string{}, claudeIdleAfterWrapped[:len(claudeIdleAfterWrapped)-1]...),
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt")
	if d := classify(t, model.HarnessClaude, working); d.State != screen.Working {
		t.Fatal(d.State)
	}
}

func TestNumberedReplyIsNotAMenu(t *testing.T) {
	scr := []string{"● Addendum 2 sent to the lead:",
		"  1. Pronouns: he/him in all docs and agent briefs.",
		"  2. Every data source is a plug-in, ready for all your data later:",
		"  3. A second model checks the secret filter before any real data goes in:",
		"  4. Sealed predictions stay out of sight:",
		"",
		"✻ Sautéed for 52s · done 3:31 PM · 1 shell still running",
		"",
		strings.Repeat("─", 60), ">", strings.Repeat("─", 60),
		"  ⏵⏵ bypass permissions on · 1 shell · ← for agents · ↓ to manage"}
	if d := classify(t, model.HarnessClaude, scr); d.State != screen.Idle {
		t.Fatal(d.State)
	}
	p, _ := pats(t, model.HarnessClaude)
	reply := []string{"● Here is the plan:", "  1. Down the chain: ask every member.", "  2. Back up: consolidate their answers.",
		"  - credit who said what", "  - flag divergences", ""}
	if d := screen.Classify(reply, p, 10*time.Second); d.State == screen.Dialog {
		t.Fatal("a numbered reply without a cursor became a dialog")
	}
	if d := classify(t, model.HarnessClaude, []string{"Pick one:", "❯ 1. Yes", "  2. No"}); d.State != screen.Dialog {
		t.Fatal(d.State)
	}
}

func TestOwnSentMessageIsNotAMenu(t *testing.T) {
	p, _ := pats(t, model.HarnessClaude)
	for _, scr := range [][]string{
		{"● Done.", "", "> Right and we need to redesign the website as well, and also", "  the other thing we talked about earlier today", "", "✻ Thinking…", ""},
		{"> 1. first point of my message", "  2. second point", ""},
		{"● Still missing: company bank statements, payroll, receipts.", "",
			"❯ ok thanks, I'll get the bank exports from WIR and Yapeal tonight and drop them in the inbox folder,",
			"  then we look at the payroll tomorrow morning together with the fiduciary if she has time", ""},
	} {
		if d := screen.Classify(scr, p, 10*time.Second); d.State == screen.Dialog || d.State == screen.Question {
			t.Fatalf("%v: %s", scr, d.State)
		}
	}
}

func TestCodexUsageLimit(t *testing.T) {
	weekly := strings.Split("■ You’ve hit your usage limit. Visit https://chatgpt.com/codex/settings/usage to purchase more credits "+
		"or try again at\nOct 20th, 2026 7:52 AM.\n\n\n› Ask Codex to do anything\n\n"+
		"  gpt-6-astra medium · ~\\AppData\\Local\\TerminalMatrix2\\work", "\n")
	d := classify(t, model.HarnessCodex, weekly)
	if d.State != screen.UsageLimit || d.Prompt != "Oct 20th, 2026 7:52 AM" {
		t.Fatalf("%+v", d)
	}
	rolling := strings.Split("■ You've hit your usage limit. Try again at 6:39 PM.\n\n\n› Ask Codex to do anything\n\n  gpt-6-astra medium · ~\\daai", "\n")
	if d := classify(t, model.HarnessCodex, rolling); d.State != screen.UsageLimit || d.Prompt != "6:39 PM" {
		t.Fatalf("%+v", d)
	}
	recovered := append([]string{}, weekly...)
	for i := 0; i < 20; i++ {
		recovered = append(recovered, "line")
	}
	recovered = append(recovered, "› Ask Codex to do anything", "", "  gpt-6-astra medium · ~\\work")
	if d := classify(t, model.HarnessCodex, recovered); d.State == screen.UsageLimit {
		t.Fatal("an old banner still counts")
	}
}

func TestCodexInputBox(t *testing.T) {
	p, _ := pats(t, model.HarnessCodex)
	for _, l := range []string{"  GPT-6-Astra medium · D:\\daai · Review team meeting context", "  gpt-5.1 medium · ~\\Documents\\x", "› Ask Codex to do anything", "›"} {
		if !p.AtPrompt.MatchString(l) {
			t.Errorf("at_prompt misses %q", l)
		}
	}
	for _, l := range []string{"› 1. Trust and continue", "                  ⚠ 1 warning · f2 to view"} {
		if p.AtPrompt.MatchString(l) {
			t.Errorf("at_prompt matches %q", l)
		}
	}
	long := []string{"  Worked for 39m 16s · Sep 25 at 13:36", "",
		"› Supervisor Broadcast: Terminal Matrix v2 is deployed! You and astra-1 can now",
		"  communicate directly via 'tmx msg astra-1 <text>', check messages via 'tmx inbox' and more,",
		"  state via 'tmx ls'. Outstanding work on completing Milestone 5.3 and the end-to-end runner",
		"  and final verification report. The DAAI prototype is 100% complete across all 5 phases!", "", "",
		"  GPT-6-Astra medium · D:\\daai · Review team meeting context",
		"                                  ⚠ 1 warning · f2 to view"}
	if d := classify(t, model.HarnessCodex, long); d.State != screen.Idle {
		t.Fatal(d.State)
	}
	if d := classify(t, model.HarnessCodex, codexTrust); d.State != screen.Trust {
		t.Fatal(d.State)
	}
}

func TestClaudeQuestionToolWithDescriptions(t *testing.T) {
	scr := []string{
		"│ How should I reconcile this?", "",
		"> 1. Keep 4 sections, 1 item each (Recommended)",
		"     Pick the single best-fitting content line per existing group.",
		"  2. Expand to 7 sections",
		"     Add 3 new sections and update check.py.",
		"  3. Keep 4 sections, but pick which 4 of the 7 groups",
		"     I'll propose dropping 3 of the 7 groups.",
		"  4. Type something.",
		strings.Repeat("─", 80),
		"  5. Chat about this", "",
		"Enter to select · ↑/↓ to navigate · Esc to cancel"}
	d := classify(t, model.HarnessClaude, scr)
	if d.State != screen.Question || len(d.Choices) != 5 || d.Choices[1].Label != "Expand to 7 sections" {
		t.Fatalf("%+v", d)
	}
	if k := plan(t, model.HarnessClaude, scr, "Expand"); k != "2" {
		t.Fatal(k)
	}
}

func TestGatesAndTrustableFolder(t *testing.T) {
	dir := t.TempDir()
	if !TrustableFolder(dir) {
		t.Fatalf("%s should be trustable", dir)
	}
	home, _ := os.UserHomeDir()
	for _, d := range []string{"/", home, "/usr", ""} {
		if TrustableFolder(d) {
			t.Errorf("%q must not be trustable", d)
		}
	}
	info, _ := Get(model.HarnessClaude)
	wrapped := "Quick safety check: Is this a project you created or one you\n trust? (Like your own code"
	if !info.Gates[0].Rx.MatchString(wrapped) || !info.Gates[0].Trust {
		t.Fatal("the wrapped trust question is not the trust gate")
	}
}

// Live capture, Claude Code 2.1.309 at 120 columns (23 Oct 2026): a Bash permission whose second
// option wraps at the screen's edge, the command framed by dashed rules, "manual mode" footer.
var claudeWrappedOption = screenOf(`⏺ Bash(touch made-by-tmx.txt)
  ⎿  Waiting…

%RULE%
 Bash command
 Create a file named made-by-tmx.txt
%DASH%
 touch made-by-tmx.txt
%DASH%
 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and always allow access to /private/tmp/claude-501/-Users-x-Developer-terminal-matrix/05a59965-1ef0-4a30-a140-
      ff43ab8618a1/scratchpad/harnesses/ws/calib from this project
   3. No

 Esc to cancel · Tab to amend`)

// screenOf splits a capture into lines; %RULE% and %DASH% are rules as wide as its longest line.
func screenOf(s string) []string {
	lines := strings.Split(s, "\n")
	w := 0
	for _, l := range lines {
		w = max(w, utf8.RuneCountInString(l))
	}
	for i, l := range lines {
		l = strings.ReplaceAll(l, "%RULE%", strings.Repeat("─", w))
		lines[i] = strings.ReplaceAll(l, "%DASH%", strings.Repeat("╌", w))
	}
	return lines
}

func TestClaudeWrappedOptionAndDashedRules(t *testing.T) {
	d := classify(t, model.HarnessClaude, claudeWrappedOption)
	if d.State != screen.Approval || len(d.Choices) != 3 || d.Selected != 1 {
		t.Fatalf("%+v", d)
	}
	if want := "Yes, and always allow access to /private/tmp/claude-501/-Users-x-Developer-terminal-matrix/05a59965-1ef0-4a30-a140-ff43ab8618a1/scratchpad/harnesses/ws/calib from this project"; d.Choices[1].Label != want {
		t.Fatalf("label %q", d.Choices[1].Label)
	}
	if strings.Contains(d.Prompt, "╌") || !strings.Contains(d.Prompt, "touch made-by-tmx.txt") || !strings.HasPrefix(d.Prompt, "Bash command") {
		t.Fatalf("prompt %q", d.Prompt)
	}
	if k := plan(t, model.HarnessClaude, claudeWrappedOption, "deny"); k != "down down enter" {
		t.Fatal(k)
	}
	idle := screenOf("⏺ done\n\n%RULE%\n❯\n%RULE%\n  ⏸ manual mode on · 1 shell")
	if d := classify(t, model.HarnessClaude, idle); d.State != screen.Idle {
		t.Fatalf("manual mode footer: %+v", d)
	}
}

// Codex's confirmation of a switch to Full Access (codex-rs tui snapshot
// full_access_confirmation_popup.snap): a menu for the owner, never a gate tmx answers.
var codexFullAccessConfirm = strings.Split(`      Enable full access?
› As  When Codex runs with full access, it can edit any file on your
      computer and run commands with network, without your approval.
  GP  Exercise caution when enabling full access. This significantly
      increases the risk of data loss, leaks, or unexpected behavior.


    › 1. Yes, continue anyway  Apply full access for this session
      2. Cancel                Go back without enabling full access

      enter select · esc back`, "\n")

func TestCodexFullAccessConfirmationIsNotAGate(t *testing.T) {
	d := classify(t, model.HarnessCodex, codexFullAccessConfirm)
	if !d.State.Menu() {
		t.Fatalf("the full-access confirmation is not a menu: %s", d.State)
	}
	info, _ := Get(model.HarnessCodex)
	text := strings.Join(codexFullAccessConfirm, "\n")
	for _, g := range info.Gates {
		if g.Rx.MatchString(text) {
			t.Fatalf("a gate answers the full-access confirmation: %q", g.Label)
		}
	}
	for _, g := range info.Gates {
		if strings.Contains(strings.ToLower(g.Label), "continue anyway") {
			t.Fatalf("gate %q", g.Label)
		}
	}
}
