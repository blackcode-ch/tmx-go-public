package fleet

import (
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// The inbox is available mid-turn; inspecting it as owner must not consume mail.
// A reply obeys the same busy/typing/unknown-state holds as a new send.
func TestMailSendInboxReplyHeld(t *testing.T) {
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
			if first.State != model.MsgQueued || reply.State != model.MsgQueued || len(p.Sent()) != 0 {
				t.Fatal("mail bypassed hold")
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
			if len(p.Sent()) != 1 {
				t.Fatal("queued send and reply not batched on idle")
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
			if len(p.Sent()) != 1 {
				t.Fatal("mail delivered twice")
			}
		})
	}
}
