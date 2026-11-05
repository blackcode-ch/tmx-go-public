package fleet

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestGuardHookAtLaunch(t *testing.T) {
	old := launchGOOS
	launchGOOS = "linux" // explicit guard opt-out is unavailable on Windows
	t.Cleanup(func() { launchGOOS = old })
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	spec := e.chat.Last(head.ID).Spec
	if spec.SettingsPath == "" {
		t.Fatal("a chat-mode Claude agent got no guard hook")
	}
	b, err := os.ReadFile(spec.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type, Command string
				Timeout       int
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	pre := cfg.Hooks["PreToolUse"]
	if len(pre) != 1 || pre[0].Matcher != guard.Matcher || len(pre[0].Hooks) != 1 ||
		pre[0].Hooks[0].Command != HookCommand("/opt/tmx/tmx") || pre[0].Hooks[0].Timeout < int(guard.HoldWait/time.Second) {
		t.Fatalf("hook config: %s", b)
	}
	env := strings.Join(spec.Env, "\n")
	for _, want := range []string{guard.EnvFolder + "=" + head.Cwd, guard.EnvWorkspace + "=" + e.workspace, guard.EnvState + "=" + e.home} {
		if !strings.Contains(env, want) {
			t.Fatalf("env lacks %s:\n%s", want, env)
		}
	}
	if st, _ := os.Stat(spec.SettingsPath); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("settings.json mode %v", st.Mode().Perm())
	}

	// Off for workers: a worker launched now gets no hook; the head (on) restarts with one.
	if _, err := f.SetGuardSettings(OwnerCaller, guard.Settings{Enabled: true, OffFor: []string{"Worker"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.GuardSettings(); len(got.OffFor) != 1 || got.OffFor[0] != "worker" {
		t.Fatalf("settings: %+v", got)
	}
	if _, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	w, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "w1", ProjectID: "alpha", Level: model.LevelWorker})
	if err != nil {
		t.Fatal(err)
	}
	if e.chat.Last(w.ID).Spec.SettingsPath != "" {
		t.Fatal("guard off for workers, yet the worker got the hook")
	}
	if e.chat.Last(mustAgent(t, f, "alpha-lead").ID).Spec.SettingsPath == "" {
		t.Fatal("the lead lost the hook")
	}
	if _, err := f.SetGuardSettings(OwnerCaller, guard.DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	sh, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "sh1", ProjectID: "alpha", Level: model.LevelWorker, Harness: model.HarnessShell})
	if err != nil || e.term.Last(sh.ID) == nil || e.term.Last(sh.ID).Spec.SettingsPath != "" {
		t.Fatalf("owner shell must remain available without an agent guard (%v)", err)
	}
	if _, err := f.SetGuardSettings(AgentCaller(head.ID), guard.DefaultSettings()); KindOf(err) != KindForbidden {
		t.Fatal("an agent changed the guard")
	}
}

