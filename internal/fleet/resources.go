package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// Resources is resource control (internal/resources) as the fleet sees it. nil = no limits.
// Its methods must not call back into the Fleet (they run with Fleet.mu held).
type Resources interface {
	// Admit reports whether one more agent may start while `running` agents run; when not,
	// why, as a short phrase ("8 of 8 agents running").
	Admit(running int) (ok bool, reason string)
	// LowPriority reports whether agent processes start at lower CPU priority.
	LowPriority() bool
	// Resumed tells resource control the owner resumed an agent braked for reason; raise =
	// with a raised limit.
	Resumed(agentID, reason string, raise bool)
}

// StartOptions modify a start (POST /api/agents/{id}/start, launch).
type StartOptions struct {
	Force  bool `json:"force,omitempty"`   // owner only: start now even over the limits ("start anyway")
	NoWait bool `json:"no_wait,omitempty"` // refuse instead of queueing when over a limit
}

type startOpts struct {
	recovery           *agentRec
	recoveryGeneration uint64
	deliberate         bool
	StartOptions
	fromQueue bool // the queue pump already admitted it
	reattach  bool // daemon start: take over a session host an earlier daemon left running
}

// Brake reasons (Agent.PausedReason).
const (
	BrakeWriteBudget = "write_budget"
	BrakeMemory      = "memory"
	BrakeDisk        = "disk"
)

// resState is the fleet's resource bookkeeping. Guarded by Fleet.mu, except pumpMu.
type resState struct {
	queue  []string // agent IDs waiting for capacity, in order
	paused map[string]*pauseRec
	pumpMu sync.Mutex // one queue pump at a time
}

type pauseRec struct {
	reason     string
	prev       model.AgentState // the runner's state, restored on resume
	approvalID string
}

const pendingPromptFile = "pending-prompt.txt"

func (f *Fleet) suspend(pid int, stop bool) ([]sysinfo.Proc, error) {
	if f.opts.Suspend != nil {
		return f.opts.Suspend(pid, stop)
	}
	return sysinfo.SuspendTree(pid, stop)
}

func (f *Fleet) lowPriorityLocked() bool {
	return f.opts.Resources != nil && f.opts.Resources.LowPriority()
}

// runningLocked counts agents that hold capacity: a process (paused ones too) or a start
// in flight.
func (f *Fleet) runningLocked() int {
	n := 0
	for _, r := range f.agents {
		if r.proc != nil || r.starting {
			n++
		}
	}
	return n
}

// Counts returns how many agents run (paused included), wait in the queue, and are paused.
func (f *Fleet) Counts() (running, queued, paused int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runningLocked(), len(f.res.queue), len(f.res.paused)
}

// LiveAgents returns the agents that have a process, for measuring.
func (f *Fleet) LiveAgents() []model.Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.Agent, 0, len(f.agents))
	for _, r := range f.agents {
		if r.proc != nil && r.a.PID > 0 {
			out = append(out, r.a)
		}
	}
	return out
}

// Publish puts an event on the bus (resource samples, slot changes, chat.* events).
func (f *Fleet) Publish(ev model.BusEvent) {
	f.auditApprovalEvent(ev)
	f.bus.Publish(ev)
}

// admitLocked decides whether a start of r (not running) goes ahead now. It reports
// queued=true when r now waits in the queue, or a KindTooMany error with NoWait.
func (f *Fleet) admitLocked(r *agentRec, o startOpts, prompt string) (queued bool, err error) {
	if o.fromQueue {
		return false, nil
	}
	if o.Force || r.a.Level == model.LevelHead || f.opts.Resources == nil {
		f.dequeueLocked(r)
		return false, nil
	}
	if slices.Contains(f.res.queue, r.a.ID) {
		return true, nil // already waiting; keeps its place
	}
	ok, reason := true, ""
	if len(f.res.queue) > 0 {
		ok, reason = false, f.queueReasonLocked() // others wait already: no overtaking
	} else {
		ok, reason = f.opts.Resources.Admit(f.runningLocked())
	}
	if ok {
		return false, nil
	}
	if o.NoWait {
		return false, errf(KindTooMany, "the machine is at its limit (%s); %s was not started", reason, r.a.Name)
	}
	f.enqueueLocked(r, reason, prompt)
	f.goTracked(f.PumpQueue)
	return true, nil
}

// capacityLocked is the launch-time check for NoWait: an error when a start would queue.
func (f *Fleet) capacityLocked(name string) error {
	if f.opts.Resources == nil {
		return nil
	}
	reason := f.queueReasonLocked()
	ok := len(f.res.queue) == 0
	if ok {
		ok, reason = f.opts.Resources.Admit(f.runningLocked())
	}
	if !ok {
		return errf(KindTooMany, "the machine is at its limit (%s); %s was not launched", reason, name)
	}
	return nil
}

