package fleet

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestCompanionSendTotalBudget(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	project := workerProject(t, f)
	for _, name := range []string{"first", "second"} {
		if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: project, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	var first model.Message
	for i := range MaxPerWindow {
		to := "first"
		if i%2 != 0 {
			to = "second"
		}
		m, err := f.CompanionSend(SendRequest{To: to, Body: "hello", RetryID: fmt.Sprint(i)})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = m
		}
	}
	if _, err := f.CompanionSend(SendRequest{To: "second", Body: "over budget"}); KindOf(err) != KindTooMany {
		t.Fatalf("total budget: %v", err)
	}
	if m, err := f.CompanionSend(SendRequest{To: "first", Body: "hello", RetryID: "0"}); err != nil || m.ID != first.ID {
		t.Fatalf("receipt at budget: %+v %v", m, err)
	}
}

func TestCompanionSendRecipientAndRetry(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	a, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "target"})
	if err != nil {
		t.Fatal(err)
	}
	req := SendRequest{To: " target@" + f.opts.Machine + " ", Body: "hello", RetryID: "link-one"}
	first, err := f.CompanionSend(req)
	if err != nil || first.To != a.ID || first.From != model.CompanionParty || !strings.HasPrefix(first.Body, CompanionLabel) {
		t.Fatalf("send: %+v %v", first, err)
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			m, err := f.CompanionSend(req)
			if err != nil || m.ID != first.ID {
				t.Errorf("retry: %+v %v", m, err)
			}
		})
	}
	wg.Wait()
	changed := req
	changed.Body = "different"
	if _, err := f.CompanionSend(changed); KindOf(err) != KindConflict {
		t.Fatalf("changed: %v", err)
	}
	if err := f.RemoveAgent(OwnerCaller, a.ID); err != nil {
		t.Fatal(err)
	}
	if m, err := f.CompanionSend(req); err != nil || m.ID != first.ID || m.To != a.ID {
		t.Fatalf("receipt after deletion: %+v %v", m, err)
	}
	newSend := req
	newSend.RetryID = "new-send"
	if _, err := f.CompanionSend(newSend); KindOf(err) != KindNotFound {
		t.Fatalf("new send to deleted recipient: %v", err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "target"}); err != nil {
		t.Fatal(err)
	}
	if m, err := f.CompanionSend(req); err != nil || m.ID != first.ID || m.To != a.ID {
		t.Fatalf("receipt still belongs to original recipient: %+v %v", m, err)
	}
}

func TestCompanionSendBoundaries(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	f.SetPeerSender(&fakePeers{hosts: map[string]bool{"other": true}}, "testbox")
	for _, to := range []string{"owner", "companion", "operator", "worker@other"} {
		if _, err := f.CompanionSend(SendRequest{To: to, Body: "hello"}); KindOf(err) != KindForbidden {
			t.Errorf("%s: %v", to, err)
		}
	}
	if _, err := f.SendMessage(CompanionCaller, SendRequest{To: "head", Body: "unlabelled"}); KindOf(err) != KindForbidden {
		t.Fatalf("general send: %v", err)
	}
}

func TestMailRetryReplaysBeforeRecipientLookup(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	a, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "target"})
	if err != nil {
		t.Fatal(err)
	}
	req := SendRequest{To: a.Name, Body: "hello", RetryID: "ordinary-one"}
	first, err := f.SendMessage(OwnerCaller, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveAgent(OwnerCaller, a.ID); err != nil {
		t.Fatal(err)
	}
	if m, err := f.SendMessage(OwnerCaller, req); err != nil || m.ID != first.ID {
		t.Fatalf("deleted receipt: %+v %v", m, err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "target"}); err != nil {
		t.Fatal(err)
	}
	if m, err := f.SendMessage(OwnerCaller, req); err != nil || m.ID != first.ID || m.To != a.ID {
		t.Fatalf("replaced receipt: %+v %v", m, err)
	}
}

func TestMailRetryReplaysBeforePeerLookup(t *testing.T) {
	_, f, _, _, caller := peerEnv(t)
	req := SendRequest{To: "bob@hostc", Body: "hello", RetryID: "peer-one"}
	first, err := f.SendMessage(caller, req)
	if err != nil {
		t.Fatal(err)
	}
	f.SetPeerSender(nil, "testbox")
	if m, err := f.SendMessage(caller, req); err != nil || m.ID != first.ID {
		t.Fatalf("removed peer receipt: %+v %v", m, err)
	}
}
