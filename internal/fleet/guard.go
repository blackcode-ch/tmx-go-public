package fleet

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// The dangerous-command guard's daemon side (DESIGN.md "Safety"): chat-mode Claude agents get
// a PreToolUse hook (`tmx hook pretooluse`) through --settings; what it holds becomes an
// Approval of kind guard that the hook waits on, what it blocks becomes an error row in the
// agent's timeline.

// HoldRequest is POST /api/guard/holds and POST /api/guard/blocks (from the agent's hook).
type HoldRequest struct {
	Tool      string          `json:"tool"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	Command   string          `json:"command"`
	Rule      string          `json:"rule"`
	Detail    string          `json:"detail,omitempty"`
	Match     string          `json:"match,omitempty"`
	Cwd       string          `json:"cwd,omitempty"`
}

// hookTimeout is Claude's limit for one hook run: longer than the hook waits for the owner.
const hookTimeout = guard.HoldWait + 5*time.Minute

type safetyState struct {
	settings guard.Settings
	holds    map[string]chan struct{} // pending guard approval ID -> closed when it is resolved
}

func (f *Fleet) safetyLocked() *safetyState {
	if f.safety == nil {
		s := guard.DefaultSettings()
		if f.opts.Guard != nil {
			s = *f.opts.Guard
		}
		f.safety = &safetyState{settings: s, holds: map[string]chan struct{}{}}
	}
	return f.safety
}

// GuardSettings returns the guard's on/off settings.
func (f *Fleet) GuardSettings() guard.Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.safetyLocked().settings
	s.OffFor = slices.Clone(s.OffFor)
	if s.OffFor == nil {
		s.OffFor = []string{}
	}
	s.AlwaysAllow = slices.Clone(s.AlwaysAllow)
	s.RuleStates = maps.Clone(s.RuleStates)
	s.RuleLog = slices.Clone(s.RuleLog)
	return s
}

// SetGuardSettings changes them (owner only; the API asks the owner gate first when the change
// protects fewer agents). Running agents keep their hook until they restart.
func (f *Fleet) SetGuardSettings(c Caller, s guard.Settings) (guard.Settings, error) {
	if !c.Owner {
		return guard.Settings{}, errf(KindForbidden, "only the owner changes the guard")
	}
	off := []string{}
	for _, k := range s.OffFor {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" || len(k) > 64 {
			return guard.Settings{}, errf(KindInvalid, "%q is not a kind of agent", k)
		}
		if !slices.Contains(off, k) {
			off = append(off, k)
		}
	}
	s.OffFor = off
	f.mu.Lock()
	// The standing allows change only through Allow always and RevokeAlways (both owner-gated).
	cur := f.safetyLocked().settings
	s.AlwaysAllow = cur.AlwaysAllow
	s.RuleStates, s.RuleLog, s.DefaultsVersion = cur.RuleStates, cur.RuleLog, cur.DefaultsVersion // only SetRuleState
	s.AllowedBefore = cur.AllowedBefore
	err := f.saveGuardLocked(s)
	f.mu.Unlock()
	if err != nil {
		return guard.Settings{}, err
	}
	f.log.Info("guard settings changed", "enabled", s.Enabled, "off_for", s.OffFor)
	return f.GuardSettings(), nil
}

func (f *Fleet) saveGuardLocked(s guard.Settings) error {
	if f.opts.SaveGuard != nil {
		if err := f.opts.SaveGuard(s); err != nil {
			return err
		}
	}
	f.safetyLocked().settings = s
	return nil
}

// alwaysKey is the kind of agent a standing allow is remembered for: its profile, or its
// level when it has none.
func alwaysKey(a *model.Agent) string {
	if a.Profile != "" {
		return a.Profile
	}
	return "level:" + string(a.Level)
}

// RevokeAlways removes one standing allow (owner only; the API has asked the owner gate).
func (f *Fleet) RevokeAlways(c Caller, id string) (guard.Settings, error) {
	if !c.Owner {
		return guard.Settings{}, errf(KindForbidden, "only the owner revokes an Allow always")
	}
	f.mu.Lock()
	s := f.safetyLocked().settings
	i := slices.IndexFunc(s.AlwaysAllow, func(a guard.AlwaysAllow) bool { return a.ID == id })
	if i < 0 {
		f.mu.Unlock()
		return guard.Settings{}, errf(KindNotFound, "no Allow always %q", id)
	}
	gone := s.AlwaysAllow[i]
	s.AlwaysAllow = slices.Delete(slices.Clone(s.AlwaysAllow), i, i+1)
	err := f.saveGuardLocked(s)
	f.mu.Unlock()
	if err != nil {
		return guard.Settings{}, err
	}
	f.log.Info("guard allow-always revoked", "rule", gone.Rule, "profile", gone.Profile, "command", gone.Command)
	return f.GuardSettings(), nil
}

// ApprovalByID returns one approval.
func (f *Fleet) ApprovalByID(id string) (model.Approval, error) {
	ap, err := f.st.GetApproval(id)
	if err != nil {
		return model.Approval{}, errf(KindNotFound, "no approval %q", id)
	}
	return ap, nil
}

func (f *Fleet) guardInfo(req HoldRequest, outcome guard.Outcome) (model.GuardHold, error) {
	r, ok := guard.RuleByID(req.Rule)
	if !ok || f.ruleStateOf(req.Rule) != outcome {
		return model.GuardHold{}, errf(KindInvalid, "%q is not a %s rule of the guard now", req.Rule, outcome)
	}
	if len(req.Command) > MaxBodyBytes || len(req.ToolInput) > 4*MaxBodyBytes {
		return model.GuardHold{}, errf(KindInvalid, "the held call is too large")
	}
	in := req.ToolInput
	if len(in) > 0 && !json.Valid(in) {
		in = nil
	}
	return model.GuardHold{Outcome: string(outcome), Rule: r.ID, RuleName: r.Name, Why: r.Why,
		Detail: req.Detail, Match: req.Match, Command: req.Command, Tool: req.Tool, ToolInput: in, Cwd: req.Cwd}, nil
}

// Hold records a tool call the guard held (agent only): a pending Approval of kind guard in
// Needs you, and the agent waits for it.
func (f *Fleet) Hold(c Caller, req HoldRequest) (model.Approval, error) {
	if req.Rule == guard.RuleP2PInvite || req.Rule == guard.RuleP2PJoin {
		return model.Approval{}, errf(KindForbidden, "use the dedicated pairing tools")
	}
	return f.hold(c, req)
}

func (f *Fleet) hold(c Caller, req HoldRequest) (model.Approval, error) {
	if c.Owner {
		return model.Approval{}, errf(KindForbidden, "only an agent's guard hook holds calls")
	}
	info, err := f.guardInfo(req, guard.Hold)
	if err != nil {
		return model.Approval{}, err
	}
	raw, _ := json.Marshal(info)
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.callerRecLocked(c)
	if err != nil {
		return model.Approval{}, err
	}
	if r == nil { // a peer: only an agent's own guard hook holds calls
		return model.Approval{}, errf(KindForbidden, "only an agent's guard hook holds calls")
	}
	key := alwaysKey(&r.a)
	for _, al := range f.safetyLocked().settings.AlwaysAllow {
		if !al.Covers(info.Rule, info.Tool, info.Command, info.Cwd, key) {
			continue
		}
		// The owner answered this exact call "Allow always": it runs without a card.
		ap := model.Approval{ID: model.NewID(), AgentID: r.a.ID, Kind: model.ApprovalGuard, Tool: req.Tool,
			Input: raw, Description: info.Detail, Options: []string{}, State: model.ApprovalAllowed,
			Answer: AnswerAlways, CreatedAt: model.Now()}
		if err := f.st.PutApproval(ap); err != nil {
			return model.Approval{}, err
		}
		f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Tool: req.Tool,
			Text: "Allowed by your Allow always: " + info.RuleName,
			Meta: map[string]any{"guard": info, "approval_id": ap.ID, "decision": string(model.ApprovalAllowed), "always_id": al.ID}})
		return ap, nil
	}
	if at, ok := f.allowedAtLocked(guard.AllowedKey(info.Rule, info.Tool, info.Command, info.Cwd, key)); ok && guard.Rememberable[info.Rule] {
		// "The same thing never asks twice" (the owner, 3 Nov 22:30): allowed once, allowed again.
		ap := model.Approval{ID: model.NewID(), AgentID: r.a.ID, Kind: model.ApprovalGuard, Tool: req.Tool,
			Input: raw, Description: info.Detail, Options: []string{}, State: model.ApprovalAllowed,
			Answer: AnswerSame, CreatedAt: model.Now()}
		if err := f.st.PutApproval(ap); err != nil {
			return model.Approval{}, err
		}
		f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Tool: req.Tool,
			Text: "Allowed: same as approved " + dayOf(at) + ": " + info.RuleName,
			Meta: map[string]any{"guard": info, "approval_id": ap.ID, "decision": string(model.ApprovalAllowed), "same_as": at}})
		return ap, nil
	}
	ap := model.Approval{ID: model.NewID(), AgentID: r.a.ID, Kind: model.ApprovalGuard, Tool: req.Tool,
		Input: raw, Description: info.Detail, Options: []string{}, State: model.ApprovalPending, CreatedAt: model.Now()}
	if err := f.st.PutApproval(ap); err != nil {
		return model.Approval{}, err
	}
	f.safetyLocked().holds[ap.ID] = make(chan struct{})
	f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Tool: req.Tool,
		Text: "The guard held this for you: " + info.RuleName,
		Meta: map[string]any{"guard": info, "approval_id": ap.ID}})
	r.waiting++
	r.a.State = model.StateWaiting
	f.saveAgentLocked(r)
	f.bus.Publish(model.BusEvent{Type: model.EvApprovalCreated, AgentID: r.a.ID, ProjectID: r.a.ProjectID, Data: ap})
	return ap, nil
}

// WaitHold waits up to wait for the owner's decision on one of the caller's holds and returns
// the approval as it then is.
func (f *Fleet) WaitHold(ctx context.Context, c Caller, id string, wait time.Duration) (model.Approval, error) {
	f.mu.Lock()
	ap, err := f.ownHoldLocked(c, id)
	done := f.safetyLocked().holds[id]
	f.mu.Unlock()
	if err != nil {
		return model.Approval{}, err
	}
	if ap.State == model.ApprovalPending && done != nil {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-done:
		case <-t.C:
		case <-ctx.Done():
		case <-f.ctx.Done():
		}
	}
	return f.ApprovalByID(id)
}

// CancelHold expires one of the caller's holds (its hook gave up waiting).
func (f *Fleet) CancelHold(c Caller, id string) (model.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ap, err := f.ownHoldLocked(c, id)
	if err != nil {
		return model.Approval{}, err
	}
	if ap.State == model.ApprovalPending {
		f.resolveHoldLocked(&ap, model.ApprovalExpired, "", "No answer in time; not run: ")
	}
	return ap, nil
}

func (f *Fleet) ownHoldLocked(c Caller, id string) (model.Approval, error) {
	ap, err := f.st.GetApproval(id)
	if err != nil || ap.Kind != model.ApprovalGuard {
		return model.Approval{}, errf(KindNotFound, "no guard hold %q", id)
	}
	if !c.Owner && ap.AgentID != c.AgentID {
		return model.Approval{}, errf(KindForbidden, "this hold belongs to another agent")
	}
	return ap, nil
}

// resolveHoldLocked finishes a guard approval, wakes its hook and writes the timeline row.
func (f *Fleet) resolveHoldLocked(ap *model.Approval, state model.ApprovalState, answer, text string) {
	f.finishApprovalLocked(ap, state, answer)
	st := f.safetyLocked()
	if ch, ok := st.holds[ap.ID]; ok {
		close(ch)
		delete(st.holds, ap.ID)
	}
	r, ok := f.agents[ap.AgentID]
	if !ok {
		return
	}
	var info model.GuardHold
	_ = json.Unmarshal(ap.Input, &info)
	f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Tool: ap.Tool, Text: text + info.RuleName,
		Meta: map[string]any{"guard": info, "approval_id": ap.ID, "decision": string(state)}})
	f.releaseWaitLocked(r)
}

func (f *Fleet) releaseWaitLocked(r *agentRec) {
	if r.waiting > 0 {
		r.waiting--
	}
	if r.waiting == 0 && r.a.State == model.StateWaiting {
		r.a.State = model.StateWorking
	}
	f.saveAgentLocked(r)
}

// decideGuard resolves a guard approval (owner only; the API has asked the owner gate).
func (f *Fleet) decideGuard(ap model.Approval, d DecideRequest) (model.Approval, error) {
	var state model.ApprovalState
	var text string
	switch d.Decision {
	case "allow":
		state, text = model.ApprovalAllowed, "You allowed it once: "
	case DecisionAllowAlways:
		if d.Scope != "agent" && d.Scope != "any" {
			return model.Approval{}, errf(KindInvalid, `scope must be "agent" or "any" for Allow always`)
		}
		state, text = model.ApprovalAllowed, "You allowed it always: "
	case "deny":
		state, text = model.ApprovalDenied, "You denied it: "
	default:
		return model.Approval{}, errf(KindInvalid, "decision must be allow, allow_always or deny for a guard hold")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, waiting := f.safetyLocked().holds[ap.ID]; !waiting {
		f.finishApprovalLocked(&ap, model.ApprovalExpired, "")
		return ap, errf(KindConflict, "the agent is no longer waiting for this; the hold expired")
	}
	if d.Decision == DecisionAllowAlways {
		if err := f.rememberAlwaysLocked(ap, d.Scope); err != nil {
			return model.Approval{}, err
		}
	}
	if state == model.ApprovalAllowed {
		f.rememberAllowedLocked(ap)
	}
	f.resolveHoldLocked(&ap, state, d.Message, text)
	return ap, nil
}

// dayOf shows an RFC 3339 time as its date ("2026-11-03"), or as given.
func dayOf(at string) string {
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		return t.Local().Format("2006-01-02")
	}
	return at
}

// holdKeyLocked is ap's AllowedKey: its call, folder and agent kind. An agent that is gone gets
// an empty kind, which no live agent has, so nothing matches.
func (f *Fleet) holdKeyLocked(ap model.Approval) string {
	var info model.GuardHold
	_ = json.Unmarshal(ap.Input, &info)
	kind := ""
	if r, ok := f.agents[ap.AgentID]; ok {
		kind = alwaysKey(&r.a)
	}
	return guard.AllowedKey(info.Rule, info.Tool, info.Command, info.Cwd, kind)
}

// holdRule is the guard rule of a hold approval ("" if unreadable: not rememberable).
func holdRule(ap model.Approval) string {
	var info model.GuardHold
	_ = json.Unmarshal(ap.Input, &info)
	return info.Rule
}

// rememberAllowedLocked records that the owner allowed this exact call (a failure to save only
// means the PIN is asked again next time).
func (f *Fleet) rememberAllowedLocked(ap model.Approval) {
	if !guard.Rememberable[holdRule(ap)] {
		return // asks every time: never recorded
	}
	k := f.holdKeyLocked(ap)
	s := f.safetyLocked().settings
	if _, ok := f.allowedAtLocked(k); ok {
		return
	}
	s.AllowedBefore = append(slices.Clone(s.AllowedBefore), guard.AllowedEntry{Key: k, At: time.Now().UTC().Format(time.RFC3339)})
	if len(s.AllowedBefore) > guard.AllowedBeforeMax {
		s.AllowedBefore = s.AllowedBefore[len(s.AllowedBefore)-guard.AllowedBeforeMax:]
	}
	if err := f.saveGuardLocked(s); err != nil {
		f.log.Warn("guard: cannot remember an allowed call", "err", err)
	}
}

// AllowedBefore reports whether the owner already allowed this guard hold's exact call once:
// then allowing it again needs no PIN (the API skips the gate; the owner still taps Allow).
func (f *Fleet) AllowedBefore(ap model.Approval) bool {
	if ap.Kind != model.ApprovalGuard || !guard.Rememberable[holdRule(ap)] {
		return false // not rememberable: always the fresh proof
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.allowedAtLocked(f.holdKeyLocked(ap))
	return ok
}

// allowedAtLocked returns when the owner first allowed the call with this key.
func (f *Fleet) allowedAtLocked(key string) (string, bool) {
	for _, e := range f.safetyLocked().settings.AllowedBefore {
		if e.Key == key {
			return e.At, true
		}
	}
	return "", false
}

// WithdrawHold: the agent that raised a pending hold takes it back (it decided not to run the
// command), so it never reaches the owner as a question. It never allows anything.
func (f *Fleet) WithdrawHold(c Caller, id string) (model.Approval, error) {
	if c.Owner || c.AgentID == "" {
		return model.Approval{}, errf(KindForbidden, "only the agent that raised a hold withdraws it")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ap, err := f.ownHoldLocked(c, id)
	if err != nil {
		return model.Approval{}, err
	}
	if ap.State != model.ApprovalPending {
		return model.Approval{}, errf(KindConflict, "this hold is no longer pending (%s)", ap.State)
	}
	f.resolveHoldLocked(&ap, model.ApprovalExpired, AnswerWithdrawn, "Withdrawn by the agent; not run: ")
	return ap, nil
}

// PendingHolds lists the caller's own pending guard holds (for withdraw_hold without an id).
func (f *Fleet) PendingHolds(c Caller) []model.Approval {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.Approval
	for id := range f.safetyLocked().holds {
		if ap, err := f.st.GetApproval(id); err == nil && ap.AgentID == c.AgentID && ap.State == model.ApprovalPending {
			out = append(out, ap)
		}
	}
	return out
}

// Guard decisions beyond allow and deny.
const (
	DecisionAllowAlways = "allow_always"       // DecideRequest.Scope: "agent" (its profile) or "any"
	AnswerAlways        = "always"             // Approval.Answer of a hold a standing allow let through
	AnswerWithdrawn     = "withdrawn by agent" // Approval.Answer of a hold its agent took back
	AnswerSame          = "same as approved"   // Approval.Answer of a call the owner allowed before
)

// rememberAlwaysLocked stores the standing allow for a held call (rule + exact command + tool,
// for the agent's profile or any agent). Only holds: blocks never reach a decision.
func (f *Fleet) rememberAlwaysLocked(ap model.Approval, scope string) error {
	var info model.GuardHold
	if err := json.Unmarshal(ap.Input, &info); err != nil || info.Outcome != string(guard.Hold) || info.Command == "" {
		return errf(KindInvalid, "this hold cannot be allowed always")
	}
	if _, ok := guard.RuleByID(info.Rule); !ok || f.ruleStateOfLocked(info.Rule) != guard.Hold {
		return errf(KindInvalid, "%q is not a hold rule of the guard", info.Rule)
	}
	if guard.RunTimeDependent(info.Command) {
		return errf(KindInvalid, "this command depends on values only known when it runs (variables, wildcards or subshells), so it cannot be allowed always; allow it once")
	}
	al := guard.AlwaysAllow{ID: model.NewID(), Rule: info.Rule, RuleName: info.RuleName, Tool: info.Tool,
		Command: info.Command, Cwd: info.Cwd, Created: time.Now().UTC().Format(time.RFC3339)}
	r, ok := f.agents[ap.AgentID]
	if ok {
		al.From = r.a.Name
	}
	if scope == "agent" {
		if !ok {
			return errf(KindConflict, "the agent is gone; allow it for any agent or once")
		}
		al.Profile = alwaysKey(&r.a)
	}
	s := f.safetyLocked().settings
	for _, old := range s.AlwaysAllow {
		if old.Rule == al.Rule && old.Tool == al.Tool && old.Command == al.Command && old.Cwd == al.Cwd && old.Profile == al.Profile {
			return nil // already remembered
		}
	}
	s.AlwaysAllow = append(slices.Clone(s.AlwaysAllow), al)
	if err := f.saveGuardLocked(s); err != nil {
		return err
	}
	f.log.Info("guard allow-always added", "rule", al.Rule, "profile", al.Profile, "command", al.Command)
	return nil
}

// ReportBlock writes a blocked call into the agent's timeline as an error row (agent only).
func (f *Fleet) ReportBlock(c Caller, req HoldRequest) error {
	if c.Owner {
		return errf(KindForbidden, "only an agent's guard hook reports blocks")
	}
	info, err := f.guardInfo(req, guard.Block)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.callerRecLocked(c)
	if err != nil {
		return err
	}
	if r == nil { // a peer
		return errf(KindForbidden, "only an agent's guard hook reports blocks")
	}
	text := "Blocked by the guard: " + info.RuleName
	if info.Detail != "" {
		text += " (" + info.Detail + ")"
	}
	f.appendLocked(r, model.TimelineEntry{Kind: model.EntryError, Tool: req.Tool, Text: text, IsError: true,
		Meta: map[string]any{"guard": info}})
	f.log.Warn("guard blocked a tool call", "agent", r.a.Name, "rule", info.Rule, "detail", info.Detail)
	return nil
}

// expireHoldsLocked expires the open holds of an agent whose process ended.
func (f *Fleet) expireHoldsLocked(agentID string) {
	st := f.safetyLocked()
	for id, ch := range st.holds {
		ap, err := f.st.GetApproval(id)
		if err != nil || ap.AgentID != agentID {
			continue
		}
		close(ch)
		delete(st.holds, id)
		if ap.State == model.ApprovalPending {
			f.finishApprovalLocked(&ap, model.ApprovalExpired, "")
		}
	}
}

// guardSpecLocked installs the enforced adapter or refuses the launch. Claude
// supports PreToolUse in both modes; other harnesses cannot satisfy an enabled
// guard yet. An explicit owner guard opt-out remains subject to launch policy.
func (f *Fleet) guardSpecLocked(r *agentRec, spec *runner.Spec) error {
	a := r.a
	if a.Harness == model.HarnessShell {
		return nil // owner terminal; agent authorization denies launching or typing into it
	}
	if err := f.guardAdmissionLocked(a); err != nil {
		return err
	}
	if !f.safetyLocked().settings.On(string(a.Level), a.Profile) { // off_for may name a level or a profile
		return nil
	}
	if f.codexParityEligibleLocked(a) {
		spec.CodexGuardHome = filepath.Join(f.opts.Paths.AgentDir(a.ID), "codex-guard")
		spec.GuardStates = func() map[string]string {
			out := map[string]string{}
			for id, o := range f.GuardStates() {
				out[id] = string(o)
			}
			return out
		}
		spec.CodexGuardExecutable = f.opts.Executable
		spec.Env = append(spec.Env, guard.EnvFolder+"="+a.Cwd, guard.EnvWorkspace+"="+f.opts.Workspace, guard.EnvState+"="+f.opts.Paths.Home)
		return nil
	}
	cfg := map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{
		"matcher": guard.Matcher,
		"hooks": []any{map[string]any{"type": "command", "command": HookCommand(f.opts.Executable),
			"timeout": int(hookTimeout / time.Second)}},
	}}}}
	// The owner, 3 Nov 13:54: no per-tool "allow once" prompts. With the guard hook installed, a
	// Claude agent on the default mode ("auto") runs in bypass mode, like the Python fleet: the
	// hook alone decides what reaches the owner (only machine-protection holds become cards).
	// config.json "agent_bypass": false keeps Claude's own prompts.
	if a.Harness == model.HarnessClaude && spec.PermissionMode == "auto" && f.agentBypass() {
		spec.PermissionMode = "bypassPermissions"
		cfg["skipDangerousModePermissionPrompt"] = true
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := f.opts.Paths.AgentDir(a.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "settings.json")
	if err := config.WriteFileAtomic(path, b, 0o600); err != nil {
		return err
	}
	spec.SettingsPath = path
	spec.Env = append(spec.Env, guard.EnvFolder+"="+a.Cwd, guard.EnvWorkspace+"="+f.opts.Workspace,
		guard.EnvState+"="+f.opts.Paths.Home)
	return nil
}

// GuardStates is the state of every switchable rule (defaults + the owner's switches): what
// the hook, the process check and the Codex parser apply.
func (f *Fleet) GuardStates() map[string]guard.Outcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.safetyLocked().settings.Effective()
}

func (f *Fleet) ruleStateOf(rule string) guard.Outcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ruleStateOfLocked(rule)
}

func (f *Fleet) ruleStateOfLocked(rule string) guard.Outcome {
	return guard.Context{States: f.safetyLocked().settings.Effective()}.StateOf(rule)
}

// SetRuleState flips one rule's switch (owner only; the API has asked the owner gate). Fixed
// rules have no switch. Every change is logged.
func (f *Fleet) SetRuleState(c Caller, rule string, state guard.Outcome) (guard.Settings, error) {
	if !c.Owner || c.Peer != "" || c.Companion || c.AgentID != "" {
		return guard.Settings{}, errf(KindForbidden, "only the owner flips a guard rule")
	}
	r, ok := guard.RuleByID(rule)
	if !ok || r.Fixed {
		return guard.Settings{}, errf(KindInvalid, "%q is not a switchable guard rule", rule)
	}
	if !guard.ValidState(state) {
		return guard.Settings{}, errf(KindInvalid, "state must be block, hold or allow")
	}
	f.mu.Lock()
	s := f.safetyLocked().settings
	old := s.Effective()[rule]
	if old == state {
		f.mu.Unlock()
		return f.GuardSettings(), nil
	}
	s.RuleStates = maps.Clone(s.RuleStates)
	if s.RuleStates == nil {
		s.RuleStates = map[string]guard.Outcome{}
	}
	s.RuleStates[rule] = state
	s.RuleLog = appendRuleLog(s.RuleLog, guard.RuleChange{Rule: rule, Old: old, New: state, At: time.Now().UTC().Format(time.RFC3339), By: "owner"})
	err := f.saveGuardLocked(s)
	f.mu.Unlock()
	if err != nil {
		return guard.Settings{}, err
	}
	f.log.Info("guard rule switched", "rule", rule, "old", old, "new", state)
	return f.GuardSettings(), nil
}

func appendRuleLog(log []guard.RuleChange, ch ...guard.RuleChange) []guard.RuleChange {
	out := append(slices.Clone(log), ch...)
	if len(out) > guard.RuleLogMax {
		out = out[len(out)-guard.RuleLogMax:]
	}
	return out
}

// applyRuleDefaultsLocked: the first start with rule switches logs, once, every rule whose
// default differs from what this install did before (the strict built-in outcome). Existing
// installs thereby move to the defaults of the owner's brief, visibly.
func (f *Fleet) applyRuleDefaultsLocked() {
	s := f.safetyLocked().settings
	if s.DefaultsVersion >= guard.DefaultsVersion {
		return
	}
	at := time.Now().UTC().Format(time.RFC3339)
	var ch []guard.RuleChange
	for _, r := range guard.Rules() {
		if _, set := s.RuleStates[r.ID]; !set && r.Default != r.Outcome {
			ch = append(ch, guard.RuleChange{Rule: r.ID, Old: r.Outcome, New: r.Default, At: at, By: "tmx"})
		}
	}
	s.RuleLog = appendRuleLog(s.RuleLog, ch...)
	s.DefaultsVersion = guard.DefaultsVersion
	if err := f.saveGuardLocked(s); err != nil {
		f.log.Warn("guard: cannot record the rule defaults", "err", err)
		return
	}
	if len(ch) > 0 {
		f.log.Info("guard: rule defaults applied", "changed", len(ch))
	}
}

// HookCommand is the hook's command line, quoted for the shell Claude Code runs hooks with.
func HookCommand(exe string) string {
	if runtime.GOOS == "windows" {
		return `"` + exe + `" hook pretooluse`
	}
	return "'" + strings.ReplaceAll(exe, "'", `'\''`) + "' hook pretooluse"
}

// hooksDisabledIn names a Claude settings file that switches every hook off, if any: the
// project's .claude settings, the user scope in each CLAUDE_CONFIG_DIR the launch uses (Claude
// reads its settings.json there instead of ~/.claude), and ~/.claude.
func hooksDisabledIn(cwd string, configDirs ...string) string {
	var files []string
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".claude", "settings.json"))
	}
	for _, d := range configDirs {
		if d != "" {
			files = append(files, filepath.Join(d, "settings.json"), filepath.Join(d, "settings.local.json"))
		}
	}
	files = append(files, filepath.Join(cwd, ".claude", "settings.json"), filepath.Join(cwd, ".claude", "settings.local.json"))
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s struct {
			DisableAllHooks bool `json:"disableAllHooks"`
		}
		if json.Unmarshal(b, &s) == nil && s.DisableAllHooks {
			return p
		}
	}
	return ""
}
