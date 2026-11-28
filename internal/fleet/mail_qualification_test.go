package fleet

import (
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestTerminalLaunchPromptPrecedesQueuedOwnerInput(t *testing.T) {
	for _, h := range []model.Harness{model.HarnessClaude, model.HarnessCodex, model.HarnessOpenCode, model.HarnessGemini, model.HarnessPi, model.HarnessShell} {
		t.Run(string(h), func(t *testing.T) {
			e := newUnguardedEnv(t)
			e.ext = fakeClaudeExt(t)
			f := e.started()
			initial := "first launch line\nsecond launch line"
			if h == model.HarnessShell {
				initial = "echo launch-first"
			}
			a, err := f.LaunchAgent(OwnerCaller, LaunchRequest{
				ProjectID: workerProject(t, f), Name: "terminal-start", Harness: h,
				Mode: model.ModeTerminal, Prompt: initial,
			})
			if err != nil {
				t.Fatal(err)
			}
			p := e.term.Last(a.ID)
			if p == nil {
				t.Fatal("terminal process did not start")
			}
			if got := p.Sent(); len(got) != 1 || !strings.Contains(got[0], initial) {
				t.Fatal("initial launch input was not sent exactly once before idle")
			}
			f.mu.Lock()
			ready := ownerTurnReady(f.agents[a.ID])
			f.mu.Unlock()
			if ready {
				t.Fatal("starting terminal was ready for the queued owner turn")
			}
			if _, err := f.SendMessage(OwnerCaller, SendRequest{To: a.ID, Body: "owner follow-up"}); err != nil {
				t.Fatal(err)
			}
			if h == model.HarnessShell && len(p.Sent()) != 1 {
				t.Fatal("shell submitted queued input before idle")
			}
			p.State(model.StateIdle)
			eventually(t, "owner follow-up", func() bool { return len(p.Sent()) >= 2 })
			got := p.Sent()
			if len(got) != 2 || !strings.Contains(got[1], "owner follow-up") {
				t.Fatal("owner follow-up did not follow the initial launch input exactly once")
			}
		})
	}
}

// The inbox is available mid-turn; inspecting it as owner must not consume mail.
// A reply obeys the same busy/typing/unknown-state holds as a new send.
func TestMailSendInboxReplyImmediate(t *testing.T) {
	for _, hold := range []string{"busy", "typing", "unknown"} {
		t.Run(hold, func(t *testing.T) {
			e := newUnguardedEnv(t)
			f := e.started()
			a, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "recipient", Mode: model.ModeTerminal})
			if err != nil {
				t.Fatal(err)
			}
			b, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "sender"})
			if err != nil {
				t.Fatal(err)
			}
			p := e.term.Last(a.ID)
			orig, err := f.SendMessage(AgentCaller(a.ID), SendRequest{To: b.ID, Body: "question"})
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			r := f.agents[a.ID]
			switch hold {
			case "busy":
				r.a.State = model.StateWorking
			case "unknown":
				r.a.State = model.StateStarting
			case "typing":
				r.a.State = model.StateIdle
				r.humanInputAt = time.Now()
			}
			f.mu.Unlock()
			first, err := f.SendMessage(AgentCaller(b.ID), SendRequest{To: a.ID, Body: "new mail"})
			if err != nil {
				t.Fatal(err)
			}
			reply, err := f.SendMessage(AgentCaller(b.ID), SendRequest{ReplyTo: orig.ID, Body: "answer"})
			if err != nil {
				t.Fatal(err)
			}
			if first.State == model.MsgQueued || reply.State == model.MsgQueued || len(p.Sent()) != 2 {
				t.Fatal("mail did not submit immediately")
			}
			msgs, err := f.Messages(OwnerCaller, a.ID, 0)
			if err != nil || len(msgs) != 3 {
				t.Fatalf("owner inbox: %v %v", msgs, err)
			}
			msgs, err = f.Messages(AgentCaller(a.ID), "", 0)
			if err != nil || len(msgs) != 3 {
				t.Fatalf("recipient inbox: %v %v", msgs, err)
			}
			f.mu.Lock()
			r.a.State = model.StateIdle
			r.humanInputAt = time.Now().Add(-TypingQuiet - time.Second)
			f.mu.Unlock()
			f.tryDeliver(a.ID)
			if len(p.Sent()) != 2 {
				t.Fatal("send and reply were not separate submissions")
			}
			msgs, err = f.Messages(AgentCaller(a.ID), "", 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range msgs {
				if m.To == a.ID && m.State != model.MsgRead {
					t.Fatalf("unread: %+v", m)
				}
			}
			f.tryDeliver(a.ID)
			if len(p.Sent()) != 2 {
				t.Fatal("mail delivered twice")
			}
		})
	}
}
