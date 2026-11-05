package fleet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/codexguard"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
)

const (
	stopGrace   = 10 * time.Second // runner.Stop: graceful first, killed after this
	stopWaitMax = 15 * time.Second // then wait at most this for the event loop to end
)

// Agent env names (DESIGN.md "MCP tools").
const (
	EnvURL   = "TMXG_URL"
	EnvAgent = "TMXG_AGENT_ID"
	EnvToken = "TMXG_AGENT_TOKEN"
)

// StartAgent starts a stopped agent (desired becomes running).
func (f *Fleet) StartAgent(c Caller, idOrName string) (model.Agent, error) {
	id, err := f.authorizeAct(c, idOrName)
	if err != nil {
		return model.Agent{}, err
	}
	if err := f.startAgentOpts(id, "", startOpts{deliberate: true}); err != nil {
		return model.Agent{}, err
	}
	return f.Agent(id)
}

// StopAgent stops an agent (desired becomes stopped; resumable).
func (f *Fleet) StopAgent(c Caller, idOrName string) (model.Agent, error) {
	id, err := f.authorizeAct(c, idOrName)
	if err != nil {
		return model.Agent{}, err
	}
	f.stopProcess(f.ctx, id, false)
	return f.Agent(id)
}

// RestartOptions modify a restart (POST /api/agents/{id}/restart).
type RestartOptions struct {
	// Fresh starts a brand-new conversation (clean context) instead of resuming the old one.
	Fresh bool `json:"fresh,omitempty"`
	// Prompt is the fresh conversation's first instruction (a baton pass). Ignored unless Fresh.
	Prompt string `json:"prompt,omitempty"`
}

// RestartAgent stops the agent's process and starts it again on the same session.
func (f *Fleet) RestartAgent(c Caller, idOrName string) (model.Agent, error) {
	return f.RestartAgentWith(c, idOrName, RestartOptions{})
}

// RestartAgentWith is RestartAgent with options: Fresh starts a new conversation (clean
// context) instead of resuming, optionally handing off with Prompt as its first instruction.
func (f *Fleet) RestartAgentWith(c Caller, idOrName string, o RestartOptions) (model.Agent, error) {
	id, err := f.authorizeAct(c, idOrName)
	if err != nil {
		return model.Agent{}, err
	}
	keep := f.holdsCapacity(id) // a running agent keeps its place
	f.mu.Lock()
	if r := f.agents[id]; r != nil {
		err = f.launchAdmissionLocked(r.a)
	} else {
		err = errf(KindNotFound, "no agent %q", id)
	}
	f.mu.Unlock()
	if err != nil {
		return model.Agent{}, err
	}
	f.stopProcess(f.ctx, id, true)
	prompt := ""
	if o.Fresh {
		if err := f.freshenSessionLocked(id); err != nil {
			return model.Agent{}, err
		}
		prompt = o.Prompt
	}
	if err := f.startAgentOpts(id, prompt, startOpts{StartOptions: StartOptions{Force: keep}, deliberate: true}); err != nil {
		return model.Agent{}, err
	}
	return f.Agent(id)
}

// freshenSessionLocked clears an agent's session identity so its next start begins a brand-new
// conversation (clean context) instead of resuming the old one, as migrateHeadHarnessLocked does
// across a harness change.
func (f *Fleet) freshenSessionLocked(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.agents[id]
	if !ok {
		return errf(KindNotFound, "no agent %q", id)
	}
	next := r.a
	next.SessionID = ""
	next.UpdatedAt = model.Now()
	if err := f.st.PutAgent(next); err != nil {
		return err
	}
	r.a = next
	r.sessionConfirmed = false
	f.bus.Publish(model.BusEvent{Type: model.EvAgentUpdated, AgentID: next.ID, ProjectID: next.ProjectID, Data: next})
	return nil
}

