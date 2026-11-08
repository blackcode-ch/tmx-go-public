package fleet

import (
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// TestFleetControlAPI covers sprint s05's acceptance scenario end to end against the fake
// harness: launch -> list, stop -> stopped, start -> resumed, restart fresh -> a new
// conversation, send -> delivered when idle, and a worker stopping another agent -> refused.
func TestFleetControlAPI(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	lead := mustAgent(t, f, "alpha-lead")
	lc := callerFor(t, f, e.chat, lead.ID)

	// launch -> list
	w, err := f.LaunchAgent(lc, LaunchRequest{Name: "w1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	snap, err := f.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range snap.Agents {
		if a.ID == w.ID {
			found = true
			if a.Name != "w1" || a.Level != model.LevelWorker || a.ParentID != lead.ID || a.State == "" {
				t.Fatalf("listed agent: %+v", a)
			}
		}
	}
	if !found {
		t.Fatal("launched agent not in list")
	}
	wc := callerFor(t, f, e.chat, w.ID)

	// A real conversation (EvSession + a turn) so a later start/restart has something to
	// resume, as TestRestartRecovery does.
	launched := e.chat.Last(w.ID)
	launched.Emit(runner.Event{Kind: runner.EvSession, SessionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"})
	launched.Turn("on it", 0.1, 1)

	// stop -> stopped
	a, err := f.StopAgent(lc, "w1")
	if err != nil || a.State != model.StateStopped || a.Desired != "stopped" {
		t.Fatalf("stop: %+v %v", a, err)
	}

	// start -> resumed (same confirmed session id, no fresh --session-id args)
	a, err = f.StartAgent(lc, "w1")
	if err != nil || a.State != model.StateStarting || a.Desired != "running" {
		t.Fatalf("start: %+v %v", a, err)
	}
	resumed := e.chat.Last(w.ID)
	if resumed.Spec.SessionID != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" || len(resumed.Spec.ExtraArgs) != 0 {
		t.Fatalf("start did not resume the same conversation: %+v", resumed.Spec)
	}
	resumed.State(model.StateIdle)

	// restart without Fresh -> resumes the same conversation; Prompt is ignored (it is only a
	// fresh conversation's first instruction)
	sameSessionBefore := mustAgent(t, f, "w1").SessionID
	a, err = f.RestartAgentWith(lc, "w1", RestartOptions{Prompt: "should not be delivered"})
	if err != nil {
		t.Fatalf("restart (not fresh): %v", err)
	}
	if a.SessionID != sameSessionBefore {
		t.Fatal("restart without fresh changed the session id")
	}
	notFresh := e.chat.Last(w.ID)
	if notFresh.Spec.SessionID != sameSessionBefore || len(notFresh.Spec.ExtraArgs) != 0 {
		t.Fatalf("restart without fresh did not resume the same conversation: %+v", notFresh.Spec)
	}
	notFresh.State(model.StateIdle)
	if sent := notFresh.Sent(); len(sent) != 0 {
		t.Fatalf("prompt delivered on a non-fresh restart: %q", sent)
	}

	// restart fresh -> a new conversation (pre-assigned --session-id, not the old one; prompt delivered)
	oldSession := mustAgent(t, f, "w1").SessionID
	a, err = f.RestartAgentWith(lc, "w1", RestartOptions{Fresh: true, Prompt: "fresh start: pick up the audit"})
	if err != nil {
		t.Fatalf("restart fresh: %v", err)
	}
	if a.SessionID == oldSession {
		t.Fatal("restart fresh kept the old session id")
	}
	fresh := e.chat.Last(w.ID)
	if fresh.Spec.SessionID != "" || len(fresh.Spec.ExtraArgs) != 2 || fresh.Spec.ExtraArgs[0] != "--session-id" ||
		fresh.Spec.ExtraArgs[1] != a.SessionID {
		t.Fatalf("restart fresh did not start a new conversation: %+v", fresh.Spec)
	}
	fresh.State(model.StateIdle)
	waitState(t, f, "w1", model.StateIdle)
	if sent := fresh.Sent(); len(sent) != 1 || sent[0] != "fresh start: pick up the audit" {
		t.Fatalf("fresh prompt not delivered: %q", sent)
	}

	// send -> delivered when idle (busy first, so it queues, then delivered on idle)
	fresh.State(model.StateWorking)
	waitState(t, f, "w1", model.StateWorking)
	m, err := f.SendMessage(lc, SendRequest{To: "w1", Body: "status please"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if m.State != model.MsgQueued {
		t.Fatalf("expected queued while busy: %+v", m)
	}
	before := len(fresh.Sent())
	fresh.Turn("working on it", 0, 0)
	eventually(t, "mail delivered at idle", func() bool { return len(fresh.Sent()) > before })
	if got := mustAgent(t, f, "w1").Queued; got != 0 {
		t.Fatalf("still queued after idle delivery: %d", got)
	}

	// a worker stopping another agent -> refused
	if _, err := f.StopAgent(wc, "alpha-lead"); KindOf(err) != KindForbidden {
		t.Fatalf("worker stopped its lead: %v", err)
	}
	w2, _ := f.LaunchAgent(lc, LaunchRequest{Name: "w2"})
	if _, err := f.StopAgent(wc, "w2"); KindOf(err) != KindForbidden {
		t.Fatalf("worker stopped a sibling worker: %v", err)
	}
	_ = w2
}

// TestTypeKeysScreenAuthorization covers fleet control's terminal primitives: tier-based
// authorization (same as stop/start/restart) and deterministic round trips against the fake
// harness, without a real terminal or network.
func TestTypeKeysScreenAuthorization(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	lead := mustAgent(t, f, "alpha-lead")
	lc := callerFor(t, f, e.chat, lead.ID)
	w, _ := f.LaunchAgent(lc, LaunchRequest{Name: "w1", Mode: model.ModeTerminal})
	wc := callerFor(t, f, e.term, w.ID)
	proc := e.term.Last(w.ID)
	proc.State(model.StateIdle)
	eventually(t, "w1 idle", func() bool { return mustAgent(t, f, "w1").State == model.StateIdle })

	// type: a lead's text is delivered like mail (terminal_test.go has the details)
	if err := f.TypeText(lc, "w1", "hello", true); err != nil {
		t.Fatalf("type: %v", err)
	}
	if got := proc.Sent(); len(got) != 1 || got[0] != "[message from alpha-lead (lead)]\n│ hello" {
		t.Fatalf("typed: %q", got)
	}

	// type: the owner's submit false leaves it unsent (no trailing \r)
	if err := f.TypeText(OwnerCaller, "w1", "draft", false); err != nil {
		t.Fatalf("type no submit: %v", err)
	}
	if got := string(proc.Written()); got != "draft" {
		t.Fatalf("typed bytes after unsent: %q", got)
	}

	// keys: named keys and ctrl+ encode to the expected VT bytes
	if err := f.SendKeys(OwnerCaller, "w1", "down enter"); err != nil {
		t.Fatalf("keys: %v", err)
	}
	if got := string(proc.Written()); got != "draft\x1b[B\r" {
		t.Fatalf("keys bytes: %q", got)
	}
	if err := f.SendKeys(lc, "w1", "nope-not-a-key"); KindOf(err) != KindInvalid {
		t.Fatalf("unknown key: %v", err)
	}

	// screen: renders the replayed terminal output as text
	proc.SetReplay([]byte("ready>"))
	got, err := f.Screen(lc, "w1", 0)
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if got := strings.TrimRight(got, "\n"); got != "ready>" {
		t.Fatalf("screen text: %q", got)
	}

	// screen: a read-only call never counts as human input (it must not hold back mail)
	f.mu.Lock()
	f.agents[w.ID].humanInputAt = time.Time{}
	f.mu.Unlock()
	if _, err := f.Screen(lc, "w1", 0); err != nil {
		t.Fatalf("screen: %v", err)
	}
	f.mu.Lock()
	humanInputAt := f.agents[w.ID].humanInputAt
	f.mu.Unlock()
	if !humanInputAt.IsZero() {
		t.Fatal("screen set humanInputAt")
	}

	// screen: history is bounded before the rows arithmetic
	if _, err := f.Screen(lc, "w1", -1); KindOf(err) != KindInvalid {
		t.Fatalf("negative history: %v", err)
	}
	if _, err := f.Screen(lc, "w1", maxScreenHistory+1); KindOf(err) != KindInvalid {
		t.Fatalf("oversized history: %v", err)
	}
	if _, err := f.Screen(lc, "w1", maxScreenHistory); err != nil {
		t.Fatalf("max history: %v", err)
	}

	// type/keys: while an owner-only approval is pending on the target, only the owner may
	// type/press keys - a lead must not be able to press Enter/y on its worker's own menu
	proc.Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{
		ID: "perm-1", Kind: model.ApprovalPermission, Tool: "bash", Description: "rm -rf /tmp/x"}})
	eventually(t, "w1 waiting on its own approval", func() bool {
		x, _ := f.Agent("w1")
		return x.State == model.StateWaiting
	})
	if err := f.TypeText(lc, "w1", "y", true); KindOf(err) != KindForbidden {
		t.Fatalf("lead typed past a pending approval: %v", err)
	}
	if err := f.SendKeys(lc, "w1", "esc"); KindOf(err) != KindForbidden {
		t.Fatalf("lead sent keys past a pending approval: %v", err)
	}
	// Screen stays readable; it does not race the approval gate.
	if _, err := f.Screen(lc, "w1", 0); err != nil {
		t.Fatalf("screen while waiting: %v", err)
	}
	// the owner is exempt from the gate: it may still type past its own pending approval
	if err := f.TypeText(OwnerCaller, "w1", "y", true); err != nil {
		t.Fatalf("owner typed past its own pending approval: %v", err)
	}

	// tier rule: a worker may not type/press keys/read the screen of a sibling or its lead
	if err := f.TypeText(wc, "alpha-lead", "x", true); KindOf(err) != KindForbidden {
		t.Fatalf("worker typed into its lead: %v", err)
	}
	if err := f.SendKeys(wc, "alpha-lead", "enter"); KindOf(err) != KindForbidden {
		t.Fatalf("worker sent keys to its lead: %v", err)
	}
	if _, err := f.Screen(wc, "alpha-lead", 0); KindOf(err) != KindForbidden {
		t.Fatalf("worker read its lead's screen: %v", err)
	}
	// the owner and the lead (an ancestor) may act on the worker
	if err := f.TypeText(OwnerCaller, "w1", "owner says hi", true); err != nil {
		t.Fatalf("owner type: %v", err)
	}
}
