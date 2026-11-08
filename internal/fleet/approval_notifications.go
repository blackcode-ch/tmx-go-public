package fleet

import (
	"context"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// ApprovalNotifier delivers a generic owner alert, never an approval capability.
// The channel name is a fixed transport label; errors must not be logged because
// transport errors may contain subscription endpoints or credentials.
type ApprovalNotifier func(context.Context, model.Approval) (channel string, err error)

type approvalNotifications struct {
	mu    sync.Mutex
	send  ApprovalNotifier
	queue chan model.Approval
}

func (f *Fleet) SetApprovalNotifier(send ApprovalNotifier) {
	f.approvalNotify.mu.Lock()
	f.approvalNotify.send = send
	f.approvalNotify.mu.Unlock()
}

func (f *Fleet) startApprovalNotifications() {
	f.approvalNotify.queue = make(chan model.Approval, 128)
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			select {
			case <-f.ctx.Done():
				return
			case ap := <-f.approvalNotify.queue:
				f.approvalNotify.mu.Lock()
				send := f.approvalNotify.send
				f.approvalNotify.mu.Unlock()
				channel, ok := "unconfigured", false
				if send != nil {
					ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
					var err error
					channel, err = send(ctx, ap)
					ok = err == nil
					cancel()
				}
				f.recordApprovalNotification(ap.ID, channel, ok)
			}
		}
	}()
}

func (f *Fleet) recordApprovalNotification(id, channel string, ok bool) {
	status := channel + ":fail"
	if ok {
		status = channel + ":ok"
	}
	if err := f.st.SetApprovalNotified(id, status); err != nil {
		f.log.Error("approval notification receipt unavailable", "approval_id", id)
	}
	f.log.Info("approval notified", "approval_id", id, "channel", channel, "ok", ok)
}

func (f *Fleet) auditApprovalEvent(ev model.BusEvent) {
	if ev.Type != model.EvApprovalCreated && ev.Type != model.EvApprovalResolved {
		return
	}
	ap, ok := ev.Data.(model.Approval)
	if !ok {
		return
	}
	if ev.Type == model.EvApprovalCreated {
		fresh, err := f.st.CreateApprovalDelivery(ap.ID)
		if err != nil {
			f.log.Error("approval delivery tracking unavailable", "approval_id", ap.ID)
			return
		}
		if !fresh {
			return
		}
		f.log.Info("approval created", "approval_id", ap.ID, "agent_id", ap.AgentID, "tool", ap.Tool)
		if ap.Deferred {
			return
		}
		select {
		case f.approvalNotify.queue <- ap:
		default:
			f.recordApprovalNotification(ap.ID, "queue_full", false)
		}
	} else {
		notified, seen := f.st.ApprovalDelivery(ap.ID)
		f.log.Info("approval resolved", "approval_id", ap.ID, "state", ap.State, "notified", notified, "seen", seen != 0)
	}
}

// MarkApprovalSeen is called only by the owner UI when a card is visible.
// Fetching the approvals list or delivering a push does not prove it was seen.
func (f *Fleet) MarkApprovalSeen(c Caller, id string) error {
	if !c.Owner {
		return errf(KindForbidden, "only the owner marks approvals seen")
	}
	if _, err := f.ApprovalByID(id); err != nil {
		return err
	}
	if _, err := f.st.CreateApprovalDelivery(id); err != nil {
		return err
	}
	fresh, err := f.st.MarkApprovalSeen(id, model.Now())
	if fresh {
		f.log.Info("approval seen", "approval_id", id)
	}
	return err
}