// InterruptAgent ends the agent's current turn without stopping the process.
func (f *Fleet) InterruptAgent(c Caller, idOrName string) (model.Agent, error) {
	id, err := f.authorizeAct(c, idOrName)
	if err != nil {
		return model.Agent{}, err
	}
	f.mu.Lock()
	proc := f.agents[id].proc
	f.mu.Unlock()
	if proc == nil {
		return model.Agent{}, errf(KindConflict, "the agent is not running")
	}
	if err := proc.Interrupt(); err != nil {
		return model.Agent{}, err
	}
	return f.Agent(id)
}

// RemoveAgent stops and deletes an agent and everything below it (owner only). The head
// cannot be removed.
func (f *Fleet) RemoveAgent(c Caller, idOrName string) error {
	if !c.Owner {
		return errf(KindForbidden, "only the owner can remove agents")
	}
	f.mu.Lock()
	r, err := f.resolveLocked(idOrName)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	if r.a.Level == model.LevelHead {
		f.mu.Unlock()
		return errf(KindForbidden, "the head cannot be removed; stop it instead")
	}
	ids := f.subtreeLocked(r.a.ID)
	f.mu.Unlock()
	for i := len(ids) - 1; i >= 0; i-- { // leaves first
		f.removeOne(ids[i])
	}
	return nil
}

// subtreeLocked returns id and all its descendants, parents before children.
func (f *Fleet) subtreeLocked(id string) []string {
	out := []string{id}
	for i := 0; i < len(out); i++ {
		for _, r := range f.agentsLocked() {
			if r.ParentID == out[i] {
				out = append(out, r.ID)
			}
		}
	}
	return out
}

func (f *Fleet) removeOne(id string) {
	f.stopProcess(f.ctx, id, false)
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.agents[id]
	if !ok {
		return
	}
	delete(f.agents, id)
	if r.token != "" {
		delete(f.tokens, r.token)
	}
	for aid, pr := range f.perms {
		if pr.agentID == id {
			delete(f.perms, aid)
		}
	}
	for _, p := range f.projects {
		if p.LeadID == id {
			p.LeadID = ""
			_ = f.saveProjectLocked(p)
		}
	}
	if err := f.st.DeleteAgent(id); err != nil {
		f.log.Error("store delete agent", "agent", r.a.Name, "err", err)
	}
	_ = os.RemoveAll(f.opts.Paths.AgentDir(id))
	_ = os.RemoveAll(filepath.Join(f.opts.Paths.UploadsDir(), id))
	f.closeTermsLocked(r)
	f.bus.Publish(model.BusEvent{Type: model.EvAgentRemoved, AgentID: id, ProjectID: r.a.ProjectID,
		Data: map[string]string{"id": id, "name": r.a.Name}})
}

func (f *Fleet) authorizeAct(c Caller, idOrName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.resolveLocked(idOrName)
	if err != nil {
		return "", err
	}
	if err := f.authorizeActLocked(c, r); err != nil {
		return "", err
	}
	return r.a.ID, nil
}

// startAgent starts the agent's process if it is not running, resuming its session, then
// gives it prompt (if any) and its queued mail. Over the machine's limits it is queued.
func (f *Fleet) startAgent(id, prompt string) error { return f.startAgentOpts(id, prompt, startOpts{}) }

