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
	if _, err := f.CompanionSend(req); KindOf(err) != KindNotFound {
		t.Fatalf("deleted recipient: %v", err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "target"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.CompanionSend(req); KindOf(err) != KindConflict {
		t.Fatalf("replacement recipient: %v", err)
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

func TestMailRetryRevalidatesRecipient(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	a, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "target"})
	if err != nil {
		t.Fatal(err)
	}
	req := SendRequest{To: a.Name, Body: "hello", RetryID: "ordinary-one"}
	if _, err := f.SendMessage(OwnerCaller, req); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveAgent(OwnerCaller, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.SendMessage(OwnerCaller, req); KindOf(err) != KindNotFound {
		t.Fatalf("deleted: %v", err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "target"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.SendMessage(OwnerCaller, req); KindOf(err) != KindConflict {
		t.Fatalf("replaced: %v", err)
	}
}

func TestMailRetryRevalidatesPeer(t *testing.T) {
	_, f, _, _, caller := peerEnv(t)
	req := SendRequest{To: "bob@hostc", Body: "hello", RetryID: "peer-one"}
	if _, err := f.SendMessage(caller, req); err != nil {
		t.Fatal(err)
	}
	f.SetPeerSender(nil, "testbox")
	if _, err := f.SendMessage(caller, req); KindOf(err) != KindNotFound {
		t.Fatalf("removed peer: %v", err)
	}
}
