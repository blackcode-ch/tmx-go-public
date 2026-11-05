package fleet

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// TypingQuiet: mail is typed into a terminal agent only if no human typed into it for this
// long (they may be in the middle of a sentence).
const TypingQuiet = 20 * time.Second

// harnessModeLocked resolves a launch's harness and mode: the harness must be known, run in
// that mode, and this daemon must have a runner that takes it.
func (f *Fleet) harnessModeLocked(h model.Harness, m model.Mode) (model.Harness, model.Mode, error) {
	if h == "" {
		h = model.HarnessClaude
	}
	info, ok := harness.Get(h)
	if !ok {
		return "", "", errf(KindInvalid, "unknown harness %q", h)
	}
	if m == "" {
		m = info.DefaultMode()
	}
	if m != model.ModeChat && m != model.ModeTerminal {
		return "", "", errf(KindInvalid, "unknown mode %q", m)
	}
	if !info.Supports(m) {
		if h == model.HarnessShell {
			return "", "", errf(KindInvalid, "a shell runs only in terminal mode")
		}
		return "", "", errf(KindInvalid, "%s runs only in %s mode", info.Label, info.DefaultMode())
	}
	run, ok := f.opts.Runners[m]
	if !ok {
		return "", "", errf(KindInvalid, "this daemon has no runner for %s mode", m)
	}
	if s, ok := run.(runner.Supporter); ok && !s.Supports(h) {
		return "", "", errf(KindInvalid, "this daemon cannot run %s in %s mode", info.Label, m)
	}
	return h, m, nil
}

// pickHarnessModelLocked: Claude's model is the profile's (pickModel). Another harness uses
// its own default ("") unless a model is asked for: the owner may name any (a profile's
// models are Claude's), an agent only one the profile allows. A shell has none.
func (f *Fleet) pickHarnessModelLocked(h model.Harness, prof model.Profile, requested string, byOwner bool) (string, error) {
	switch h {
	case model.HarnessClaude:
		return pickModel(prof, requested)
	case model.HarnessShell:
		return "", nil
	}
	if requested == "" || byOwner {
		return requested, nil
	}
	if !slices.Contains(prof.AllowedModels, requested) {
		return "", errf(KindForbidden, "model %s is not allowed for the %s profile (allowed: %s; or leave it out for the harness's default)",
			requested, prof.Slug, strings.Join(prof.AllowedModels, ", "))
	}
	return requested, nil
}

// harnessProfileOK refuses a profile the harness cannot honour: a read-only profile on pi,
// which has no permission controls at all (Codex runs it in its read-only sandbox, or on
// Windows unsandboxed with untrusted approvals; opencode with edit and bash denied).
func harnessProfileOK(h model.Harness, prof model.Profile) error {
	if prof.ReadOnly && (h == model.HarnessPi || h == "agy" || h == "antigravity") {
		return errf(KindInvalid, "the %s profile is read-only and %s cannot be limited to reading; pick another harness", prof.Slug, h)
	}
	return nil
}

// harnessSpecLocked completes the spec of an agent on a harness other than Claude. The
// profile gives the permission mode (a read-only profile runs as "plan": Codex's read-only
// sandbox on macOS and Linux, untrusted approvals on Windows where Codex has no sandbox;
// opencode with edits and shell denied) and the instructions; its Claude tool lists and the
// guard's hook do not apply (see DESIGN.md "Harnesses").
func (f *Fleet) harnessSpecLocked(r *agentRec, spec runner.Spec) runner.Spec {
	a := r.a
	if a.Harness == model.HarnessShell {
		return spec
	}
	prof := f.profileForLocked(a)
	spec.PermissionMode = prof.PermissionMode
	if prof.ReadOnly {
		spec.PermissionMode = "plan"
	}
	spec.SystemPrompt = f.systemPromptLocked(a)
	spec.SessionID = a.SessionID
	switch a.Harness {
	case "agy", "antigravity":
		spec.AgyFullAccess = a.AgyFullAccess && !prof.ReadOnly
	case model.HarnessCodex:
		spec.CodexFullAccess = a.CodexFullAccess && !prof.ReadOnly
		spec.MCP = &runner.MCPServer{Command: f.opts.Executable, Args: []string{"mcp"}, EnvKeys: []string{EnvURL, EnvAgent, EnvToken}}
	case model.HarnessOpenCode:
		// opencode's TUI has no --model flag and no id of its own to report back, so tmx
		// assigns the session up front (opencode creates it on launch) and resumes it by id.
		if r.a.SessionID == "" {
			r.a.SessionID = harness.NewOpenCodeSession()
		}
		spec.SessionID = r.a.SessionID
		spec.MCP = &runner.MCPServer{Command: f.opts.Executable, Args: []string{"mcp"}, EnvKeys: []string{EnvURL, EnvAgent, EnvToken}}
	case model.HarnessPi: // pi takes the id of a new conversation up front, like Claude
		if r.a.SessionID == "" {
			r.a.SessionID = harness.NewPiSession()
		}
		spec.SessionID = r.a.SessionID
	}
	return spec
}

