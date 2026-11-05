package fleet

import (
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestOwnerRecoveryNoticeCannotBeDecidedWithOwnerToken(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	sub := f.Subscribe()
	defer f.Unsubscribe(sub)
	if err := f.OwnerRecoveryNotice("trial.example.ts.net", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{model.EvApprovalCreated: false, model.EvTimelineAppended: false, "security.owner_recovery": false}
	for i := 0; i < 3; i++ {
		select {
		case ev := <-sub.C():
			want[ev.Type] = true
		case <-time.After(time.Second):
			t.Fatal("missing host notification/event")
		}
	}
	for event, got := range want {
		if !got {
			t.Fatalf("missing %s", event)
		}
	}
	aps, err := f.Approvals(model.ApprovalPending)
	if err != nil {
		t.Fatal(err)
	}
	var found *model.Approval
	for i := range aps {
		if aps[i].Tool == ToolOwnerRecovery {
			found = &aps[i]
		}
	}
	if found == nil || !strings.Contains(found.Description, "expires") {
		t.Fatal("missing visible security card")
	}
	if _, err := f.Decide(OwnerCaller, found.ID, DecideRequest{Decision: "allow"}); err == nil {
		t.Fatal("owner token decided recovery notice")
	}
	entries, err := f.Timeline(found.AgentID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Tool == ToolOwnerRecovery {
			return
		}
	}
	t.Fatal("missing security timeline entry")
}
