package fleet

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func openLinks(t *testing.T, f *Fleet, thread string) []model.ThreadLink {
	t.Helper()
	ls, err := f.ThreadLinks(OwnerCaller, thread, false)
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

func hasAgentLink(ls []model.ThreadLink, agentID, role string) bool {
	for _, l := range ls {
		if l.Axis == model.AxisAgent && l.Ref == agentID && l.Role == role && l.How == model.LinkAuto {
			return true
		}
	}
	return false
}

// M0's story S6: agents launched for a thread show up on it without anyone attaching them.
func TestMatrixLaunchLinksAgents(t *testing.T) {
	e := newEnv(t)
	f := e.started()

	p, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "Shop"})
	if err != nil {
		t.Fatal(err)
	}
	pt := model.ProjectThread(p.ID)
	if !hasAgentLink(openLinks(t, f, pt), p.LeadID, "lead") {
		t.Fatalf("the project's lead is not on its thread: %+v", openLinks(t, f, pt))
	}

	// The lead's worker joins the project's thread.
	w, err := f.LaunchAgent(Caller{AgentID: p.LeadID}, LaunchRequest{Name: "shop-worker"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasAgentLink(openLinks(t, f, pt), w.ID, "worker") {
		t.Fatalf("the worker is not on the project's thread: %+v", openLinks(t, f, pt))
	}
	// The project's lead may put an agent on its thread by hand; its worker may not.
	if _, err := f.AttachLink(Caller{AgentID: p.LeadID}, LinkRequest{Thread: pt, Axis: "agent", Ref: w.ID, Role: "reviewer"}); err != nil {
		t.Fatalf("lead putting its worker on the thread: %v", err)
	}
	if _, err := f.AttachLink(Caller{AgentID: w.ID}, LinkRequest{Thread: pt, Axis: "agent", Ref: p.LeadID, Role: "reviewer"}); KindOf(err) != KindForbidden {
		t.Fatalf("worker putting an agent on the thread: %v", err)
	}

	// A launch for a named thread joins that one.
	topic, err := f.CreateThread("Buy computers", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "buyer", ThreadID: topic.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !hasAgentLink(openLinks(t, f, topic.ID), b.ID, "worker") {
		t.Fatalf("buyer is not on its thread: %+v", openLinks(t, f, topic.ID))
	}

	// A lead can't launch onto a thread that isn't its own; nothing is created.
	if _, err := f.LaunchAgent(Caller{AgentID: p.LeadID}, LaunchRequest{Name: "sneaky", ThreadID: topic.ID}); KindOf(err) != KindForbidden {
		t.Fatalf("launch onto another thread: %v", err)
	}
	if _, err := f.Agent("sneaky"); err == nil {
		t.Fatal("a refused launch left an agent behind")
	}
	// A chat's thread concerns people outside: no agent but the head works on it.
	chat := model.Thread{ID: model.ChatThread("ch1"), Title: "With Bala", Kind: model.ThreadChat, ChatID: "ch1", CreatedAt: 1, UpdatedAt: 1}
	if err := e.st.PutThread(chat); err != nil {
		t.Fatal(err)
	}
	if _, err := f.LaunchAgent(Caller{AgentID: p.LeadID}, LaunchRequest{Name: "chatty", ThreadID: chat.ID}); KindOf(err) != KindForbidden {
		t.Fatalf("launch onto a chat's thread: %v", err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "lost", ThreadID: "no-such-thread"}); KindOf(err) != KindNotFound {
		t.Fatalf("launch onto a missing thread: %v", err)
	}
}

func TestMatrixAttachDetach(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	topic, err := f.CreateThread("Buy computers", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "buyer", ThreadID: topic.ID})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.CreateThread("Elsewhere", "")
	if err != nil {
		t.Fatal(err)
	}
	me := Caller{AgentID: b.ID}

	// An agent attaches a document to its own thread; the same twice is one link.
	doc := LinkRequest{Thread: "Buy computers", Axis: "doc", Ref: "D:/shop/shortlist.html", Why: "the three offers"}
	l1, err := f.AttachLink(me, doc)
	if err != nil || l1.By != "buyer" || l1.How != model.LinkHand || l1.ThreadID != topic.ID {
		t.Fatalf("attach %+v %v", l1, err)
	}
	if l2, err := f.AttachLink(me, doc); err != nil || l2.ID != l1.ID {
		t.Fatalf("attach twice %+v %v", l2, err)
	}

	// Not on another thread, no secrets by an agent, never a secret's value, only known axes.
	if _, err := f.AttachLink(me, LinkRequest{Thread: other.ID, Axis: "doc", Ref: "x.md"}); KindOf(err) != KindForbidden {
		t.Fatalf("another thread: %v", err)
	}
	if _, err := f.AttachLink(me, LinkRequest{Thread: topic.ID, Axis: "agent", Ref: "someone-else"}); KindOf(err) != KindForbidden {
		t.Fatalf("agent putting another agent on its thread: %v", err)
	}
	if _, err := f.AttachLink(me, LinkRequest{Thread: topic.ID, Axis: "secret", Ref: "shop-account"}); KindOf(err) != KindForbidden {
		t.Fatalf("agent attaching a secret: %v", err)
	}
	leak := "token AbCdEf0123456789AbCdEf0123456789xyz"
	for _, req := range []LinkRequest{
		{Thread: topic.ID, Axis: "secret", Ref: leak},
		{Thread: topic.ID, Axis: "secret", Ref: "shop-account", Label: leak},
		{Thread: topic.ID, Axis: "doc", Ref: "x.md", Why: leak},
	} {
		if _, err := f.AttachLink(OwnerCaller, req); KindOf(err) != KindInvalid {
			t.Fatalf("secret value %+v: %v", req, err)
		}
	}
	if _, err := f.AttachLink(OwnerCaller, LinkRequest{Thread: topic.ID, Axis: "bogus", Ref: "x"}); KindOf(err) != KindInvalid {
		t.Fatalf("bad axis: %v", err)
	}
	sec, err := f.AttachLink(OwnerCaller, LinkRequest{Thread: topic.ID, Axis: "secret", Ref: "shop-account"})
	if err != nil || sec.By != model.Owner {
		t.Fatalf("owner attaching a secret's name %+v %v", sec, err)
	}
	for _, l := range openLinks(t, f, topic.ID) {
		if strings.Contains(l.Ref+l.Label+l.Why, "AbCdEf0123") {
			t.Fatalf("a secret value reached the thread: %+v", l)
		}
	}

	// Detach: it leaves the open list and stays in the history; an agent can't detach a secret.
	if _, err := f.DetachLink(me, sec.ID); KindOf(err) != KindForbidden {
		t.Fatalf("agent detaching a secret: %v", err)
	}
	if d, err := f.DetachLink(me, l1.ID); err != nil || d.EndedAt == 0 {
		t.Fatalf("detach %+v %v", d, err)
	}
	for _, l := range openLinks(t, f, topic.ID) {
		if l.ID == l1.ID {
			t.Fatal("a detached link is still open")
		}
	}
	past, err := f.ThreadLinks(OwnerCaller, topic.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range past {
		found = found || (l.ID == l1.ID && l.EndedAt != 0)
	}
	if !found {
		t.Fatalf("the detached link left the history: %+v", past)
	}
	if _, err := f.DetachLink(OwnerCaller, "nope"); KindOf(err) != KindNotFound {
		t.Fatalf("detach missing: %v", err)
	}
}

// M1's story S9: an agent records events on the thread it works on, and the thread's view
// shows its events, not its chatter.
func TestMatrixEventsAndView(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	topic, err := f.CreateThread("Buy computers", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.CreateThread("Elsewhere", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Level: model.LevelLead, Name: "buyer", ThreadID: topic.ID})
	if err != nil {
		t.Fatal(err)
	}
	me := Caller{AgentID: b.ID}

	// No thread named: an agent outside a project files on the thread it joined.
	if _, _, err := f.AddEntry(me, AddEntryRequest{Kind: "start", Text: "Looking for two laptops"}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []AddEntryRequest{
		{Thread: "Buy computers", Kind: "info", Text: "ok thank you"},
		{Thread: "Buy computers", Kind: "result", Text: "Three offers found", Link: "D:/shop/shortlist.html"},
		{Thread: "Buy computers", Kind: "blocker", Text: "Payment needs the owner"},
	} {
		if _, _, err := f.AddEntry(me, ev); err != nil {
			t.Fatalf("%s: %v", ev.Kind, err)
		}
	}
	if _, _, err := f.AddEntry(me, AddEntryRequest{Thread: "Buy computers", Kind: "gossip", Text: "x"}); KindOf(err) != KindInvalid {
		t.Fatalf("unknown kind: %v", err)
	}
	if _, _, err := f.AddEntry(me, AddEntryRequest{Thread: other.ID, Kind: "result", Text: "x"}); KindOf(err) != KindForbidden {
		t.Fatalf("event on a thread it isn't on: %v", err)
	}

	v, err := f.View(me, topic.ID, false, false)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, ev := range v.Events {
		kinds = append(kinds, ev.Kind)
	}
	if strings.Join(kinds, ",") != "start,result,blocker" { // the order they were filed, even within a millisecond
		t.Fatalf("events %v", kinds)
	}
	if !hasAgentLink(v.Links, b.ID, "lead") {
		t.Fatalf("links %+v", v.Links)
	}
	if all, _ := f.View(OwnerCaller, topic.ID, false, true); len(all.Events) != 4 {
		t.Fatalf("with chatter: %d", len(all.Events))
	}
	if _, err := f.View(me, other.ID, false, false); KindOf(err) != KindForbidden {
		t.Fatalf("view of another thread: %v", err)
	}

	// See by: the threads an agent is on, as far as the caller may see.
	if ls, err := f.LinksTo(OwnerCaller, model.AxisAgent, b.ID); err != nil || len(ls) != 1 || ls[0].ThreadID != topic.ID {
		t.Fatalf("links to buyer %+v %v", ls, err)
	}
	stranger, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "stranger", ThreadID: other.ID})
	if err != nil {
		t.Fatal(err)
	}
	if ls, err := f.LinksTo(Caller{AgentID: stranger.ID}, model.AxisAgent, b.ID); err != nil || len(ls) != 0 {
		t.Fatalf("a stranger sees %+v %v", ls, err)
	}
	if _, err := f.LinksTo(OwnerCaller, "bogus", "x"); KindOf(err) != KindInvalid {
		t.Fatalf("bad axis: %v", err)
	}
}