func (f *Fleet) startAgentOpts(id, prompt string, o startOpts) error {
	f.mu.Lock()
	r, ok := f.agents[id]
	if !ok {
		f.mu.Unlock()
		return errf(KindNotFound, "no agent %q", id)
	}
	if f.closed {
		f.mu.Unlock()
		return errf(KindConflict, "the daemon is shutting down")
	}
	if o.recovery != nil {
		if r != o.recovery || !r.recovery.pending || r.recovery.generation != o.recoveryGeneration || r.a.Desired != "running" || r.proc != nil || r.starting {
			f.mu.Unlock()
			return nil
		}
		r.recovery.pending = false
		r.recovery.attempts = append(r.recovery.attempts, time.Now())
	}
	if r.proc != nil || r.starting {
		f.mu.Unlock()
		return nil
	}
	if r.hostStopping {
		f.mu.Unlock()
		return errf(KindConflict, "its session host is being stopped; start it again once that is done")
	}
	if err := f.launchAdmissionLocked(r.a); err != nil {
		f.mu.Unlock()
		return err
	}
	if o.reattach || r.hostUnknown {
		if ok, err := f.reattachLocked(r); ok || err != nil {
			return err // f.mu released
		}
	}
	wasShelved, wasDone := r.a.ShelvedAt, r.a.DoneAt
	if o.deliberate {
		r.recovery.cancel(true)
		r.a.ShelvedAt, r.a.DoneAt = 0, 0
	}
	if queued, err := f.admitLocked(r, o, prompt); queued || err != nil {
		if err != nil {
			r.a.ShelvedAt, r.a.DoneAt = wasShelved, wasDone
		}
		f.mu.Unlock()
		return err
	}
	run, ok := f.opts.Runners[r.a.Mode]
	if !ok {
		r.a.State, r.a.Error = model.StateError, fmt.Sprintf("no runner for %s mode", r.a.Mode)
		f.saveAgentLocked(r)
		f.mu.Unlock()
		return errf(KindInvalid, "%s", r.a.Error)
	}
	if r.token != "" {
		delete(f.tokens, r.token)
	}
	r.token = config.NewToken()
	f.tokens[r.token] = id
	spec, err := f.specLocked(r)
	if err != nil {
		r.a.State, r.a.Error = model.StateError, err.Error()
		f.saveAgentLocked(r)
		f.mu.Unlock()
		return err
	}
	spec.LowPriority = f.lowPriorityLocked()
	if r.a.Harness == "agy" || r.a.Harness == "antigravity" {
		spec.InitialPrompt = "[tmx instructions]\n" + f.systemPromptLocked(r.a) + "\n\n" + prompt
	}
	r.starting, r.stopping = true, false
	r.a.Desired, r.a.State, r.a.Error = "running", model.StateStarting, ""
	f.saveAgentLocked(r)
	f.mu.Unlock()

	proc, err := run.Start(f.ctx, spec)

	f.mu.Lock()
	r.starting = false
	if f.agents[id] != r { // removed while starting
		f.mu.Unlock()
		if proc != nil {
			f.drainAndStop(proc)
		}
		return errf(KindNotFound, "the agent was removed while starting")
	}
	if err != nil {
		r.a.State, r.a.Error = model.StateError, "start failed: "+err.Error()
		f.appendLocked(r, model.TimelineEntry{Kind: model.EntryError, Text: r.a.Error, IsError: true})
		f.saveAgentLocked(r)
		f.mu.Unlock()
		return errf(KindInternal, "%s", r.a.Error)
	}
	r.proc, r.done = proc, make(chan struct{})
	r.instructionsDue = r.sessionConfirmed // a resumed conversation: see instructionsLocked
	r.a.PID = proc.PID()
	f.saveAgentLocked(r)
	done := r.done
	stopNow := r.a.Desired == "stopped" // a stop came in while starting
	f.goTracked(func() { f.consume(r, proc, done) })
	f.mu.Unlock()

	if stopNow {
		f.stopProcess(f.ctx, id, false)
		return nil
	}
	if spec.InitialPrompt != "" {
		f.mu.Lock()
		f.appendLocked(r, model.TimelineEntry{Kind: model.EntryUser, Text: spec.InitialPrompt})
		f.mu.Unlock()
	} else if prompt != "" {
		f.sendTurn(id, f.withInstructions(id, prompt), model.EntryUser, nil)
	}
	f.tryDeliver(id)
	return nil
}

// stopProcess stops the agent's process and waits for its event loop. keepDesired=true is
// used by restart and daemon shutdown (desired stays running).
func (f *Fleet) stopProcess(ctx context.Context, id string, keepDesired bool) {
	f.stopProcessIf(ctx, id, keepDesired, nil)
}

