package fleet

import (
	"fmt"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Live mode switch (DESIGN.md "Switching live"). A Claude or Codex agent leaves chat mode for
// its harness's own screen (the TUI in a pty, read by tmx like any terminal agent) and comes
// back, on the same conversation: its process is stopped and started again in the other mode
// with the same session id, so the profile, the MCP config, the permissions, the guard and the
// mail rules are rebuilt exactly as at any start (specLocked).
//
// A switch never cuts a turn short: asked for during a turn (or while an approval is open), it
// waits (Agent.PendingMode) until the agent has been idle for switchSettle, and mail waits with
// it. A switch lives in memory only: the store keeps the launch mode (Agent.HomeMode while
// switched), so a daemon restart brings the agent back the way it was launched. A switched
// process that ends by itself (the owner typed /exit in the screen, or it crashed) goes home too.

// switchSettle: how long an agent must have been idle before its process is switched. Claude
// starts a message it queued during a turn right after that turn's result; the wait keeps a
// switch from cutting such a turn off before it showed any output.
var switchSettle = 2 * time.Second

// ModeRequest is POST /api/agents/{id}/mode.
type ModeRequest struct {
	Mode model.Mode `json:"mode"` // chat | terminal
}

// ModeChange answers a ModeRequest.
type ModeChange struct {
	Agent   model.Agent `json:"agent"`
	Pending bool        `json:"pending"` // it switches when its current turn ends
	Note    string      `json:"note"`
}

// SetMode switches an agent between chat mode and its harness's own screen (owner only).
// A stopped agent only changes the mode it starts in; a running one switches at once when it
// is idle, else when its current turn ends. Asking for the mode it is in cancels a pending
// switch.
func (f *Fleet) SetMode(c Caller, idOrName string, req ModeRequest) (ModeChange, error) {
	if !c.Owner {
		return ModeChange{}, errf(KindForbidden, "only the owner switches an agent between chat and its own screen")
	}
	to := req.Mode
	if to != model.ModeChat && to != model.ModeTerminal {
		return ModeChange{}, errf(KindInvalid, "mode must be chat or terminal")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.resolveLocked(idOrName)
	if err != nil {
		return ModeChange{}, err
	}
	if _, _, err := f.harnessModeLocked(r.a.Harness, to); err != nil {
		return ModeChange{}, err
	}
	name, where, toWhere := r.a.Name, modeWhere(r.a.Harness, to), modeTo(r.a.Harness, to)
	out := ModeChange{}
	switch {
	case r.switching:
		return ModeChange{}, errf(KindConflict, "%s is switching right now; try again in a moment", name)
	case to == r.a.Mode:
		if r.a.PendingMode == "" {
			out.Note = fmt.Sprintf("%s is %s already.", name, where)
			break
		}
		r.a.PendingMode = ""
		f.saveAgentLocked(r)
		out.Note = fmt.Sprintf("%s stays %s; the switch is cancelled.", name, where)
	case f.pausedLocked(r.a.ID):
		return ModeChange{}, errf(KindConflict, "%s is paused by a resource brake; resume it first", name)
	case r.proc == nil && !r.starting:
		f.applyModeLocked(r, to)
		r.a.PendingMode = ""
		f.saveAgentLocked(r)
		out.Note = fmt.Sprintf("Saved; %s starts %s.", name, where)
	default:
		r.a.PendingMode = to
		f.saveAgentLocked(r)
		if f.switchableLocked(r) {
			f.scheduleSwitchLocked(r)
			out.Note = fmt.Sprintf("Switching %s %s, on the same conversation.", name, toWhere)
		} else {
			out.Pending = true
			out.Note = fmt.Sprintf("%s is in the middle of a turn; it switches %s when the turn ends.", name, toWhere)
		}
	}
	out.Agent = r.a
	return out, nil
}

// modeWhere names a mode for the owner: "in chat" or "in Claude Code's own screen".
func modeWhere(h model.Harness, m model.Mode) string {
	if m == model.ModeChat {
		return "in chat"
	}
	return "in " + harnessLabel(h) + "'s own screen"
}

// modeTo is where a switch goes: "to chat" or "to Claude Code's own screen".
func modeTo(h model.Harness, m model.Mode) string {
	if m == model.ModeChat {
		return "to chat"
	}
	return "to " + harnessLabel(h) + "'s own screen"
}

func harnessLabel(h model.Harness) string {
	if info, ok := harness.Get(h); ok {
		return info.Label
	}
	return string(h)
}

// applyModeLocked sets the mode the agent runs in, remembering its launch mode while it is
// switched (HomeMode; cleared once it is home again).
func (f *Fleet) applyModeLocked(r *agentRec, to model.Mode) {
	home := r.a.HomeMode
	if home == "" {
		home = r.a.Mode
	}
	r.a.Mode = to
	if to == home {
		r.a.HomeMode = ""
	} else {
		r.a.HomeMode = home
	}
}

// switchableLocked: the agent's process runs, is idle and asks nothing, so stopping it cuts
// no turn short.
func (f *Fleet) switchableLocked(r *agentRec) bool {
	return r.proc != nil && !r.starting && !r.stopping && r.a.State == model.StateIdle && r.waiting == 0 &&
		!f.pausedLocked(r.a.ID)
}

// scheduleSwitchLocked runs a pending switch once the agent has been idle for switchSettle.
// It is called when the switch is asked for and whenever the agent becomes idle.
func (f *Fleet) scheduleSwitchLocked(r *agentRec) {
	if r.a.PendingMode == "" || r.switchScheduled || r.switching || f.closed || !f.switchableLocked(r) {
		return
	}
	wait := max(switchSettle-time.Since(r.idleAt), 0)
	r.switchScheduled = true
	id := r.a.ID
	time.AfterFunc(wait, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.closed {
			return
		}
		f.goTracked(func() { f.trySwitch(id) }) // under f.mu while open: ordered before Close's Wait
	})
}

// trySwitch switches the agent's process if it is still idle, settled and asked to.
func (f *Fleet) trySwitch(id string) {
	f.mu.Lock()
	r, ok := f.agents[id]
	if !ok {
		f.mu.Unlock()
		return
	}
	r.switchScheduled = false
	to, from := r.a.PendingMode, r.a.Mode
	switch {
	case to == "" || r.switching:
		f.mu.Unlock()
		return
	case to == from:
		r.a.PendingMode = ""
		f.saveAgentLocked(r)
		f.mu.Unlock()
		return
	case !f.switchableLocked(r):
		f.mu.Unlock() // busy again: the next idle schedules it
		return
	case time.Since(r.idleAt) < switchSettle:
		f.scheduleSwitchLocked(r)
		f.mu.Unlock()
		return
	}
	r.switching = true
	f.mu.Unlock()

	f.stopProcess(f.ctx, id, true) // handleExit applies the new mode

	f.mu.Lock()
	r.switching = false
	if f.agents[id] != r || r.a.Desired != "running" || f.closed || r.a.Mode != to {
		f.mu.Unlock() // stopped or removed meanwhile, or the process did not end
		return
	}
	f.mu.Unlock()
	err := f.startAgentOpts(id, "", startOpts{StartOptions: StartOptions{Force: true}})
	if err == nil {
		return
	}
	// The other mode would not start: back to the mode it ran in, so the agent keeps working.
	f.mu.Lock()
	if f.agents[id] != r || r.proc != nil || r.starting || r.a.Desired != "running" || r.a.Mode == from {
		f.mu.Unlock()
		return
	}
	f.applyModeLocked(r, from)
	f.appendLocked(r, model.TimelineEntry{Kind: model.EntryError, IsError: true,
		Text: fmt.Sprintf("Could not switch %s (%v); back %s", modeTo(r.a.Harness, to), err, modeWhere(r.a.Harness, from))})
	f.saveAgentLocked(r)
	f.mu.Unlock()
	if err := f.startAgentOpts(id, "", startOpts{StartOptions: StartOptions{Force: true}}); err != nil {
		f.log.Warn("start after a failed switch", "agent", id, "err", err)
	}
}

// exitModeLocked runs in handleExit for the process that just ended. It returns the timeline
// note and true when the exit belongs to a switch (the new mode is applied, the agent shows
// as starting) or to a switched process that ended by itself (it goes home; goHome starts it).
// A requested stop applies a pending switch (the turn it waited for is over); any other exit
// drops it.
func (f *Fleet) exitModeLocked(r *agentRec, code int, err error) (string, bool) {
	pending := r.a.PendingMode
	r.a.PendingMode = ""
	running := r.a.Desired == "running"
	switch {
	case r.switching && running && pending != "":
		f.applyModeLocked(r, pending)
		r.a.State = model.StateStarting
		if pending == model.ModeTerminal {
			return fmt.Sprintf("Switched to %s's own screen, same conversation", harnessLabel(r.a.Harness)), true
		}
		return "Back in chat, same conversation", true
	case r.stopping || !running:
		if pending != "" {
			f.applyModeLocked(r, pending)
		}
		return "", false
	case r.a.HomeMode != "":
		how := "closed"
		switch {
		case err != nil:
			how = "ended: " + err.Error()
		case code != 0:
			how = fmt.Sprintf("exited with code %d", code)
		}
		from := r.a.Mode
		f.applyModeLocked(r, r.a.HomeMode)
		r.a.State, r.goHome = model.StateStarting, true
		return fmt.Sprintf("%s %s; back %s, same conversation", screenName(r.a.Harness, from), how, modeWhere(r.a.Harness, r.a.Mode)), true
	}
	return "", false
}

func screenName(h model.Harness, m model.Mode) string {
	if m == model.ModeTerminal {
		return harnessLabel(h) + "'s screen"
	}
	return "The chat process"
}

// afterSwitchExit starts an agent whose switched process ended by itself, in its home mode.
func (f *Fleet) afterSwitchExit(r *agentRec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !r.goHome {
		return
	}
	r.goHome = false
	if f.closed || f.agents[r.a.ID] != r {
		return
	}
	id := r.a.ID
	f.goTracked(func() {
		if err := f.startAgentOpts(id, "", startOpts{StartOptions: StartOptions{Force: true}}); err != nil {
			f.log.Warn("start at home after the screen closed", "agent", id, "err", err)
		}
	})
}

// mailHeldLocked: mail waits while a switch is pending (a new turn would put it off) or the
// process is being stopped; it is delivered once the agent runs in its new mode.
func mailHeldLocked(r *agentRec) bool {
	return r.a.PendingMode != "" || r.stopping || r.switching
}

// typedInScreen: a message someone typed into the agent's own screen (read from its transcript).
func typedInScreen(e model.TimelineEntry) bool {
	src, _ := e.Meta["source"].(string)
	return src == "screen"
}
