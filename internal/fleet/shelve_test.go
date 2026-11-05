package fleet

import (
	"context"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shelveWorker(t *testing.T, e *env, f *Fleet, name string) model.Agent {
	t.Helper()
	w, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: name, ProjectID: workerProject(t, f)})
	if err != nil {
		t.Fatal(err)
	}
	e.chat.Last(w.ID).State(model.StateIdle)
	waitState(t, f, w.ID, model.StateIdle)
	return w
}

func TestShelveDoneStopsOwnedToolsKeepsPromotedAndResumes(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	w := shelveWorker(t, e, f, "finisher")
	var promoted string
	for i := range 3 {
		result, err := f.RequestProcess(AgentCaller(w.ID), processRequest(t))
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			promoted = result.Process.ID
			if err := f.PromoteProcess(OwnerCaller, promoted, w.ProjectID, model.Owner); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := f.MarkDone(AgentCaller(w.ID), w.ID); err != nil {
		t.Fatal(err)
	}
	f.SweepShelved(time.Now())
	a := mustAgent(t, f, w.ID)
	if a.ShelvedAt == 0 || a.Desired != "stopped" || !e.chat.Last(w.ID).Stopped() {
		t.Fatalf("not shelved: %+v", a)
	}
	for _, p := range f.ProcessTools() {
		if (p.ID == promoted) != (p.PID != 0) {
			t.Fatalf("tool lifetime: %+v", p)
		}
	}
	stored, _ := e.st.GetAgent(w.ID)
	if stored.ShelvedAt != a.ShelvedAt || stored.DoneAt == 0 {
		t.Fatalf("not persisted: %+v", stored)
	}
	if _, err := f.StartAgent(OwnerCaller, w.ID); err != nil {
		t.Fatal(err)
	}
	a = mustAgent(t, f, w.ID)
	if a.ShelvedAt != 0 || a.DoneAt != 0 || a.ID != w.ID {
		t.Fatalf("not resumed: %+v", a)
	}
}

// Shelving goes through the same stopProcessIf path as an explicit stop, so a Windows-parity
// Codex agent's isolated copy of the owner's Codex login must not linger once it is shelved
// (tmx-lead, 3 Nov: a lingering credential copy for every auto-shelved agent is a stale secret).
func TestShelveDeletesCodexGuardAuthCopy(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	w := shelveWorker(t, e, f, "finisher")
	authDir := filepath.Join(f.opts.Paths.AgentDir(w.ID), "codex-guard", "home")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(authDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"owner":"fixture login"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.MarkDone(AgentCaller(w.ID), w.ID); err != nil {
		t.Fatal(err)
	}
	f.SweepShelved(time.Now())
	a := mustAgent(t, f, w.ID)
	if a.ShelvedAt == 0 {
		t.Fatalf("not shelved: %+v", a)
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("codex-guard auth copy survived shelving: %v", err)
	}
}

func TestShelveIdleThresholdAndExclusions(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	now := time.Now()
	for _, name := range []string{"old", "recent", "working", "waiting", "protected", "mail"} {
		w := shelveWorker(t, e, f, name)
		f.mu.Lock()
		r := f.agents[w.ID]
		r.a.LastActivityAt = now.Add(-3 * time.Hour).UnixMilli()
		switch name {
		case "recent":
			r.a.LastActivityAt = now.Add(-time.Hour).UnixMilli()
		case "working":
			r.a.State = model.StateWorking
		case "waiting":
			r.a.State = model.StateWaiting
			r.waiting = 1
		case "protected":
			r.a.Protected = true
		case "mail":
			r.a.Queued = 1
		}
		f.mu.Unlock()
	}
	f.mu.Lock()
	for _, r := range f.agents {
		if r.a.Level != model.LevelWorker {
			r.a.State = model.StateIdle
			r.a.DoneAt = 1
			r.a.LastActivityAt = 1
		}
	}
	f.mu.Unlock()
	f.SweepShelved(now)
	f.SweepShelved(now) // daily/repeated sweep is idempotent
	for _, name := range []string{"old", "recent", "working", "waiting", "protected", "mail", "head", "test-workers-lead"} {
		a := mustAgent(t, f, name)
		if (a.ShelvedAt != 0) != (name == "old") {
			t.Fatalf("unexpected shelving %s: %+v", name, a)
		}
	}
}

func TestShelveDisabledAndNewWorkClearsCompletion(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	w := shelveWorker(t, e, f, "worker")
	off := false
	f.opts.AutoShelve = &off
	if _, err := f.MarkDone(AgentCaller(w.ID), w.ID); err != nil {
		t.Fatal(err)
	}
	f.SweepShelved(time.Now().Add(24 * time.Hour))
	if mustAgent(t, f, w.ID).ShelvedAt != 0 {
		t.Fatal("disabled shelving ran")
	}
	if _, err := f.SendOwner(w.ID, "new work", ""); err != nil {
		t.Fatal(err)
	}
	if mustAgent(t, f, w.ID).DoneAt != 0 {
		t.Fatal("new work retained completion")
	}
}

func TestShelveCompletionWaitsForTurnAndBoundaries(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	w := shelveWorker(t, e, f, "worker")
	e.chat.Last(w.ID).State(model.StateWorking)
	waitState(t, f, w.ID, model.StateWorking)
	if _, err := f.MarkDone(AgentCaller(f.HeadID()), w.ID); KindOf(err) != KindForbidden {
		t.Fatal("cross-agent done")
	}
	on := true
	if _, err := f.UpdateAgent(AgentCaller(w.ID), w.ID, AgentPatch{Protected: &on}); KindOf(err) != KindForbidden {
		t.Fatal("agent self-protected")
	}
	if _, err := f.MarkDone(AgentCaller(w.ID), w.ID); err != nil {
		t.Fatal(err)
	}
	f.SweepShelved(time.Now())
	if mustAgent(t, f, w.ID).ShelvedAt != 0 {
		t.Fatal("cut off current turn")
	}
	e.chat.Last(w.ID).State(model.StateIdle)
	eventually(t, "done worker shelved at idle", func() bool { return mustAgent(t, f, w.ID).ShelvedAt != 0 })
}

func TestShelveStartupKeepsOverdueWorkerStopped(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	w := shelveWorker(t, e, f, "worker")
	if err := f.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	w, _ = e.st.GetAgent(w.ID)
	w.State, w.Desired = model.StateIdle, "running"
	w.LastActivityAt = time.Now().Add(-3 * time.Hour).UnixMilli()
	if err := e.st.PutAgent(w); err != nil {
		t.Fatal(err)
	}
	f2 := e.fleet()
	if err := f2.Start(); err != nil {
		t.Fatal(err)
	}
	a := mustAgent(t, f2, w.ID)
	if a.ShelvedAt == 0 || a.Desired != "stopped" || a.PID != 0 {
		t.Fatalf("restarted overdue worker: %+v", a)
	}
}

func TestWorkerRequiresProjectLeadAndInheritsLauncher(t *testing.T) {
	e := newEnv(t)
	f := e.fleet()
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "orphan"}); KindOf(err) != KindInvalid || !strings.Contains(err.Error(), "project with a lead") {
		t.Fatalf("orphan admitted: %v", err)
	}
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	headWorker, err := f.LaunchAgent(AgentCaller(f.HeadID()), LaunchRequest{Name: "head-worker"})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	general := *f.projects[headWorker.ProjectID]
	f.mu.Unlock()
	if general.Name != "General" || general.LeadID != f.HeadID() || headWorker.ParentID != f.HeadID() {
		t.Fatalf("head worker did not inherit General: %+v %+v", headWorker, general)
	}
	ownerWorker, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "owner-worker"})
	if err != nil || ownerWorker.ProjectID != general.ID {
		t.Fatalf("General not reused: %+v %v", ownerWorker, err)
	}
	p, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := f.LaunchAgent(AgentCaller(p.LeadID), LaunchRequest{Name: "worker"})
	if err != nil || w.ProjectID != p.ID || w.ParentID != p.LeadID {
		t.Fatalf("inheritance: %+v %v", w, err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "owner-shell", Harness: model.HarnessShell}); err != nil {
		t.Fatal(err)
	}
}