// instructionsEvery is how often a harness without a system prompt is reminded of tmx's
// instructions (with its next prompt or mail) while it works.
const instructionsEvery = 4 * time.Hour

// withInstructions puts tmx's instructions in front of a prompt for a harness that takes none
// at launch (opencode, pi): a new conversation's first message, the first after a resume, and
// one every instructionsEvery.
func (f *Fleet) withInstructions(id, prompt string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.agents[id]
	if !ok {
		return prompt
	}
	return f.instructionsLocked(r) + prompt
}

// instructionsLocked is the "[tmx instructions]" block to put before what is sent to r now,
// or "" (a harness with a system prompt, or not due yet); giving it clears the due flag.
func (f *Fleet) instructionsLocked(r *agentRec) string {
	if r.a.Harness == model.HarnessShell {
		return ""
	}
	if info, ok := harness.Get(r.a.Harness); !ok || info.SystemPrompt {
		return ""
	}
	if r.sessionConfirmed && !r.instructionsDue && time.Since(r.instructionsAt) < instructionsEvery {
		return ""
	}
	r.instructionsDue, r.instructionsAt = false, time.Now()
	return "[tmx instructions]\n" + f.systemPromptLocked(r.a) + "\n\n"
}

// terminalMailLocked reports whether queued mail may be typed into a terminal agent now:
// its harness takes typed mail (never a shell), it is idle, and no human typed into it in
// the last TypingQuiet. When only recent typing is in the way, a retry is scheduled.
func (f *Fleet) terminalMailLocked(r *agentRec) bool {
	info, ok := harness.Get(r.a.Harness)
	if !ok || !info.TypedMail || r.a.State != model.StateIdle {
		return false
	}
	if wait := TypingQuiet - time.Since(r.humanInputAt); wait > 0 {
		if !r.retryMail {
			r.retryMail = true
			id := r.a.ID
			time.AfterFunc(wait+50*time.Millisecond, func() {
				f.mu.Lock()
				if rr, ok := f.agents[id]; ok {
					rr.retryMail = false
				}
				closed := f.closed
				f.mu.Unlock()
				if !closed {
					f.tryDeliver(id)
				}
			})
		}
		return false
	}
	return true
}

// typedTerminal: owner mail to a harness TUI waits for its input box like any mail (typing
// into a menu would answer it); a shell still gets the owner's text at once.
func typedTerminal(a model.Agent) bool {
	return a.Mode == model.ModeTerminal && a.Harness != model.HarnessShell
}

// Harnesses lists the harnesses and whether each is installed here (GET /api/harnesses).
func (f *Fleet) Harnesses() []harness.Status {
	out := harness.Statuses()
	for i := range out {
		if out[i].Name == model.HarnessClaude && f.opts.ClaudeCommand != "" {
			out[i].Installed, out[i].Path = true, f.opts.ClaudeCommand
		}
	}
	return out
}

// claudeConversationExists: Claude has a transcript of the agent's conversation (resuming
// it with --session-id would fail: "Session ID … is already in use"). root is the agents'
// CLAUDE_CONFIG_DIR.
func claudeConversationExists(root string, a model.Agent) bool {
	return harness.ClaudeTranscriptExistsIn(root, a.Cwd, a.SessionID)
}

// carryClaudeTranscript copies an agent's conversation that a daemon before the isolated
// CLAUDE_CONFIG_DIR left in ~/.claude/projects into root, so it can still be resumed. Copied,
// not moved: the owner's folder is left as it was.
func carryClaudeTranscript(root string, a model.Agent) {
	if a.SessionID == "" || claudeConversationExists(root, a) {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil || !harness.ClaudeTranscriptExists(home, a.Cwd, a.SessionID) {
		return
	}
	b, err := os.ReadFile(filepath.Join(harness.ClaudeProjectDir(home, a.Cwd), a.SessionID+".jsonl"))
	if err != nil {
		return
	}
	dst := harness.ClaudeProjectDirIn(root, a.Cwd)
	if os.MkdirAll(dst, 0o700) == nil {
		_ = config.WriteFileAtomic(filepath.Join(dst, a.SessionID+".jsonl"), b, 0o600)
	}
}
