package fleet

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

func taskTarget(t *testing.T, f *Fleet) model.Agent {
	t.Helper()
	a, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "target"})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.agents[a.ID].a.State = model.StateIdle
	f.mu.Unlock()
	return a
}

func TestTaskObservationWindow(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	taskTarget(t, f)
	req := TaskRequest{ID: "123456789abc", To: "target@testbox", Body: "work"}
	if _, err := f.PostTask(OwnerCaller, req); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	record, err := f.st.Task(req.ID)
	if err == nil {
		record.AdmittedAt = model.TaskNow() - 120
		record.SawWorking = false
		err = f.st.SaveTask(record)
	}
	f.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.GetTask(OwnerCaller, req.ID)
	if err != nil || got.Task.State != "running" {
		t.Fatalf("default window: %+v %v", got, err)
	}
	f.mu.Lock()
	f.opts.Tasks = &config.TaskConfig{StartSeconds: 100}
	f.mu.Unlock()
	got, err = f.GetTask(OwnerCaller, req.ID)
	if err != nil || got.Task.State != "uncertain" {
		t.Fatalf("configured window: %+v %v", got, err)
	}
}

func TestTaskReplayAndObservedTurn(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	a := taskTarget(t, f)
	req := TaskRequest{ID: "123456789abc", To: "target@testbox", Body: "one assigned turn", From: "companion"}
	out, err := f.PostTask(OwnerCaller, req)
	if err != nil || out.Task.State != "running" || out.Task.Verified || out.Task.Sender != "companion@testbox" {
		t.Fatalf("%+v %v", out, err)
	}
	const n = 6
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.PostTask(OwnerCaller, req); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(e.chat.Last(a.ID).Sent()) != 1 {
		t.Fatal("replay sent extra turns")
	}
	changed := req
	changed.Urgent = true
	if _, err := f.PostTask(OwnerCaller, changed); KindOf(err) != KindConflict {
		t.Fatal("urgency conflict", err)
	}
	p := e.chat.Last(a.ID)
	f.handleEvent(f.agents[a.ID], p, runner.Event{Kind: runner.EvTimeline, Entry: &model.TimelineEntry{Kind: model.EntryResult, Text: "Observed result"}})
	final, err := f.GetTask(OwnerCaller, req.ID)
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("tmx-task-v1:"+req.ID+":turn_finished")))
	if err != nil || final.Task.State != "turn_finished" || final.Task.ReceiptID == nil || *final.Task.ReceiptID != want || final.Task.Report != "Observed result" || final.Task.FinishedAt == nil {
		t.Fatalf("%+v %v", final, err)
	}
	f.mu.Lock()
	saved := f.agents[a.ID]
	delete(f.agents, a.ID)
	f.mu.Unlock()
	replay, err := f.PostTask(OwnerCaller, req)
	if err != nil || replay.Task.UpdatedAt != final.Task.UpdatedAt {
		t.Fatal("replay looked up removed target", err)
	}
	f.mu.Lock()
	f.agents[a.ID] = saved
	f.mu.Unlock()
}

func TestTaskAuthorityAndAmbiguousBatch(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	a := taskTarget(t, f)
	req := TaskRequest{ID: "abcdef012345", To: "target@testbox", Body: "work"}
	if _, err := f.PostTask(AgentCaller(a.ID), req); KindOf(err) != KindForbidden {
		t.Fatal("self assignment", err)
	}
	other, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: a.ProjectID, Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.PostTask(AgentCaller(other.ID), req); KindOf(err) != KindForbidden {
		t.Fatal("worker authority", err)
	}
	if _, err := f.PostTask(CompanionCaller, req); KindOf(err) != KindForbidden {
		t.Fatal("link authority", err)
	}
	if _, err := f.PostTask(PeerCaller("peer"), req); KindOf(err) != KindForbidden {
		t.Fatal("remote peer authority", err)
	}
	f.mu.Lock()
	f.agents[a.ID].a.State = model.StateWorking
	f.mu.Unlock()
	if _, err := f.PostTask(OwnerCaller, req); err != nil {
		t.Fatal(err)
	}
	second := req
	second.ID = "abcdef012346"
	if _, err := f.PostTask(OwnerCaller, second); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.agents[a.ID].a.State = model.StateIdle
	f.mu.Unlock()
	f.tryDeliver(a.ID)
	for _, id := range []string{req.ID, second.ID} {
		got, err := f.GetTask(OwnerCaller, id)
		if err != nil || got.Task.State != "uncertain" {
			t.Fatal(got, err)
		}
	}
	if len(e.chat.Last(a.ID).Sent()) != 1 {
		t.Fatal("batch effect count")
	}
}