func (f *Fleet) queueReasonLocked() string {
	if len(f.res.queue) > 0 {
		if r := f.agents[f.res.queue[0]]; r != nil && r.a.QueueReason != "" {
			return r.a.QueueReason
		}
	}
	return "other agents are waiting"
}

func (f *Fleet) enqueueLocked(r *agentRec, reason, prompt string) {
	f.res.queue = append(f.res.queue, r.a.ID)
	r.a.State, r.a.Desired, r.a.Error, r.a.QueueReason = model.StateQueued, "running", "", reason
	if prompt != "" {
		dir := f.opts.Paths.AgentDir(r.a.ID)
		if err := os.MkdirAll(dir, 0o700); err == nil {
			_ = os.WriteFile(filepath.Join(dir, pendingPromptFile), []byte(prompt), 0o600)
		}
	}
	f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Text: "queued: " + reason,
		Meta: map[string]any{"resource": "queue"}})
	f.renumberLocked()
}

func (f *Fleet) dequeueLocked(r *agentRec) {
	i := slices.Index(f.res.queue, r.a.ID)
	if i < 0 {
		return
	}
	f.res.queue = slices.Delete(f.res.queue, i, i+1)
	r.a.QueuePos, r.a.QueueReason = 0, ""
	if r.a.State == model.StateQueued {
		r.a.State = model.StateStopped
	}
	f.saveAgentLocked(r)
	f.renumberLocked()
}

// renumberLocked keeps QueuePos 1..n in queue order.
func (f *Fleet) renumberLocked() {
	for i, id := range f.res.queue {
		if r := f.agents[id]; r != nil && r.a.QueuePos != i+1 {
			r.a.QueuePos = i + 1
			f.saveAgentLocked(r)
		}
	}
}

