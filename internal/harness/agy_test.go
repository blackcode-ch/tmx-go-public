package harness

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/term/screen"
)

func TestAgyLaunchAndAlias(t *testing.T) {
	for _, h := range []string{"agy", "antigravity"} {
		info, ok := Get(model.Harness(h))
		if !ok || info.Name != "agy" {
			t.Fatal(h)
		}
		c, err := TerminalCommand(runner.Spec{Harness: model.Harness(h), Command: "/test/agy", InitialPrompt: "[tmx instructions]\nhello", Model: "gemini", SessionID: "existing"})
		want := []string{"--mode", "auto", "--model", "gemini", "-i", "[tmx instructions]\nhello"}
		if err != nil || !slices.Equal(c.Args, want) {
			t.Fatalf("%v %v", c.Args, err)
		}
	}
	c, err := TerminalCommand(runner.Spec{Harness: "agy", Command: "/test/agy", AgyFullAccess: true})
	if err != nil || !slices.Equal(c.Args, []string{"--dangerously-skip-permissions"}) {
		t.Fatalf("owner opt-in: %v %v", c.Args, err)
	}
	if _, err := TerminalCommand(runner.Spec{Harness: "agy", Command: "/test/agy", PermissionMode: "plan", AgyFullAccess: true}); err == nil {
		t.Fatal("read-only accepted")
	}
}

func TestAgyScreensAndNumericAnswers(t *testing.T) {
	info, _ := Get("agy")
	for _, tc := range []struct {
		text  string
		state screen.State
	}{
		{"Thought for 12s\n╭────────╮\n│ >      │\n╰────────╯\n? for shortcuts", screen.Idle},
		{"╰────────╯\nUsing read_file\n? for shortcuts", screen.Working},
		{"> earlier prompt\nThought for 12s\nUsing read_file", screen.Working},
		{">", screen.Working},
		{"? for shortcuts\nAllow creation of this file?\n> 1. Yes, allow access\n  2. No, deny access", screen.Approval},
		{"Allow creation of this file?\n> 1. Yes, allow access\n  2. No, deny access", screen.Approval},
		{"Do you trust the contents of this project?\n> Yes, I trust this folder\n  No, exit", screen.Trust},
	} {
		if d := screen.Classify(strings.Split(tc.text, "\n"), info.Patterns, time.Minute); d.State != tc.state {
			t.Fatalf("%q: %s", tc.text, d.State)
		}
	}
	d := screen.Classify(strings.Split("Allow creation of this file?\n> 1. Yes, allow access\n  2. No, deny access", "\n"), info.Patterns, 0)
	for choice, want := range map[string]string{"approve": "1 enter", "deny": "2 enter"} {
		got, err := screen.PlanAnswer(d, choice, info.Synonyms)
		if err != nil || got != want {
			t.Fatalf("%s: %q %v", choice, got, err)
		}
	}
}

// TestAgyTrustGateAnswersOwnWorkspaceOnly is tmx-lead's follow-up (3 Nov): agy shows its trust
// dialog even with --mode auto, so tmx must pre-answer it like Claude's and Codex's own trust
// gates, but only for the agent's own workspace, never a drive root or the owner's home folder.
// The gate (harness.go) and the watcher's TrustableFolder check already implement this; this
// locks the exact screen sample in the report in place as a regression test.
func TestAgyTrustGateAnswersOwnWorkspaceOnly(t *testing.T) {
	info, _ := Get("agy")
	text := "Do you trust the contents of this project?\n> Yes, I trust this folder\n  No, exit"
	d := screen.Classify(strings.Split(text, "\n"), info.Patterns, time.Minute)
	if d.State != screen.Trust {
		t.Fatalf("not detected as a trust dialog: %s", d.State)
	}
	var gate *Gate
	for i := range info.Gates {
		if info.Gates[i].Rx.MatchString(text) {
			gate = &info.Gates[i]
		}
	}
	if gate == nil || !gate.Trust || gate.Label != "Yes, I trust this folder" {
		t.Fatalf("agy has no trust gate for this dialog: %+v", gate)
	}
	got, err := screen.PlanAnswer(d, gate.Label, info.Synonyms)
	if err != nil || got != "1 enter" {
		t.Fatalf("plan answer: %q %v", got, err)
	}
	// TrustableFolder (checked by the watcher before answering any Trust gate, including this
	// one) refuses a drive root or the owner's home folder - only a real own-workspace
	// subfolder is auto-trusted.
	tmp := t.TempDir()
	if !TrustableFolder(tmp) {
		t.Fatalf("own workspace folder refused: %s", tmp)
	}
	if home, _ := os.UserHomeDir(); home != "" && TrustableFolder(home) {
		t.Fatal("owner's home folder was auto-trusted")
	}
	if root := filepath.VolumeName(tmp) + string(filepath.Separator); TrustableFolder(root) {
		t.Fatal("a drive root was auto-trusted")
	}
}
