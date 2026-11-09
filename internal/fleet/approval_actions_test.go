package fleet

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

type approvalLog struct {
	sync.Mutex
	bytes.Buffer
}

func (b *approvalLog) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}
func (b *approvalLog) text() string { b.Lock(); defer b.Unlock(); return b.Buffer.String() }

func TestApprovalActionLifecycle(t *testing.T) {
	for _, verdict := range []string{"allow", "deny", "expire"} {
		t.Run(verdict, func(t *testing.T) {
			e := newEnv(t)
			f := e.started()
			var logs approvalLog
			f.log = slog.New(slog.NewTextHandler(&logs, nil))
			notified := make(chan struct{}, 1)
			f.SetApprovalNotifier(func(ctx context.Context, ap model.Approval) (string, error) {
				notified <- struct{}{}
				if verdict == "expire" {
					return "fake_phone", errors.New("secret-endpoint-never-log")
				}
				return "fake_phone", nil
			})
			head, _ := f.Agent("head")
			c := AgentCaller(head.ID)
			ap, err := f.HoldP2P(c, ToolP2PInvite, "Create an invitation")
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-notified:
			case <-time.After(3 * time.Second):
				t.Fatal("no phone notification")
			}
			eventually(t, "notification receipt", func() bool { n, _ := e.st.ApprovalDelivery(ap.ID); return strings.HasPrefix(n, "fake_phone:") })
			if verdict != "expire" {
				if err := f.MarkApprovalSeen(OwnerCaller, ap.ID); err != nil {
					t.Fatal(err)
				}
			}
			result := make(chan model.ApprovalState, 1)
			until := time.Now().Add(time.Minute)
			if verdict == "expire" {
				until = time.Now().Add(-time.Second)
			}
			executed := false
			f.RunAfterApproval(c, ap.ID, until, func(ctx context.Context) { executed = true }, func(a model.Approval) { result <- a.State })
			if verdict != "expire" {
				if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: verdict}); err != nil {
					t.Fatal(err)
				}
			}
			var state model.ApprovalState
			select {
			case state = <-result:
			case <-time.After(3 * time.Second):
				t.Fatal("daemon action not resolved")
			}
			if executed != (verdict == "allow") {
				t.Fatalf("execution after %s: %v", verdict, executed)
			}
			want := map[string]model.ApprovalState{"allow": model.ApprovalAllowed, "deny": model.ApprovalDenied, "expire": model.ApprovalExpired}[verdict]
			if state != want {
				t.Fatalf("state %s, want %s", state, want)
			}
			wantLogs := []string{"approval created", "approval notified", "approval resolved", "notified=fake_phone:", "state=" + string(want)}
			// Notification receipts and resolution callbacks can precede their audit logs.
			eventually(t, "approval lifecycle audit logs", func() bool {
				text := logs.text()
				for _, part := range wantLogs {
					if !strings.Contains(text, part) {
						return false
					}
				}
				return true
			})
			text := logs.text()
			for _, part := range wantLogs {
				if !strings.Contains(text, part) {
					t.Fatalf("missing %q in %s", part, text)
				}
			}
			if verdict == "expire" && !strings.Contains(text, "seen=false") {
				t.Fatal(text)
			}
			if strings.Contains(text, "secret-endpoint") {
				t.Fatal("transport secret logged")
			}
		})
	}
}