func (f *Fleet) takePendingPrompt(id string) string {
	path := filepath.Join(f.opts.Paths.AgentDir(id), pendingPromptFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	_ = os.Remove(path)
	return string(b)
}

// PumpQueue starts queued agents, in order, while there is capacity. Resource control
// calls it after every sample; the fleet after every exit.
func (f *Fleet) PumpQueue() {
	f.res.pumpMu.Lock()
	defer f.res.pumpMu.Unlock()
	for {
		f.mu.Lock()
		if f.closed || len(f.res.queue) == 0 {
			f.mu.Unlock()
			return
		}
		id := f.res.queue[0]
		r := f.agents[id]
		if r == nil || r.proc != nil || r.starting {
			f.res.queue = f.res.queue[1:]
			f.renumberLocked()
			f.mu.Unlock()
			continue
		}
		ok, reason := true, ""
		if f.opts.Resources != nil {
			ok, reason = f.opts.Resources.Admit(f.runningLocked())
		}
		if !ok {
			for _, qid := range f.res.queue {
				if q := f.agents[qid]; q != nil && q.a.QueueReason != reason {
					q.a.QueueReason = reason
					f.saveAgentLocked(q)
				}
			}
			f.mu.Unlock()
			return
		}
		f.res.queue = f.res.queue[1:]
		r.a.QueuePos, r.a.QueueReason = 0, ""
		f.renumberLocked()
		f.mu.Unlock()
		if err := f.startAgentOpts(id, f.takePendingPrompt(id), startOpts{fromQueue: true}); err != nil {
			f.log.Warn("start queued agent", "agent", id, "err", err)
		}
	}
}

// StartAgentWith is StartAgent with options: Force ("start anyway", owner only) skips the
// queue; NoWait refuses instead of queueing.
func (f *Fleet) StartAgentWith(c Caller, idOrName string, o StartOptions) (model.Agent, error) {
	if o.Force && !c.Owner {
		return model.Agent{}, errf(KindForbidden, "only the owner can start an agent over the machine's limits")
	}
	id, err := f.authorizeAct(c, idOrName)
	if err != nil {
		return model.Agent{}, err
	}
	if err := f.startAgentOpts(id, "", startOpts{StartOptions: o, deliberate: true}); err != nil {
		return model.Agent{}, err
	}
	return f.Agent(id)
}

// holdsCapacity: a running agent keeps its place across a restart.
func (f *Fleet) holdsCapacity(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.agents[id]
	return ok && (r.proc != nil || r.starting)
}

// bootOrderLocked: at daemon start, agents that ran come first; queued ones keep their order.
func (f *Fleet) bootOrderLocked(ids []string) []string {
	sort.SliceStable(ids, func(i, j int) bool {
		qi := f.agents[ids[i]].a.State == model.StateQueued
		qj := f.agents[ids[j]].a.State == model.StateQueued
		return !qi && qj
	})
	return ids
}

// beforeStopLocked: a queued agent leaves the queue; a paused one is continued first so it
// can end gracefully.
func (f *Fleet) beforeStopLocked(r *agentRec) {
	f.dequeueLocked(r)
	if p := f.res.paused[r.a.ID]; p != nil && r.proc != nil {
		if _, err := f.suspend(r.a.PID, false); err != nil {
			f.log.Warn("resume before stop", "agent", r.a.Name, "err", err)
		}
		f.clearPauseLocked(r, p, "")
	}
}

// afterExit runs when a process's event loop ended: its pause is over and its capacity free.
func (f *Fleet) afterExit(r *agentRec) {
	f.mu.Lock()
	if p := f.res.paused[r.a.ID]; p != nil && r.proc == nil {
		f.clearPauseLocked(r, p, "")
		f.saveAgentLocked(r)
	}
	f.mu.Unlock()
	f.goTracked(f.PumpQueue)
}

// pausedStateLocked: while paused, runner state changes are remembered, not shown.
func (f *Fleet) pausedStateLocked(r *agentRec, st model.AgentState) bool {
	p := f.res.paused[r.a.ID]
	if p == nil {
		return false
	}
	p.prev = st
	return true
}

func (f *Fleet) pausedLocked(id string) bool { return f.res.paused[id] != nil }

// BrakeAgent pauses an agent's whole process tree for a resource brake. ask=true also puts
// an item in Needs you (Resume / Raise). It returns the processes it suspended.
func (f *Fleet) BrakeAgent(id, reason, detail string, ask bool) ([]sysinfo.Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.agents[id]
	if !ok {
		return nil, errf(KindNotFound, "no agent %q", id)
	}
	if r.a.Harness == model.HarnessShell {
		return nil, errf(KindForbidden, "the owner shell is exempt from resource brakes")
	}
	if r.proc == nil || r.stopping || r.a.PID <= 0 {
		return nil, errf(KindConflict, "%s is not running", r.a.Name)
	}
	if f.res.paused[id] != nil {
		return nil, errf(KindConflict, "%s is paused already", r.a.Name)
	}
	procs, err := f.suspend(r.a.PID, true)
	if err != nil {
		return nil, fmt.Errorf("pause %s: %w", r.a.Name, err)
	}
	if f.res.paused == nil {
		f.res.paused = map[string]*pauseRec{}
	}
	p := &pauseRec{reason: reason, prev: r.a.State}
	f.res.paused[id] = p
	r.a.State, r.a.PausedReason, r.a.PausedDetail, r.a.PausedAt = model.StatePaused, reason, detail, model.Now()
	text := "paused by tmx: " + detail
	if r.a.Level == model.LevelHead {
		text = "the head was paused by tmx: " + detail
	}
	f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Text: text, IsError: true,
		Meta: map[string]any{"resource": reason}})
	if ask {
		opts := []string{"Resume"}
		if reason == BrakeWriteBudget || reason == BrakeMemory {
			opts = append(opts, "Raise limit")
		}
		ap := model.Approval{ID: model.NewID(), AgentID: id, Kind: model.ApprovalBrake, Tool: reason,
			Description: detail, Question: r.a.Name + " was paused", Options: opts,
			State: model.ApprovalPending, CreatedAt: model.Now()}
		if err := f.st.PutApproval(ap); err != nil {
			f.log.Error("store approval", "err", err)
		} else {
			p.approvalID = ap.ID
			f.Publish(model.BusEvent{Type: model.EvApprovalCreated, AgentID: id, ProjectID: r.a.ProjectID, Data: ap})
		}
	}
	f.saveAgentLocked(r)
	f.log.Warn("agent paused", "agent", r.a.Name, "reason", reason, "detail", detail, "processes", len(procs))
	return procs, nil
}

// UnbrakeAgent resumes an agent paused for reason ("" = any) without the owner: a new day,
// the end of a disk turn, or watchdog recovery. Restoration is unconditional:
// launch admission must not leave an already-suspended process or its bookkeeping
// paused beyond the safety deadline. It reports whether it resumed anything.
func (f *Fleet) UnbrakeAgent(id, reason, why string) bool {
	f.mu.Lock()
	r, ok := f.agents[id]
	p := f.res.paused[id]
	if !ok || p == nil || (reason != "" && p.reason != reason) {
		f.mu.Unlock()
		return false
	}
	f.resumeLocked(r, p, why)
	f.mu.Unlock()
	f.goTracked(func() { f.tryDeliver(id) })
	return true
}

// ResumeAgent continues an agent a brake paused (owner, or head/lead for their subtree).
func (f *Fleet) ResumeAgent(c Caller, idOrName string) (model.Agent, error) {
	id, err := f.authorizeAct(c, idOrName)
	if err != nil {
		return model.Agent{}, err
	}
	reason, err := f.resumeBy(id, "resumed by "+f.callerName(c))
	if err != nil {
		return model.Agent{}, err
	}
	if f.opts.Resources != nil {
		f.opts.Resources.Resumed(id, reason, false)
	}
	f.tryDeliver(id)
	return f.Agent(id)
}

