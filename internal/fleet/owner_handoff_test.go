package fleet

import (
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"strings"
	"sync"
	"testing"
	"time"
)

type heldMailProcess struct {
	runner.Process
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func TestOwnerBurstWaitsForLiveTurnAndUnblocksInFIFO(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	a, proc := talkingAgent(t, e, f, "owner-burst")
	proc.State(model.StateWorking)
	waitState(t, f, a.ID, model.StateWorking)
	before := len(proc.Sent())
	inputs := []string{"first follow-up", "second follow-up", "third follow-up", "fourth follow-up", "fifth follow-up"}
	for _, body := range inputs {
		m, err := f.SendOwner(a.ID, body, "")
		if err != nil || m.State != model.MsgQueued || m.Receipt != "queued" {
			t.Fatalf("queued %q: %+v %v", body, m, err)
		}
	}
	if proc.Interrupts() != 0 || len(proc.Sent()) != before {
		t.Fatal("owner sends cut the active turn short")
	}
	proc.Turn("completed the active answer", 0, 1)
	for i, body := range inputs {
		eventually(t, body, func() bool { return len(proc.Sent()) > before+i })
		if sent := proc.Sent()[before:]; len(sent) != i+1 || sent[i] != body {
			t.Fatalf("handoff %d: %q", i, sent)
		}
		if i == 0 {
			if _, err := f.InterruptAgent(OwnerCaller, a.ID); err != nil {
				t.Fatal(err)
			}
		}
		proc.State(model.StateIdle)
	}
	if proc.Interrupts() != 1 {
		t.Fatalf("expected only the explicit interrupt, got %d", proc.Interrupts())
	}
}

func (p *heldMailProcess) SendMail(text string, ids []string) error {
	p.once.Do(func() { close(p.entered); <-p.release })
	return p.Process.Send(text)
}
func (p *heldMailProcess) Interject(text string, ids []string) error { return p.SendMail(text, ids) }

func TestOwnerFIFOAcrossConcurrentHandoff(t *testing.T) {
	_, f, _, a, original := typeTextLead(t)
	p := &heldMailProcess{Process: original, entered: make(chan struct{}), release: make(chan struct{})}
	f.mu.Lock()
	f.agents[a.ID].proc = p
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.agents[a.ID].proc = original; f.mu.Unlock() }()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = f.SendOwner(a.ID, "older handoff", "") }()
	select {
	case <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("first handoff never entered")
	}
	// Claim has happened, but the first runner call has not received its body yet.
	newer, err := f.SendOwner(a.ID, "newer handoff", "")
	if err != nil || newer.State != model.MsgQueued {
		close(p.release)
		<-done
		t.Fatalf("newer bypassed handoff: %+v %v", newer, err)
	}
	close(p.release)
	<-done
	if original.Interrupts() != 0 || len(original.Sent()) != 1 {
		t.Fatal("second owner message interrupted the first turn")
	}
	f.handleEvent(f.agents[a.ID], p, runner.Event{Kind: runner.EvState, State: model.StateIdle})
	f.tryDeliver(a.ID)
	sent := original.Sent()
	if len(sent) != 2 || !strings.Contains(sent[0], "older handoff") || !strings.Contains(sent[1], "newer handoff") {
		t.Fatal(sent)
	}
}

func TestMixedSenderFIFOAcrossConcurrentHandoff(t *testing.T) {
	_, f, _, a, original := typeTextLead(t)
	peer, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "mail-peer"})
	if err != nil {
		t.Fatal(err)
	}
	p := &heldMailProcess{Process: original, entered: make(chan struct{}), release: make(chan struct{})}
	f.mu.Lock()
	f.agents[a.ID].proc = p
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.agents[a.ID].proc = original; f.mu.Unlock() }()
	before := len(original.Sent())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = f.SendMessage(AgentCaller(peer.ID), SendRequest{To: a.ID, Body: "agent oldest"})
	}()
	select {
	case <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("first handoff did not start")
	}
	_, e1 := f.SendOwner(a.ID, "owner middle", "")
	_, e2 := f.SendMessage(AgentCaller(peer.ID), SendRequest{To: a.ID, Body: "agent newest"})
	close(p.release)
	<-done
	if e1 != nil || e2 != nil {
		t.Fatalf("%v %v", e1, e2)
	}
	if original.Interrupts() != 0 || len(original.Sent()) != before+1 {
		t.Fatal("owner turn interrupted after the first handoff")
	}
	f.handleEvent(f.agents[a.ID], p, runner.Event{Kind: runner.EvState, State: model.StateIdle})
	f.tryDeliver(a.ID)
	sent := original.Sent()[before:]
	if len(sent) != 3 || !strings.Contains(sent[0], "agent oldest") || !strings.Contains(sent[1], "owner middle") || !strings.Contains(sent[2], "agent newest") {
		t.Fatal(sent)
	}
}
