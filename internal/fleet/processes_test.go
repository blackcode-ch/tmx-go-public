package fleet

import (
	"github.com/blackcode-ch/tmx-go/internal/model"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFleetProcessHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "fleet-process-helper" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

func processRequest(t *testing.T) ProcessRequest {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ProcessRequest{Purpose: "test tool", Command: exe, Args: []string{"-test.run=^TestFleetProcessHelper$", "--", "fleet-process-helper"}}
}

func TestAgentStopEndsTwoTools(t *testing.T) {
	f := newEnv(t).started()
	id := f.HeadID()
	for range 2 {
		result, err := f.RequestProcess(AgentCaller(id), processRequest(t))
		if err != nil {
			t.Fatal(err)
		}
		if result.Process == nil || result.Approval != nil {
			t.Fatal(err)
		}
	}
	aps, err := f.Approvals(model.ApprovalPending)
	if err != nil || len(aps) != 0 {
		t.Fatalf("unexpected approval cards: %v %v", aps, err)
	}
	rows, err := f.AgentProcesses(AgentCaller(id), id)
	if err != nil || len(rows) != 2 {
		t.Fatalf("list: %v %v", rows, err)
	}
	if _, err := f.StopAgent(OwnerCaller, id); err != nil {
		t.Fatal(err)
	}
	rows, _ = f.AgentProcesses(OwnerCaller, id)
	for _, p := range rows {
		if p.PID != 0 {
			t.Fatalf("child survived: %+v", p)
		}
	}
	if _, err := f.StartProcess(OwnerCaller, id, processRequest(t)); err == nil {
		t.Fatal("stopped owner accepted child")
	}
}

func TestProcessRequestExpiresAndCannotReplay(t *testing.T) {
	f := newEnv(t).started()
	id := f.HeadID()
	held := ProcessRequest{Purpose: "red command", Command: "sudo", Args: []string{"true"}}
	result, err := f.RequestProcess(AgentCaller(id), held)
	if err != nil || result.Approval == nil || result.Process != nil {
		t.Fatalf("hold: %+v %v", result, err)
	}
	ap := result.Approval
	if len(f.processes.List()) != 0 {
		t.Fatal("held command executed")
	}
	if _, err := f.Decide(AgentCaller(id), ap.ID, DecideRequest{Decision: "allow"}); err == nil {
		t.Fatal("self approval")
	}
	if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: "deny"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: "allow"}); err == nil {
		t.Fatal("replayed approval")
	}
	pending, err := f.RequestProcess(AgentCaller(id), held)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.StopAgent(OwnerCaller, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Decide(OwnerCaller, pending.Approval.ID, DecideRequest{Decision: "allow"}); err == nil {
		t.Fatal("stopped generation launched")
	}
	if _, err := f.RequestProcess(AgentCaller(id), processRequest(t)); err == nil {
		t.Fatal("stopped agent spawned")
	}
}

func TestProcessStartAdmission(t *testing.T) {
	for _, boundary := range []string{"readonly", "elevated", "unsafe", "blocked"} {
		t.Run(boundary, func(t *testing.T) {
			f := newEnv(t).started()
			id := f.HeadID()
			req := processRequest(t)
			switch boundary {
			case "readonly":
				f.profiles["head"].ReadOnly = true
			case "elevated":
				f.opts.Elevated = func() bool { return true }
			case "unsafe":
				f.agents[id].a.Cwd = filepath.VolumeName(f.agents[id].a.Cwd) + string(os.PathSeparator)
			case "blocked":
				req.Command = "pkill"
				req.Args = []string{"node"}
			}
			if _, err := f.RequestProcess(AgentCaller(id), req); KindOf(err) != KindForbidden {
				t.Fatalf("admission: %v", err)
			}
			if len(f.processes.List()) != 0 {
				t.Fatal("denied command executed")
			}
		})
	}
}
func TestProcessOwnershipBoundaries(t *testing.T) {
	f := newEnv(t).started()
	id := f.HeadID()
	if _, err := f.StartProcess(AgentCaller(id), id, processRequest(t)); err == nil {
		t.Fatal("agent arbitrary spawn bypassed shell boundary")
	}
	p, err := f.StartProcess(OwnerCaller, id, processRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.AgentProcesses(AgentCaller("other"), id); err == nil {
		t.Fatal("cross-agent list")
	}
	if err := f.StopOwnedProcess(AgentCaller("other"), p.ID); err == nil {
		t.Fatal("cross-agent stop")
	}
	if err := f.PromoteProcess(AgentCaller(id), p.ID, "project", model.Owner); err == nil {
		t.Fatal("self-promotion")
	}
	if err := f.StopOwnedProcess(AgentCaller(id), p.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPromotedProjectToolSurvivesCreator(t *testing.T) {
	f := newEnv(t).started()
	id := f.HeadID()
	project, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "Tools"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.StartProcess(OwnerCaller, id, processRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.PromoteProcess(OwnerCaller, p.ID, project.ID, model.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.StopAgent(OwnerCaller, id); err != nil {
		t.Fatal(err)
	}
	for _, v := range f.processes.List() {
		if v.ID == p.ID && (v.PID != p.PID || v.OwnerID != model.Owner) {
			t.Fatalf("promoted tool stopped: %+v", v)
		}
	}
}

func TestPromotedToolRemainsVisibleAfterCreatorRemoval(t *testing.T) {
	f := newEnv(t).started()
	project, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "Retained tools"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "toolmaker", ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.StartProcess(OwnerCaller, agent.ID, processRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.PromoteProcess(OwnerCaller, p.ID, project.ID, model.Owner); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveAgent(OwnerCaller, agent.ID); err != nil {
		t.Fatal(err)
	}
	rows := f.ProcessTools()
	if len(rows) != 1 || rows[0].PID != p.PID || !rows[0].Promoted || rows[0].OwnerID != model.Owner {
		t.Fatalf("lost promoted tool: %+v", rows)
	}
	if err := f.DeleteProject(OwnerCaller, project.ID); err != nil {
		t.Fatal(err)
	}
	if f.ProcessTools()[0].PID != 0 {
		t.Fatal("deleted project retained tool")
	}
}