func TestGuardHoldFlow(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	me := callerFor(t, f, e.chat, head.ID)
	e.chat.Last(head.ID).State(model.StateWorking)
	waitState(t, f, "head", model.StateWorking)

	req := HoldRequest{Tool: "Bash", ToolInput: json.RawMessage(`{"command":"sudo true"}`), Command: "sudo true",
		Rule: guard.RuleElevation, Detail: "sudo runs a command with administrator rights", Match: "sudo true"}
	if _, err := f.Hold(OwnerCaller, req); KindOf(err) != KindForbidden {
		t.Fatal("the owner created a hold")
	}
	if _, err := f.Hold(me, HoldRequest{Rule: "nope"}); KindOf(err) != KindInvalid {
		t.Fatal("unknown rule accepted")
	}
	if _, err := f.Hold(me, HoldRequest{Rule: guard.RuleKillByName}); KindOf(err) != KindInvalid {
		t.Fatal("a block rule was accepted as a hold")
	}
	ap, err := f.Hold(me, req)
	if err != nil || ap.Kind != model.ApprovalGuard || ap.State != model.ApprovalPending {
		t.Fatalf("hold: %+v %v", ap, err)
	}
	var info model.GuardHold
	if json.Unmarshal(ap.Input, &info) != nil || info.RuleName == "" || info.Why == "" || info.Command != "sudo true" {
		t.Fatalf("guard info: %s", ap.Input)
	}
	waitState(t, f, "head", model.StateWaiting)

	// Another agent cannot see or cancel it; a wait times out while pending.
	if _, err := f.WaitHold(context.Background(), AgentCaller("someone"), ap.ID, time.Millisecond); KindOf(err) != KindForbidden {
		t.Fatal("another agent waited on the hold")
	}
	got, _ := f.WaitHold(context.Background(), me, ap.ID, 20*time.Millisecond)
	if got.State != model.ApprovalPending {
		t.Fatalf("after a short wait: %s", got.State)
	}

	done := make(chan model.Approval, 1)
	go func() {
		a, _ := f.WaitHold(context.Background(), me, ap.ID, 5*time.Second)
		done <- a
	}()
	time.Sleep(20 * time.Millisecond)
	if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: "answer"}); KindOf(err) != KindInvalid {
		t.Fatal("answer accepted for a guard hold")
	}
	if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-done:
		if a.State != model.ApprovalAllowed {
			t.Fatalf("waiter saw %s", a.State)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting hook was not woken")
	}
	waitState(t, f, "head", model.StateWorking)

	// Deny with a reason; cancel by the hook; expiry when the process ends.
	ap2, _ := f.Hold(me, req)
	if a, _ := f.Decide(OwnerCaller, ap2.ID, DecideRequest{Decision: "deny", Message: "use brew"}); a.State != model.ApprovalDenied || a.Answer != "use brew" {
		t.Fatalf("deny: %+v", a)
	}
	ap3, _ := f.Hold(me, req)
	if a, _ := f.CancelHold(me, ap3.ID); a.State != model.ApprovalExpired {
		t.Fatalf("cancel: %+v", a)
	}
	if _, err := f.Decide(OwnerCaller, ap3.ID, DecideRequest{Decision: "allow"}); KindOf(err) != KindConflict {
		t.Fatal("allowed an expired hold")
	}
	ap4, _ := f.Hold(me, req)
	e.chat.Last(head.ID).Exit(0, nil)
	eventually(t, "hold expired on exit", func() bool {
		a, _ := f.ApprovalByID(ap4.ID)
		return a.State == model.ApprovalExpired
	})

	tl, _ := f.Timeline("head", 0, 100)
	var texts []string
	for _, en := range tl {
		if en.Meta["guard"] != nil {
			texts = append(texts, en.Text)
		}
	}
	joined := strings.Join(texts, " | ")
	for _, want := range []string{"The guard held this for you", "You allowed it once", "You denied it", "No answer in time"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("timeline lacks %q: %s", want, joined)
		}
	}
}

func TestGuardBlockTimelineAndRecovery(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	me := callerFor(t, f, e.chat, head.ID)
	if err := f.ReportBlock(me, HoldRequest{Tool: "Bash", Command: "pkill node", Rule: guard.RuleKillTmx, // node is a tmx runtime name: the fixed carve-out
		Detail: "pkill kills every process with that name", Match: "pkill node"}); err != nil {
		t.Fatal(err)
	}
	if err := f.ReportBlock(me, HoldRequest{Rule: guard.RuleElevation}); KindOf(err) != KindInvalid {
		t.Fatal("a hold rule was reported as a block")
	}
	tl, _ := f.Timeline("head", 0, 100)
	last := tl[len(tl)-1]
	if last.Kind != model.EntryError || !last.IsError || !strings.Contains(last.Text, "Blocked by the guard") || last.Meta["guard"] == nil {
		t.Fatalf("block row: %+v", last)
	}

	// A hold pending when the daemon stops expires on the next start.
	ap, _ := f.Hold(me, HoldRequest{Tool: "Bash", Command: "sudo true", Rule: guard.RuleElevation})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.Close(ctx)
	f2 := e.started()
	if a, _ := f2.ApprovalByID(ap.ID); a.State != model.ApprovalExpired {
		t.Fatalf("after restart: %s", a.State)
	}
}

