package fleet

import (
	"encoding/json"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"os"
	"runtime"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/processes"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

type ProcessRequest struct {
	Purpose string   `json:"purpose"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

func (f *Fleet) StartProcess(c Caller, agent string, req ProcessRequest) (model.ProcessInfo, error) {
	if !c.Owner {
		return model.ProcessInfo{}, errf(KindForbidden, "only the owner may authorize arbitrary commands")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startProcessLocked(agent, req)
}

func (f *Fleet) startProcessLocked(agent string, req ProcessRequest) (model.ProcessInfo, error) {
	r, err := f.resolveLocked(agent)
	if err != nil {
		return model.ProcessInfo{}, err
	}
	if f.closed || r.proc == nil || r.stopping || r.a.Desired == "stopped" {
		return model.ProcessInfo{}, errf(KindConflict, "agent is not running")
	}
	if f.opts.Elevated() {
		return model.ProcessInfo{}, errf(KindForbidden, "refusing to launch from an elevated process")
	}
	if err := harness.ValidateLaunchCwd(r.a.Cwd); err != nil {
		return model.ProcessInfo{}, errf(KindForbidden, "%s", err)
	}
	if strings.TrimSpace(req.Purpose) == "" || len(req.Purpose) > 240 || len(req.Args) > 256 {
		return model.ProcessInfo{}, errf(KindInvalid, "purpose required (up to 240 characters); at most 256 arguments")
	}
	p, err := f.processes.Start(processes.Spec{OwnerID: r.a.ID, ProjectID: r.a.ProjectID, Purpose: req.Purpose, Command: req.Command, Args: req.Args, Cwd: r.a.Cwd})
	if err != nil {
		return p, errf(KindInvalid, "could not start process: %s", err)
	}
	return p, nil
}

const ToolProcessStart = "tmx_process_start"

type ownedProcessRequest struct {
	agentID string
	proc    runner.Process
	request ProcessRequest
}

type ProcessResult struct {
	Process  *model.ProcessInfo `json:"process,omitempty"`
	Approval *model.Approval    `json:"approval,omitempty"`
}

// RequestProcess starts ordinary workspace tools immediately. Only guard holds
// create a generation-bound, single-use owner approval.
func (f *Fleet) RequestProcess(c Caller, req ProcessRequest) (ProcessResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.Owner || c.Companion || c.Peer != "" {
		return ProcessResult{}, errf(KindForbidden, "only agents request a tool launch")
	}
	r, err := f.callerRecLocked(c)
	if err != nil {
		return ProcessResult{}, err
	}
	if f.closed || r.proc == nil || r.stopping || r.a.Desired == "stopped" {
		return ProcessResult{}, errf(KindConflict, "agent is not running")
	}
	if f.profileForLocked(r.a).ReadOnly {
		return ProcessResult{}, errf(KindForbidden, "read-only profiles cannot launch commands")
	}
	if strings.TrimSpace(req.Purpose) == "" || len(req.Purpose) > 240 || strings.TrimSpace(req.Command) == "" || len(req.Args) > 256 {
		return ProcessResult{}, errf(KindInvalid, "command and purpose required; purpose up to 240 characters and at most 256 arguments")
	}
	raw, err := json.Marshal(req)
	if err != nil || len(raw) > MaxBodyBytes {
		return ProcessResult{}, errf(KindInvalid, "command request too large")
	}
	if f.opts.Elevated() {
		return ProcessResult{}, errf(KindForbidden, "refusing to launch from an elevated process")
	}
	if err := harness.ValidateLaunchCwd(r.a.Cwd); err != nil {
		return ProcessResult{}, errf(KindForbidden, "%s", err)
	}
	home, _ := os.UserHomeDir()
	v := guard.CheckExec(guard.Context{OS: runtime.GOOS, Cwd: r.a.Cwd, Own: r.a.Cwd,
		Workspace: f.opts.Workspace, StateDir: f.opts.Paths.Home, Home: home,
		Temp: []string{os.TempDir()}, Env: os.Getenv, TmxNames: []string{f.opts.Executable},
		AllowBrowserAutomation: f.safetyLocked().settings.AllowBrowserAutomation,
		States:                 f.safetyLocked().settings.Effective()}, req.Command, req.Args)
	if v.Outcome == guard.Block {
		return ProcessResult{}, errf(KindForbidden, "%s", v.Message())
	}
	if v.Outcome == guard.Allow {
		p, err := f.startProcessLocked(r.a.ID, req)
		if err != nil {
			return ProcessResult{}, err
		}
		return ProcessResult{Process: &p}, nil
	}
	count := 0
	for _, p := range f.processRequests {
		if p.agentID == r.a.ID {
			count++
		}
	}
	if count >= 8 {
		return ProcessResult{}, errf(KindConflict, "too many pending tool requests")
	}
	ap := model.Approval{ID: model.NewID(), AgentID: r.a.ID, Kind: model.ApprovalPermission, Tool: ToolProcessStart, Input: raw, Description: "Start owned tool: " + req.Purpose + " — " + v.Message(), State: model.ApprovalPending, CreatedAt: model.Now(), Options: []string{}}
	if err := f.st.PutApproval(ap); err != nil {
		return ProcessResult{}, err
	}
	if f.processRequests == nil {
		f.processRequests = map[string]ownedProcessRequest{}
	}
	req.Args = append([]string(nil), req.Args...)
	f.processRequests[ap.ID] = ownedProcessRequest{agentID: r.a.ID, proc: r.proc, request: req}
	f.Publish(model.BusEvent{Type: model.EvApprovalCreated, AgentID: r.a.ID, ProjectID: r.a.ProjectID, Data: ap})
	return ProcessResult{Approval: &ap}, nil
}

func (f *Fleet) decideProcessLocked(ap model.Approval, d DecideRequest) (model.Approval, error) {
	if d.Decision != "allow" && d.Decision != "deny" {
		return ap, errf(KindInvalid, "decision must be allow or deny")
	}
	p, ok := f.processRequests[ap.ID]
	r := f.agents[ap.AgentID]
	if !ok || r == nil || r.proc != p.proc || r.stopping || r.a.Desired == "stopped" {
		delete(f.processRequests, ap.ID)
		f.finishApprovalLocked(&ap, model.ApprovalExpired, "")
		return ap, errf(KindConflict, "the agent's process ended; request expired")
	}
	if d.Decision == "allow" && f.profileForLocked(r.a).ReadOnly {
		return ap, errf(KindForbidden, "read-only profiles cannot launch commands")
	}
	// Persist consumption before execution: a failed write must never execute.
	ap.State = model.ApprovalDenied
	if d.Decision == "allow" {
		ap.State = model.ApprovalAllowed
	}
	ap.DecidedAt = model.Now()
	if err := f.st.PutApproval(ap); err != nil {
		return ap, err
	}
	delete(f.processRequests, ap.ID)
	f.Publish(model.BusEvent{Type: model.EvApprovalResolved, AgentID: ap.AgentID, Data: ap})
	if d.Decision == "allow" {
		process, err := f.startProcessLocked(ap.AgentID, p.request)
		if err != nil {
			f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Text: "Approved tool could not start", IsError: true})
			return ap, err
		}
		f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Text: "Started owned tool: " + process.ID})
	}
	return ap, nil
}

func (f *Fleet) AgentProcesses(c Caller, agent string) ([]model.ProcessInfo, error) {
	f.mu.Lock()
	r, err := f.resolveLocked(agent)
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	a := r.a
	proc := r.proc
	if !c.Owner && c.AgentID != a.ID {
		f.mu.Unlock()
		return nil, errf(KindForbidden, "only the owner or this agent may inspect its processes")
	}
	f.mu.Unlock()
	out := []model.ProcessInfo{}
	for _, p := range f.processes.List() {
		if p.OwnerID == a.ID || p.CreatorID == a.ID {
			out = append(out, p)
		}
	}
	if a.PID > 0 {
		rows, err := sysinfo.RegisteredTree(a.PID)
		f.mu.Lock()
		current := f.agents[a.ID]
		valid := current != nil && current.proc == proc && current.a.PID == a.PID
		f.mu.Unlock()
		if err == nil && valid {
			for _, p := range rows {
				category := model.CategoryAgentTool
				purpose := "harness child"
				if p.PID == a.PID {
					category = model.CategoryAgent
					purpose = "agent harness"
				}
				out = append(out, model.ProcessInfo{ID: fmt.Sprintf("observed:%d:%d", p.PID, p.Start), Category: category, OwnerID: a.ID, ProjectID: a.ProjectID, Purpose: purpose, PID: p.PID, Command: p.Name, State: "running"})
			}
		}
	}
	return out, nil
}

func (f *Fleet) StopOwnedProcess(c Caller, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.processes.List() {
		if p.ID == id {
			if p.Category == model.CategorySystemTool || (!c.Owner && c.AgentID != p.OwnerID) {
				return errf(KindForbidden, "this process is not yours to stop")
			}
			f.processes.Stop(id)
			return nil
		}
	}
	return errf(KindNotFound, "no process %q", id)
}

func (f *Fleet) PromoteProcess(c Caller, id, project, owner string) error {
	if !c.Owner {
		return errf(KindForbidden, "only the owner may promote tools")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errf(KindConflict, "daemon is stopping")
	}
	p, err := f.projectLocked(project)
	if err != nil {
		return err
	}
	if owner != model.Owner {
		r, e := f.resolveLocked(owner)
		if e != nil {
			return e
		}
		if r.proc == nil || r.stopping || r.a.Desired == "stopped" || r.a.ProjectID != p.ID {
			return errf(KindConflict, "tool owner must be running in this project")
		}
		owner = r.a.ID
	}
	if err := f.processes.Promote(id, owner, p.ID); err != nil {
		return errf(KindConflict, "%s", err)
	}
	return nil
}

// StartSystemProcess is for built-in daemon wiring, never an HTTP/MCP command.
func (f *Fleet) StartSystemProcess(name string, req ProcessRequest) (model.ProcessInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.opts.Elevated() {
		return model.ProcessInfo{}, errf(KindForbidden, "system process launch unavailable")
	}
	return f.processes.Start(processes.Spec{OwnerID: "system", Purpose: name, Command: req.Command, Args: req.Args, System: true})
}

func (f *Fleet) SystemTools() []model.ProcessInfo {
	out := []model.ProcessInfo{}
	for _, p := range f.processes.List() {
		if p.Category == model.CategorySystemTool {
			out = append(out, p)
		}
	}
	return out
}

// ProcessTools keeps promoted tools discoverable even after their creator is removed.
func (f *Fleet) ProcessTools() []model.ProcessInfo { return f.processes.List() }