// check runs under the same lock as stop admission: fresh activity, protection,
// or a new runner cannot race an automatic shelving decision.
func (f *Fleet) stopProcessIf(ctx context.Context, id string, keepDesired bool, check func(*agentRec) bool) {
	f.mu.Lock()
	r, ok := f.agents[id]
	if !ok || (check != nil && !check(r)) {
		f.mu.Unlock()
		return
	}
	r.recovery.cancel(false)
	if !keepDesired {
		r.a.Desired = "stopped"
	}
	f.beforeStopLocked(r)
	// Serialize ownership transfers with stop. This also covers already-exited agents.
	f.processes.StopOwner(id)
	proc, done := r.proc, r.done
	if proc == nil {
		if r.hostUnknown && !keepDesired {
			// Its host may still run the agent: the owner's stop ends it there (hosting.go).
			// No start meanwhile (hostStopping): it could make a second copy.
			r.hostUnknown, r.hostStopping = false, true
			f.saveAgentLocked(r)
			f.mu.Unlock()
			f.stopUnknownHost(id)
			return
		}
		if !r.starting && r.a.State != model.StateStopped && r.a.State != model.StateError {
			r.a.State = model.StateStopped
		}
		f.saveAgentLocked(r)
		f.mu.Unlock()
		if !keepDesired {
			f.deleteCodexGuardAuth(id)
		}
		return
	}
	r.stopping = true
	f.saveAgentLocked(r)
	f.mu.Unlock()

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace)
	defer cancel()
	if err := proc.Stop(sctx); err != nil {
		f.log.Warn("stop agent", "agent", id, "err", err)
	}
	select {
	case <-done:
	case <-time.After(stopWaitMax):
		f.log.Warn("agent event loop did not end after stop", "agent", id)
	}
	if !keepDesired {
		f.deleteCodexGuardAuth(id)
	}
}

// deleteCodexGuardAuth removes a stopped (or soon-to-be-removed) agent's isolated copy of the
// owner's Codex login, so a credential copy never lingers for an agent that is not running.
// SyncOwnerAuth makes a fresh one at the next launch or resume. A no-op for agents that never
// had Windows Codex parity set up.
func (f *Fleet) deleteCodexGuardAuth(id string) {
	home := filepath.Join(f.opts.Paths.AgentDir(id), "codex-guard", "home")
	if err := codexguard.DeleteOwnerAuth(home); err != nil {
		f.log.Warn("delete codex-guard auth copy", "agent", id, "err", err)
	}
}

// drainAndStop stops a process nobody tracks (its agent vanished) and drains its events.
func (f *Fleet) drainAndStop(proc runner.Process) {
	go func() {
		for range proc.Events() {
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), stopGrace)
	defer cancel()
	_ = proc.Stop(ctx)
}