func TestHooksDisabledWarning(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte(`{"disableAllHooks": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := hooksDisabledIn(dir); !strings.HasSuffix(got, "settings.local.json") {
		t.Fatalf("hooksDisabledIn = %q", got)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if HookCommand("/a b/it's/tmx") != `'/a b/it'\''s/tmx' hook pretooluse` {
		t.Fatal(HookCommand("/a b/it's/tmx"))
	}
}

// A Windows-parity Codex agent holds a copy of the owner's Codex login under its own agent
// directory (codex-guard/home/auth.json, refreshed by codexguard.SyncOwnerAuth at each launch)
// so Codex can resume without the owner's real CODEX_HOME. That copy must not linger for an
// agent that is not running: stopping it deletes the copy (tmx-lead, 3 Nov), and removing it
// deletes the whole agent directory regardless.
func TestStopAndRemoveAgentDeleteCodexGuardAuthCopy(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	proj, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	newAuthCopy := func(agentID string) string {
		authDir := filepath.Join(f.opts.Paths.AgentDir(agentID), "codex-guard", "home")
		if err := os.MkdirAll(authDir, 0o700); err != nil {
			t.Fatal(err)
		}
		authPath := filepath.Join(authDir, "auth.json")
		if err := os.WriteFile(authPath, []byte(`{"owner":"fixture login"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return authPath
	}

	w, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "w1", ProjectID: proj.ID, Level: model.LevelWorker})
	if err != nil {
		t.Fatal(err)
	}
	stopAuthPath := newAuthCopy(w.ID)
	if _, err := f.StopAgent(OwnerCaller, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stopAuthPath); !os.IsNotExist(err) {
		t.Fatalf("codex-guard auth copy survived stop: %v", err)
	}

	w2, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "w2", ProjectID: proj.ID, Level: model.LevelWorker})
	if err != nil {
		t.Fatal(err)
	}
	removeAuthPath := newAuthCopy(w2.ID)
	if err := f.RemoveAgent(OwnerCaller, w2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(removeAuthPath); !os.IsNotExist(err) {
		t.Fatalf("codex-guard auth copy survived agent removal: %v", err)
	}
}

// A Claude agent whose folder sets disableAllHooks must be refused at launch (it would run with
// no guard at all, not merely a warning in its timeline).
func TestHooksDisabledRefusesLaunch(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	proj, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj.Folder, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj.Folder, ".claude", "settings.local.json"),
		[]byte(`{"disableAllHooks": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "w1", ProjectID: "alpha", Level: model.LevelWorker})
	if err == nil || !strings.Contains(err.Error(), "the command guard is disabled in") {
		t.Fatalf("launch error: %v", err)
	}
}

// The agents' own CLAUDE_CONFIG_DIR (<state>/claude-config) counts too: Claude reads its user
// settings there, not in ~/.claude (o4, 3 Nov).
func TestHooksDisabledInClaudeConfigRefusesLaunch(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	if _, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	dir := f.opts.Paths.ClaudeConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(cfg, []byte(`{"disableAllHooks": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "w1", ProjectID: "alpha", Level: model.LevelWorker})
	if err == nil || !strings.Contains(err.Error(), "claude-config") {
		t.Fatalf("launch with hooks off in claude-config: %v", err)
	}
	if err := os.WriteFile(cfg, []byte(`{"model": "opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "w2", ProjectID: "alpha", Level: model.LevelWorker}); err != nil {
		t.Fatalf("launch with hooks on: %v", err)
	}
}