func (f *Fleet) callerName(c Caller) string {
	if c.Owner {
		return "the owner"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nameOfLocked(c.AgentID)
}

// resumeBy resumes a paused agent and returns the brake reason it was paused for.
func (f *Fleet) resumeBy(id, why string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.agents[id]
	if !ok {
		return "", errf(KindNotFound, "no agent %q", id)
	}
	p := f.res.paused[id]
	if p == nil {
		return "", errf(KindConflict, "%s is not paused", r.a.Name)
	}
	if err := f.launchAdmissionLocked(r.a); err != nil {
		return "", err
	}
	f.resumeLocked(r, p, why)
	return p.reason, nil
}

// resumeLocked restores an existing process. User-requested paths check admission
// before calling; automatic safety recovery must remain unconditional.
func (f *Fleet) resumeLocked(r *agentRec, p *pauseRec, why string) {
	if r.proc != nil && r.a.PID > 0 {
		if _, err := f.suspend(r.a.PID, false); err != nil {
			f.log.Warn("resume agent", "agent", r.a.Name, "err", err)
		}
	}
	f.clearPauseLocked(r, p, why)
	f.saveAgentLocked(r)
}

// clearPauseLocked forgets the pause, restores the state and expires its Needs-you item.
func (f *Fleet) clearPauseLocked(r *agentRec, p *pauseRec, why string) {
	delete(f.res.paused, r.a.ID)
	if r.a.State == model.StatePaused {
		st := p.prev
		if st == model.StateWaiting && r.waiting == 0 {
			st = model.StateWorking
		}
		if st == "" || st == model.StatePaused {
			st = model.StateIdle
		}
		r.a.State = st
	}
	r.a.PausedReason, r.a.PausedDetail, r.a.PausedAt = "", "", 0
	if p.approvalID != "" {
		if ap, err := f.st.GetApproval(p.approvalID); err == nil && ap.State == model.ApprovalPending {
			f.finishApprovalLocked(&ap, model.ApprovalExpired, "")
		}
	}
	if why != "" {
		f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Text: why, Meta: map[string]any{"resource": p.reason}})
	}
}

// decideBrake resolves a Needs-you brake item: resume (allow), raise the limit and resume,
// or deny (stay paused, item dismissed).
func (f *Fleet) decideBrake(ap model.Approval, d DecideRequest) (model.Approval, error) {
	raise := d.Decision == "raise" || (d.Decision == "answer" && (d.Answer == "Raise limit" || d.Answer == "raise"))
	resume := raise || d.Decision == "allow" || d.Decision == "resume" || (d.Decision == "answer" && d.Answer == "Resume")
	if !resume && d.Decision != "deny" {
		return model.Approval{}, errf(KindInvalid, "decision must be resume, raise or deny for a paused agent")
	}
	f.mu.Lock()
	p := f.res.paused[ap.AgentID]
	if p == nil || p.approvalID != ap.ID {
		f.finishApprovalLocked(&ap, model.ApprovalExpired, "")
		f.mu.Unlock()
		return ap, errf(KindConflict, "the agent is not paused any more; nothing to decide")
	}
	if !resume {
		p.approvalID = "" // dismissed; the agent stays paused until resumed from its page
		f.finishApprovalLocked(&ap, model.ApprovalDenied, d.Message)
		f.mu.Unlock()
		return ap, nil
	}
	answer := "resume"
	if raise {
		answer = "raise"
	}
	why := "resumed by the owner"
	if raise {
		why = "resumed by the owner with a raised limit"
	}
	if err := f.launchAdmissionLocked(f.agents[ap.AgentID].a); err != nil {
		f.mu.Unlock()
		return ap, err
	}
	p.approvalID = "" // clearPauseLocked must not expire the accepted approval
	f.resumeLocked(f.agents[ap.AgentID], p, why)
	f.finishApprovalLocked(&ap, model.ApprovalAllowed, answer)
	p.approvalID = ""
	reason := p.reason
	f.mu.Unlock()
	if f.opts.Resources != nil {
		f.opts.Resources.Resumed(ap.AgentID, reason, raise)
	}
	f.tryDeliver(ap.AgentID)
	return ap, nil
}

// slotRule tells agents to take turns for heavy work (DESIGN.md "Resources").
func (f *Fleet) slotRule() string {
	return fmt.Sprintf("The machine is shared: run heavy builds, test suites, installs and big clones through "+
		"`%[1]s slot -- <command>`, and anything that starts a browser through `%[1]s slot --kind browser -- <command>`; "+
		"it waits for a free slot, runs the command and gives the slot back. If tmx pauses you for using too many "+
		"resources, the owner decides when you continue.", f.opts.Executable)
}