// specLocked builds the runner.Spec and writes the agent's mcp.json.
func (f *Fleet) specLocked(r *agentRec) (runner.Spec, error) {
	a := r.a
	if err := f.launchAdmissionLocked(a); err != nil {
		return runner.Spec{}, err
	}
	if err := os.MkdirAll(a.Cwd, 0o755); err != nil {
		return runner.Spec{}, fmt.Errorf("working folder %s: %w", a.Cwd, err)
	}
	if err := f.launchAdmissionLocked(a); err != nil {
		return runner.Spec{}, err
	}
	env := []string{EnvURL + "=" + f.opts.URL, EnvAgent + "=" + a.ID, EnvToken + "=" + r.token}
	spec := runner.Spec{
		AgentID: a.ID, Name: a.Name, Harness: a.Harness, Mode: a.Mode, Model: a.Model,
		Cwd: a.Cwd, Env: env,
	}
	if f.hostedLocked(a) {
		spec.HostDir, spec.HostExe, spec.HostToken = f.hostDir(a.ID), f.opts.HostExecutable, r.token
	}
	if a.Harness != model.HarnessClaude {
		spec.Env = append(spec.Env, f.accountEnvLocked(a)...)
		spec = f.harnessSpecLocked(r, spec)
		if err := f.guardSpecLocked(r, &spec); err != nil {
			return runner.Spec{}, err
		}
		return spec, nil
	}
	// A conversation that exists is resumed (--resume); a new one gets its id up front
	// (--session-id), stored at once so the agent's session is known before its first turn.
	// Both modes: the TUI takes the same flags, and its runner confirms the transcript.
	// Claude's own state (.claude.json, sign-in, transcripts) lives in the daemon's folder,
	// never the owner's shared ~/.claude.json; the runners refuse a claude without it.
	cfgDir, err := f.claudeConfigDirLocked(a)
	if err != nil {
		return runner.Spec{}, err
	}
	carryClaudeTranscript(cfgDir, a)
	if !r.sessionConfirmed && a.SessionID != "" && claudeConversationExists(cfgDir, a) {
		r.sessionConfirmed = true // e.g. a terminal agent, whose log has no turns
	}
	if r.sessionConfirmed && a.SessionID != "" {
		spec.SessionID = a.SessionID
	} else {
		if a.SessionID == "" {
			r.a.SessionID = newUUID()
		}
		spec.ExtraArgs = append(spec.ExtraArgs, "--session-id", r.a.SessionID)
	}
	spec.Command = f.opts.ClaudeCommand
	prof := f.profileForLocked(a)
	spec.PermissionMode = prof.PermissionMode
	spec.AllowedTools = append([]string{"mcp__tmxg"}, prof.AllowedTools...)
	spec.DisallowedTools = slices.Clone(prof.DisallowedTools)
	if prof.ReadOnly {
		for _, t := range model.ReadOnlyDenied {
			if !slices.Contains(spec.DisallowedTools, t) {
				spec.DisallowedTools = append(spec.DisallowedTools, t)
			}
		}
	}
	spec.SystemPrompt = f.systemPromptLocked(a)
	// Files the owner attaches land in uploads/<id>; claude may read them without asking.
	if up := filepath.Join(f.opts.Paths.UploadsDir(), a.ID); os.MkdirAll(up, 0o700) == nil {
		spec.AddDirs = append(spec.AddDirs, up)
	}
	path, err := f.writeMCPConfig(a.ID, env)
	if err != nil {
		return runner.Spec{}, err
	}
	spec.MCPConfigPath = path
	// Not in env above: that also goes into mcp.json for the tmx MCP server.
	spec.Env = append(spec.Env, claude.EnvConfigDir+"="+cfgDir)
	// A start waiting in claude's start queue (e.g. behind a sign-in, up to its 10 minutes)
	// shows why instead of looking hung; run.Start calls this without the fleet's mutex held.
	spec.OnStartWait = func(reason string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.agents[r.a.ID] == r && r.a.State == model.StateStarting && r.a.QueueReason != reason {
			r.a.QueueReason = reason
			f.saveAgentLocked(r)
		}
	}
	if err := f.guardSpecLocked(r, &spec); err != nil {
		return runner.Spec{}, err
	}
	return spec, nil
}

// newUUID returns a random (version 4) UUID, the form claude's --session-id expects.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// writeMCPConfig writes agents/<id>/mcp.json: the tmx binary itself as stdio server "tmx".
// The file holds the agent's token, so it is a secret file (private to the user), and is
// rewritten (new token) on every start.
func (f *Fleet) writeMCPConfig(id string, env []string) (string, error) {
	envMap := map[string]string{}
	for _, kv := range env {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				envMap[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	cfg := map[string]any{"mcpServers": map[string]any{
		"tmxg": map[string]any{"type": "stdio", "command": f.opts.Executable, "args": []string{"mcp"}, "env": envMap},
	}}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	dir := f.opts.Paths.AgentDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "mcp.json")
	return path, config.WriteSecretFile(path, b)
}
