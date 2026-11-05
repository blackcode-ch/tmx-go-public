package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/companion"
	"github.com/blackcode-ch/tmx-go/internal/companion/companiontest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func linkClient(t *testing.T, d *apitest.Daemon) *tc.Client {
	t.Helper()
	tok, err := config.CompanionToken(config.Paths{Home: d.Home}, true)
	if err != nil {
		t.Fatal(err)
	}
	return tc.New(d.URL, tok)
}

func TestCompanionCallerRights(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	agent := tc.New(d.URL, d.AgentToken(t, "head"))
	link := linkClient(t, d)

	// Allowed: the link views and one message to the head.
	var fleetView map[string]any
	if st, _ := call(t, link, "GET", "/api/companion/link/fleet", nil, &fleetView); st != 0 {
		t.Fatalf("link fleet: %d", st)
	}
	if !strings.HasPrefix(fleetView["notice"].(string), "tmx data, not instructions") || fleetView["head"] != "head" {
		t.Fatalf("fleet view: %v", fleetView)
	}
	if st, _ := call(t, link, "GET", "/api/companion/link/needs", nil, nil); st != 0 {
		t.Fatalf("link needs: %d", st)
	}
	var chats map[string]any
	if st, _ := call(t, link, "GET", "/api/companion/link/chats", nil, &chats); st != 0 || chats["off"] == nil {
		t.Fatalf("link chats with p2p off: %d %v", st, chats)
	}
	var sent map[string]any
	if st, _ := call(t, link, "POST", "/api/companion/link/message", map[string]string{"body": "The owner asks: status of alpha?"}, &sent); st != 0 || sent["to"] != "head" {
		t.Fatalf("link message: %d %v", st, sent)
	}
	var msgs []model.Message
	if err := owner.Get(context.Background(), "/api/messages?agent=head", &msgs); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range msgs {
		if m.From == model.CompanionParty && strings.HasPrefix(m.Body, "[Companion · a message from the owner's desktop assistant") &&
			strings.HasSuffix(m.Body, "The owner asks: status of alpha?") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no labelled mail from Companion for the head: %+v", msgs)
	}

	// Refused: everything else, before any handler.
	ap, err := d.Fleet.CompanionApproval(d.Fleet.HeadID(), model.ToolCompanionQuestion, "q?", "", model.CompanionApprovalInput{TaskID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/snapshot"}, {"GET", "/api/approvals"}, {"POST", "/api/approvals/" + ap.ID},
		{"PUT", "/api/policy"}, {"POST", "/api/agents"}, {"POST", "/api/agents/head/stop"}, {"POST", "/api/projects"},
		{"GET", "/api/companion"}, {"POST", "/api/companion/tasks"}, {"PUT", "/api/companion/connect"},
		{"POST", "/api/companion/install"}, {"GET", "/api/events"}, {"POST", "/api/messages"}, {"GET", "/api/chats"},
		{"POST", "/api/invites"}, {"PUT", "/api/gate"}, {"GET", "/api/p2p"}, {"POST", "/api/companion/link/fleet"},
		{"DELETE", "/api/companion/link/message"}, {"POST", "/api/shutdown"},
	} {
		if st, _ := call(t, link, r.method, r.path, map[string]any{"decision": "allow"}, nil); st != http.StatusForbidden {
			t.Errorf("Companion %s %s: %d, want 403", r.method, r.path, st)
		}
	}
	if got, _ := d.Fleet.ApprovalByID(ap.ID); got.State != model.ApprovalPending {
		t.Fatal("Companion changed an approval")
	}
	// Only Companion speaks on the link routes: not an agent (it would pose as Companion), not the owner.
	if st, _ := call(t, agent, "POST", "/api/companion/link/message", map[string]string{"body": "x"}, nil); st != http.StatusForbidden {
		t.Errorf("agent on the link: %d", st)
	}
	if st, _ := call(t, owner, "GET", "/api/companion/link/fleet", nil, nil); st != http.StatusForbidden {
		t.Errorf("owner on the link: %d", st)
	}
	// Rotation: the old token stops at once.
	if st, _ := call(t, owner, "POST", "/api/companion/link-token", nil, nil); st != 0 {
		t.Fatalf("rotate: %d", st)
	}
	time.Sleep(10 * time.Millisecond)
	if st, _ := call(t, link, "GET", "/api/companion/link/fleet", nil, nil); st != http.StatusUnauthorized {
		t.Fatalf("old link token after rotation: %d", st)
	}
	if st, _ := call(t, linkClient(t, d), "GET", "/api/companion/link/fleet", nil, nil); st != 0 {
		t.Fatalf("new link token: %d", st)
	}
}

func TestCompanionInstallAPI(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	if st, _ := call(t, owner, "POST", "/api/companion/install", nil, nil); st != http.StatusPreconditionFailed {
		t.Fatalf("install without Companion's folder: %d", st)
	}
	_ = mkdir(t, d.AIOSHome)
	var st companion.Status
	if code, _ := call(t, owner, "POST", "/api/companion/install", nil, &st); code != 0 || !st.Install.Installed || !st.LinkToken {
		t.Fatalf("install: %d %+v", code, st)
	}
	if !strings.HasPrefix(st.Install.MCPPath, d.AIOSHome) {
		t.Fatalf("installed outside the test's AIOS folder: %s", st.Install.MCPPath)
	}
	if code, _ := call(t, owner, "DELETE", "/api/companion/install", nil, &st); code != 0 || st.Install.Installed || st.LinkToken {
		t.Fatalf("uninstall: %d %+v", code, st)
	}
}

// connect points the daemon's Companion service at a fake Companion.
func connect(t *testing.T, d *apitest.Daemon) *companiontest.Fake {
	t.Helper()
	f := companiontest.New(t, "agent-key-123")
	owner := tc.New(d.URL, d.OwnerToken)
	var st companion.Status
	if code, _ := call(t, owner, "PUT", "/api/companion/connect", map[string]string{"url": f.URL, "token": "agent-key-123"}, &st); code != 0 {
		t.Fatalf("connect: %d", code)
	}
	if !st.Connected || !st.Reachable || st.KeyOK == nil || !*st.KeyOK || st.Label != "tmx" || st.Address.Source != companion.SourceConfig {
		t.Fatalf("status after connect: %+v", st)
	}
	return f
}

func waitTask(t *testing.T, owner *tc.Client, id string, want model.CompanionTaskState) model.CompanionTask {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var task model.CompanionTask
	for time.Now().Before(deadline) {
		if err := owner.Get(context.Background(), "/api/companion/tasks/"+id, &task); err != nil {
			t.Fatal(err)
		}
		if task.State == want {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s: %s (%s), want %s", id, task.State, task.Error, want)
	return task
}

func mailFromCompanion(t *testing.T, owner *tc.Client, agent, contains string) model.Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var msgs []model.Message
		if err := owner.Get(context.Background(), "/api/messages?agent="+agent, &msgs); err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if m.From == model.CompanionParty && strings.Contains(m.Body, contains) {
				return m
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no mail from Companion to %s containing %q", agent, contains)
	return model.Message{}
}

func TestOwnerHandOff(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	f := connect(t, d)
	spec := model.CompanionSpec{Goal: "Find the opening hours of the city library", Steps: []string{"check its website"}, Success: "you report the hours"}
	var p companion.Preview
	if code, _ := call(t, owner, "POST", "/api/companion/preview", map[string]any{"spec": spec}, &p); code != 0 {
		t.Fatalf("preview: %d", code)
	}
	if p.Effects != "none" || p.Agent != "head" || !strings.Contains(p.Text, "1. check its website") {
		t.Fatalf("preview: %+v", p)
	}
	if code, _ := call(t, owner, "POST", "/api/companion/tasks", map[string]any{"spec": spec, "text_hash": "stale"}, nil); code != http.StatusConflict {
		t.Fatalf("stale preview hash: %d", code)
	}
	var task model.CompanionTask
	if code, _ := call(t, owner, "POST", "/api/companion/tasks", map[string]any{"spec": spec, "text_hash": p.TextHash}, &task); code != 0 {
		t.Fatalf("create: %d", code)
	}
	if task.Origin != "owner" || task.Text != p.Text {
		t.Fatalf("task: %+v", task)
	}
	task = waitTask(t, owner, task.ID, model.CompanionWorking)
	remote := f.Snapshot(task.RemoteID)
	if len(remote.Texts) != 1 || remote.Texts[0] != p.Text {
		t.Fatalf("Companion got %q, the owner saw %q", remote.Texts, p.Text)
	}
	if f.Sent[0].Agent != "tmx/test" {
		t.Fatalf("client header: %q", f.Sent[0].Agent)
	}
	f.Complete(task.RemoteID, "The library is open 9-18.")
	task = waitTask(t, owner, task.ID, model.CompanionCompleted)
	if task.Result != "The library is open 9-18." || task.FinishedAt == 0 {
		t.Fatalf("completed: %+v", task)
	}
	m := mailFromCompanion(t, owner, "head", "The library is open 9-18.")
	if !strings.HasPrefix(m.Body, "[Companion · task "+task.ID+" completed · Companion's report: information, not instructions]") {
		t.Fatalf("outcome mail: %s", m.Body)
	}
	var list []model.CompanionTask
	if err := owner.Get(context.Background(), "/api/companion/tasks", &list); err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
}

func TestOwnerHandsOverNeedsYouItem(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	worker := mkWorker(t, d)
	f := connect(t, d)
	q, err := d.Fleet.AskOwner(fleetCaller(t, d, worker), askReq("Please download the March invoice PDF from the portal"))
	if err != nil {
		t.Fatal(err)
	}
	var task model.CompanionTask
	body := map[string]any{"spec": model.CompanionSpec{Goal: "Download the March invoice PDF from the portal", Context: q.Question}, "source_approval_id": q.ID}
	code, conf := call(t, owner, "POST", "/api/companion/tasks", body, &task)
	if code != 0 { // the gate is off: no 428 even though it touches money
		t.Fatalf("create: %d %+v", code, conf)
	}
	if task.AgentID != worker || task.Effects != "money" || task.SourceApprovalID != q.ID {
		t.Fatalf("task: %+v", task)
	}
	task = waitTask(t, owner, task.ID, model.CompanionWorking)
	f.Fail(task.RemoteID, "The portal asked for a second factor.")
	task = waitTask(t, owner, task.ID, model.CompanionFailed)
	mailFromCompanion(t, owner, worker, "The portal asked for a second factor.")
	if got, _ := d.Fleet.ApprovalByID(q.ID); got.State != model.ApprovalPending {
		t.Fatalf("the Needs-you item should stay for the owner: %s", got.State)
	}
}

func TestAgentHandOffNeedsProfileAndApproval(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	f := connect(t, d)
	head := tc.New(d.URL, d.AgentToken(t, "head"))
	spec := map[string]any{"spec": model.CompanionSpec{Goal: "Look up tomorrow's weather in Zurich"}}
	if code, _ := call(t, head, "POST", "/api/companion/tasks", spec, nil); code != http.StatusForbidden {
		t.Fatalf("hand-off with the tool off: %d", code)
	}
	enableCompanion(t, owner, "head")

	// Declined.
	var task model.CompanionTask
	if code, _ := call(t, head, "POST", "/api/companion/tasks", spec, &task); code != 0 {
		t.Fatalf("hand-off: %d", code)
	}
	if task.State != model.CompanionAwaitingApproval || task.Origin != "agent" || task.ApprovalID == "" {
		t.Fatalf("task: %+v", task)
	}
	ap, _ := d.Fleet.ApprovalByID(task.ApprovalID)
	var in model.CompanionApprovalInput
	_ = json.Unmarshal(ap.Input, &in)
	if ap.Kind != model.ApprovalCompanion || ap.Tool != model.ToolCompanionTask || in.Text != task.Text || in.TaskID != task.ID {
		t.Fatalf("approval: %+v %+v", ap, in)
	}
	// Companion cannot see the task text or decide it; agents cannot read other tasks.
	if code, _ := call(t, owner, "POST", "/api/approvals/"+ap.ID, map[string]string{"decision": "deny", "message": "not now"}, nil); code != 0 {
		t.Fatalf("deny: %d", code)
	}
	task = waitTask(t, owner, task.ID, model.CompanionDenied)
	mailFromCompanion(t, owner, "head", "The owner declined to hand your task to Companion. Their note: not now")
	if got, _ := d.Fleet.ApprovalByID(ap.ID); got.State != model.ApprovalDenied {
		t.Fatalf("approval after deny: %s", got.State)
	}
	if f.SentCount() != 0 {
		t.Fatal("a declined task reached Companion")
	}

	// Allowed, then Companion asks, the owner answers, Companion finishes.
	if code, _ := call(t, head, "POST", "/api/companion/tasks", spec, &task); code != 0 {
		t.Fatalf("hand-off 2: %d", code)
	}
	if code, _ := call(t, owner, "POST", "/api/approvals/"+task.ApprovalID, map[string]string{"decision": "allow"}, nil); code != 0 {
		t.Fatalf("allow: %d", code)
	}
	task = waitTask(t, owner, task.ID, model.CompanionWorking)
	f.Ask(task.RemoteID, "Celsius or Fahrenheit?")
	task = waitTask(t, owner, task.ID, model.CompanionInputRequired)
	if task.Question != "Celsius or Fahrenheit?" || task.QuestionApprovalID == "" {
		t.Fatalf("question: %+v", task)
	}
	qa, _ := d.Fleet.ApprovalByID(task.QuestionApprovalID)
	if qa.Kind != model.ApprovalCompanion || qa.Tool != model.ToolCompanionQuestion || qa.Question != "Celsius or Fahrenheit?" || qa.State != model.ApprovalPending {
		t.Fatalf("question approval: %+v", qa)
	}
	if code, _ := call(t, owner, "POST", "/api/approvals/"+qa.ID, map[string]string{"decision": "answer", "answer": "Celsius"}, nil); code != 0 {
		t.Fatalf("answer: %d", code)
	}
	if r := f.Snapshot(task.RemoteID); len(r.Texts) != 2 || r.Texts[1] != "Celsius" {
		t.Fatalf("Companion's messages: %q", r.Texts)
	}
	if got, _ := d.Fleet.ApprovalByID(qa.ID); got.State != model.ApprovalAnswered || got.Answer != "Celsius" {
		t.Fatalf("question approval after answer: %+v", got)
	}
	task = waitTask(t, owner, task.ID, model.CompanionWorking)
	f.Complete(task.RemoteID, "12 °C and sunny.")
	waitTask(t, owner, task.ID, model.CompanionCompleted)
	mailFromCompanion(t, owner, "head", "12 °C and sunny.")

	// The agent reads its own task; another agent does not see it.
	if code, _ := call(t, head, "GET", "/api/companion/tasks/"+task.ID, nil, nil); code != 0 {
		t.Fatalf("own task: %d", code)
	}
	other := tc.New(d.URL, d.AgentToken(t, mkWorker(t, d)))
	if code, _ := call(t, other, "GET", "/api/companion/tasks/"+task.ID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("someone else's task: %d", code)
	}
	if code, _ := call(t, other, "GET", "/api/companion/tasks", nil, nil); code != http.StatusForbidden {
		t.Fatalf("agent listing tasks: %d", code)
	}
}

func TestCompanionQuestionDenyCancels(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	f := connect(t, d)
	var task model.CompanionTask
	if code, _ := call(t, owner, "POST", "/api/companion/tasks", map[string]any{"spec": model.CompanionSpec{Goal: "Tidy the desktop"}}, &task); code != 0 {
		t.Fatal(code)
	}
	task = waitTask(t, owner, task.ID, model.CompanionWorking)
	f.Ask(task.RemoteID, "Which folder?")
	task = waitTask(t, owner, task.ID, model.CompanionInputRequired)
	if code, _ := call(t, owner, "POST", "/api/approvals/"+task.QuestionApprovalID, map[string]string{"decision": "deny"}, nil); code != 0 {
		t.Fatalf("deny question: %d", code)
	}
	waitTask(t, owner, task.ID, model.CompanionCanceled)
	if r := f.Snapshot(task.RemoteID); r.State != "TASK_STATE_CANCELED" {
		t.Fatalf("Companion's task not cancelled: %s", r.State)
	}
	if got, _ := d.Fleet.ApprovalByID(task.QuestionApprovalID); got.State != model.ApprovalDenied {
		t.Fatalf("question approval: %s", got.State)
	}
}

func TestCompanionGate(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	f := connect(t, d)
	enableCompanion(t, owner, "head")
	turnGateOn(t, d, owner)

	// The owner's own task touching money: 428, then confirmed.
	body := map[string]any{"spec": model.CompanionSpec{Goal: "Pay the electricity invoice", Effects: "money"}}
	code, conf := call(t, owner, "POST", "/api/companion/tasks", body, nil)
	if code != http.StatusPreconditionRequired || !strings.Contains(conf.Reason, "may spend or move money") {
		t.Fatalf("owner money task: %d %+v", code, conf)
	}
	var task model.CompanionTask
	if code, _ := call(t, confirmed(t, owner, conf), "POST", "/api/companion/tasks", body, &task); code != 0 {
		t.Fatalf("after confirming: %d", code)
	}
	task = waitTask(t, owner, task.ID, model.CompanionWorking)
	f.Complete(task.RemoteID, "paid")
	waitTask(t, owner, task.ID, model.CompanionCompleted)

	// An agent's hand-off touching accounts: allowing needs a proof; denying never does.
	head := tc.New(d.URL, d.AgentToken(t, "head"))
	if code, _ := call(t, head, "POST", "/api/companion/tasks", map[string]any{"spec": model.CompanionSpec{Goal: "Reset the password of the router"}}, &task); code != 0 {
		t.Fatal(code)
	}
	if task.Effects != "account" {
		t.Fatalf("effects: %s", task.Effects)
	}
	code, conf = call(t, owner, "POST", "/api/approvals/"+task.ApprovalID, map[string]string{"decision": "allow"}, nil)
	if code != http.StatusPreconditionRequired || !strings.Contains(conf.Reason, "let head hand Companion a task") {
		t.Fatalf("allow without proof: %d %+v", code, conf)
	}
	if code, _ := call(t, confirmed(t, owner, conf), "POST", "/api/approvals/"+task.ApprovalID, map[string]string{"decision": "allow"}, nil); code != 0 {
		t.Fatalf("allow with proof: %d", code)
	}
	waitTask(t, owner, task.ID, model.CompanionWorking)
	// A harmless one passes without a prompt.
	if code, _ := call(t, head, "POST", "/api/companion/tasks", map[string]any{"spec": model.CompanionSpec{Goal: "Read the weather"}}, &task); code != 0 {
		t.Fatal(code)
	}
	if code, _ := call(t, owner, "POST", "/api/approvals/"+task.ApprovalID, map[string]string{"decision": "deny"}, nil); code != 0 {
		t.Fatalf("deny with the gate on: %d", code)
	}
}

func TestCompanionRefusalBusyAndCancel(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	f := connect(t, d)

	// Refused before running: respected, never retried.
	f.Refuse("Only the person may choose a model.")
	var task model.CompanionTask
	if code, _ := call(t, owner, "POST", "/api/companion/tasks", map[string]any{"spec": model.CompanionSpec{Goal: "Do a thing"}}, &task); code != 0 {
		t.Fatal(code)
	}
	task = waitTask(t, owner, task.ID, model.CompanionRefused)
	if !strings.Contains(task.Error, "Only the person may choose a model.") {
		t.Fatalf("refusal: %s", task.Error)
	}
	mailFromCompanion(t, owner, "head", "Companion refused the task: Only the person may choose a model.")
	time.Sleep(100 * time.Millisecond)
	if n := f.SentCount(); n != 1 {
		t.Fatalf("a refused task was sent %d times", n)
	}
	f.Refuse("")

	// Busy: retried until Companion takes it.
	f.Busy(2)
	if code, _ := call(t, owner, "POST", "/api/companion/tasks", map[string]any{"spec": model.CompanionSpec{Goal: "Second"}}, &task); code != 0 {
		t.Fatal(code)
	}
	task = waitTask(t, owner, task.ID, model.CompanionWorking)
	if task.Attempts != 3 {
		t.Fatalf("attempts: %d", task.Attempts)
	}
	// One at a time: a third waits while the second runs.
	var third model.CompanionTask
	if code, _ := call(t, owner, "POST", "/api/companion/tasks", map[string]any{"spec": model.CompanionSpec{Goal: "Third"}}, &third); code != 0 {
		t.Fatal(code)
	}
	time.Sleep(150 * time.Millisecond)
	if got := waitTask(t, owner, third.ID, model.CompanionQueued); got.Attempts != 0 {
		t.Fatalf("third was sent while the second ran: %+v", got)
	}
	// Cancelling the running one cancels it in Companion; then the third goes.
	if code, _ := call(t, owner, "POST", "/api/companion/tasks/"+task.ID+"/cancel", nil, &task); code != 0 || task.State != model.CompanionCanceled {
		t.Fatalf("cancel: %d %s", code, task.State)
	}
	if r := f.Snapshot(task.RemoteID); r.State != "TASK_STATE_CANCELED" {
		t.Fatalf("not cancelled in Companion: %s", r.State)
	}
	waitTask(t, owner, third.ID, model.CompanionWorking)

	// Companion gone: a queued task waits (nothing sent), reporting why.
	f.Close()
	var fourth model.CompanionTask
	_, _ = d.Companion.Cancel(third.ID, "")
	if code, _ := call(t, owner, "POST", "/api/companion/tasks", map[string]any{"spec": model.CompanionSpec{Goal: "Fourth"}}, &fourth); code != 0 {
		t.Fatal(code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = owner.Get(context.Background(), "/api/companion/tasks/"+fourth.ID, &fourth)
		if fourth.Attempts > 0 && fourth.State == model.CompanionQueued && strings.Contains(fourth.Error, "not running") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fourth.State != model.CompanionQueued || !strings.Contains(fourth.Error, "not running") {
		t.Fatalf("with Companion gone: %+v", fourth)
	}
	var st companion.Status
	_ = owner.Get(context.Background(), "/api/companion", &st)
	if st.Reachable || st.Error == "" || st.Active != 1 {
		t.Fatalf("status with Companion gone: %+v", st)
	}
}

func TestCompanionNotConnectedWaits(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	var task model.CompanionTask
	if code, _ := call(t, owner, "POST", "/api/companion/tasks", map[string]any{"spec": model.CompanionSpec{Goal: "x"}}, &task); code != 0 {
		t.Fatal(code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(task.Error, "not connected") {
		_ = owner.Get(context.Background(), "/api/companion/tasks/"+task.ID, &task)
		time.Sleep(10 * time.Millisecond)
	}
	if task.State != model.CompanionQueued || !strings.Contains(task.Error, "not connected") {
		t.Fatalf("unconnected: %+v", task)
	}
	var st companion.Status
	if err := owner.Get(context.Background(), "/api/companion?probe=0", &st); err != nil || st.Connected || st.Checked {
		t.Fatalf("status: %+v %v", st, err)
	}
}

// A key Companion no longer accepts (the owner rotated or removed it, or Companion switched
// account) is a connection problem: tasks wait for a working key instead of being refused, a
// key Companion refuses is not stored, and connecting a good key sends the waiting tasks (#3).
func TestCompanionReplacedKeyWaitsAndResumes(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	f := connect(t, d)
	f.SetToken("agent-key-new") // rotated in Companion; tmx still has the old one

	var a, b model.CompanionTask
	for _, p := range []*model.CompanionTask{&a, &b} {
		if code, _ := call(t, owner, "POST", "/api/companion/tasks", map[string]any{"spec": model.CompanionSpec{Goal: "Task " + model.NewID()}}, p); code != 0 {
			t.Fatal(code)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(a.Error, "does not accept tmx's key") {
		_ = owner.Get(context.Background(), "/api/companion/tasks/"+a.ID, &a)
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	_ = owner.Get(context.Background(), "/api/companion/tasks/"+b.ID, &b)
	for _, x := range []model.CompanionTask{a, b} {
		if x.State != model.CompanionQueued {
			t.Fatalf("a task with a refused key: %s (%s), want queued", x.State, x.Error)
		}
	}
	if !strings.Contains(a.Error, "Connect again with a new key") {
		t.Fatalf("the waiting task does not say what to do: %q", a.Error)
	}
	if n := f.SentCount(); n != 0 {
		t.Fatalf("Companion ran %d sends with a refused key", n)
	}
	var st companion.Status
	_ = owner.Get(context.Background(), "/api/companion", &st)
	if st.KeyOK == nil || *st.KeyOK || !strings.Contains(st.Error, "does not recognise this key") {
		t.Fatalf("status with a refused key: %+v", st)
	}

	// A key Companion refuses is not stored: the old one stays.
	key := func() string { k, _ := config.ReadSecret(d.Companion.Paths().CompanionAgentTokenFile()); return k }
	if code, _ := call(t, owner, "PUT", "/api/companion/connect", map[string]string{"url": f.URL, "token": "mistyped-key"}, nil); code != http.StatusBadRequest {
		t.Fatalf("connecting a key Companion refuses: %d, want 400", code)
	}
	if key() != "agent-key-123" {
		t.Fatalf("a refused key was stored: %q", key())
	}

	// The new key: the waiting tasks go out, oldest first, none refused.
	if code, _ := call(t, owner, "PUT", "/api/companion/connect", map[string]string{"token": "agent-key-new"}, &st); code != 0 {
		t.Fatalf("connect: %d", code)
	}
	if st.KeyOK == nil || !*st.KeyOK || st.Address.URL != f.URL {
		t.Fatalf("status after reconnecting: %+v", st)
	}
	a = waitTask(t, owner, a.ID, model.CompanionWorking)
	f.Complete(a.RemoteID, "done")
	waitTask(t, owner, a.ID, model.CompanionCompleted)
	waitTask(t, owner, b.ID, model.CompanionWorking)
}

// Companion unable to check any key (its list of agents unreadable, a 503 from its auth layer)
// ran nothing: the task is retried, never written off as unconfirmed.
func TestCompanionKeyCheckDownRetries(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	f := connect(t, d)
	f.RegistryDown(true)
	var task model.CompanionTask
	if code, _ := call(t, owner, "POST", "/api/companion/tasks", map[string]any{"spec": model.CompanionSpec{Goal: "x"}}, &task); code != 0 {
		t.Fatal(code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !(task.Attempts > 0 && task.State == model.CompanionQueued) {
		_ = owner.Get(context.Background(), "/api/companion/tasks/"+task.ID, &task)
		time.Sleep(10 * time.Millisecond)
	}
	// The synthetic key contains "agen", also present in the server's "agents"
	// diagnostic. The key-echo guard withholds that text but must retain retry.
	if task.State != model.CompanionQueued || !strings.Contains(task.Error, "withheld because it contained key material") || !strings.Contains(task.Error, "tmx tries again") {
		t.Fatalf("with Companion's key check down: %s (%s), want queued", task.State, task.Error)
	}
	f.RegistryDown(false)
	waitTask(t, owner, task.ID, model.CompanionWorking)
}

// An address configured earlier that no longer answers is dropped when connecting again
// without one: tmx looks where Companion advertises itself (its engine.json) instead.
func TestCompanionReconnectDropsStaleAddress(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	old := connect(t, d) // configured: old.URL
	old.Close()          // Companion restarted elsewhere
	moved := companiontest.New(t, "agent-key-123")
	engine := filepath.Join(os.Getenv("TMXG_COMPANION_DATA"), "agents", "engine.json")
	mkdir(t, filepath.Dir(engine))
	b, _ := json.Marshal(map[string]any{"url": moved.URL, "pid": os.Getpid()})
	if err := os.WriteFile(engine, b, 0o600); err != nil {
		t.Fatal(err)
	}
	var st companion.Status
	if code, _ := call(t, owner, "PUT", "/api/companion/connect", map[string]string{"token": "agent-key-123"}, &st); code != 0 {
		t.Fatalf("connect: %d", code)
	}
	if st.Address.URL != moved.URL || st.Address.Source != companion.SourceEngine || st.KeyOK == nil || !*st.KeyOK {
		t.Fatalf("after reconnecting: %+v", st)
	}
	if c, err := config.Load(d.Companion.Paths()); err != nil {
		t.Fatal(err)
	} else if c.Companion != nil && c.Companion.URL != "" {
		t.Fatalf("the stale address is still configured: %+v", c.Companion)
	}
}

// POST /api/companion/open-connect: the owner only; installs into Companion when needed, then
// hands Companion's connect link to the OS (a fake here: nothing is ever opened).
func TestCompanionOpenConnect(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	agent := tc.New(d.URL, d.AgentToken(t, "head"))
	link := linkClient(t, d)
	for name, c := range map[string]*tc.Client{"agent": agent, "Companion's link": link} {
		if code, _ := call(t, c, "POST", "/api/companion/open-connect", nil, nil); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
	if code, _ := call(t, owner, "POST", "/api/companion/open-connect", nil, nil); code != http.StatusPreconditionFailed {
		t.Fatalf("without Companion's folder: %d, want 412", code)
	}
	if got := d.Opened(); len(got) != 0 {
		t.Fatalf("opened %v before Companion was set up, or for someone other than the owner", got)
	}

	_ = mkdir(t, d.AIOSHome)
	req, _ := http.NewRequest("POST", d.URL+"/api/companion/open-connect", nil)
	req.Header.Set("Authorization", "Bearer "+d.OwnerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var res companion.OpenConnectResult
	_ = json.NewDecoder(resp.Body).Decode(&res)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || !res.Installed || !res.Opened || res.Error != "" {
		t.Fatalf("open-connect: %d %+v", resp.StatusCode, res)
	}
	if got := d.Opened(); len(got) != 1 || got[0] != companion.DeepLink || companion.DeepLink != "aios://connect/tmx" {
		t.Fatalf("opened %v", got)
	}
	var e companion.Entry
	b, err := os.ReadFile(filepath.Join(d.AIOSHome, "mcps", companion.Name, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &e); err != nil || e.ManagedBy != companion.Marker || e.Env["TMXG_HOME"] != d.Home || e.Env["TMXG_URL"] != d.URL {
		t.Fatalf("Companion finds tmx (and the binary it runs to connect) in this entry: %+v %v", e, err)
	}
	if tok, _ := config.ReadSecret(filepath.Join(d.Home, "secrets", "companion-token")); tok == "" {
		t.Fatal("no link token after installing")
	}
	// Installed and current: not written again.
	if code, _ := call(t, owner, "POST", "/api/companion/open-connect", nil, &res); code != 0 || res.Installed || !res.Opened {
		t.Fatalf("again: %d %+v", code, res)
	}
	// No app takes Companion's links: said so, nothing else changes.
	d.OnOpen(func(string) error {
		return errors.New("Companion did not open: no app on this computer opens Companion's links")
	})
	if code, _ := call(t, owner, "POST", "/api/companion/open-connect", nil, &res); code != 0 || res.Opened || !strings.Contains(res.Error, "did not open") {
		t.Fatalf("link not opened: %d %+v", code, res)
	}
	// Someone else's entry named tmx: left alone, nothing opened.
	d.OnOpen(nil)
	n := len(d.Opened())
	if err := os.WriteFile(filepath.Join(d.AIOSHome, "mcps", companion.Name, "mcp.json"), []byte(`{"command":"/usr/bin/other","args":["serve"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, owner, "POST", "/api/companion/open-connect", nil, nil); code != http.StatusConflict || len(d.Opened()) != n {
		t.Fatalf("foreign entry: %d, opened %d → %d", code, n, len(d.Opened()))
	}
}

// --- helpers ---

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// mkWorker launches a worker (in a new project) and returns its ID.
func mkWorker(t *testing.T, d *apitest.Daemon) string {
	t.Helper()
	owner := tc.New(d.URL, d.OwnerToken)
	var p model.Project
	name := "p" + model.NewID()
	if code, _ := call(t, owner, "POST", "/api/projects", map[string]string{"name": name}, &p); code != 0 {
		t.Fatalf("project: %d", code)
	}
	var a model.Agent
	if code, _ := call(t, owner, "POST", "/api/agents", map[string]string{"name": "w" + model.NewID(), "project_id": p.ID, "level": "worker"}, &a); code != 0 {
		t.Fatalf("worker: %d", code)
	}
	return a.ID
}

func fleetCaller(t *testing.T, d *apitest.Daemon, id string) fleet.Caller {
	t.Helper()
	return fleet.AgentCaller(id)
}

func askReq(q string) fleet.AskRequest { return fleet.AskRequest{Question: q} }

// enableCompanion turns companion_tasks on in a profile.
func enableCompanion(t *testing.T, owner *tc.Client, slug string) {
	t.Helper()
	var p model.Profile
	if err := owner.Get(context.Background(), "/api/profiles/"+slug, &p); err != nil {
		t.Fatal(err)
	}
	p.CompanionTasks = true
	var out model.Profile
	if code, _ := call(t, owner, "PUT", "/api/profiles/"+slug, p, &out); code != 0 || !out.CompanionTasks {
		t.Fatalf("enable companion tasks: %d %+v", code, out)
	}
}

func turnGateOn(t *testing.T, d *apitest.Daemon, owner *tc.Client) {
	t.Helper()
	on := map[string]bool{"enabled": true}
	code, conf := call(t, owner, "PUT", "/api/gate", on, nil)
	if code != http.StatusPreconditionRequired {
		t.Fatalf("gate on: %d", code)
	}
	if code, _ := call(t, confirmed(t, owner, conf), "PUT", "/api/gate", on, nil); code != 0 {
		t.Fatalf("gate on with proof: %d", code)
	}
	waitIdle(t, d.Gate, owner)
}
