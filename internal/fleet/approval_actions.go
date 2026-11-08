package fleet

import (
	"context"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// RunAfterApproval owns the wait and execution for a held daemon action. Neither
// the requesting HTTP connection nor the agent process owns this goroutine.
// Only an allowed approval invokes run. finished receives every terminal state
// so callers can retain a denial/expiry receipt without risking execution.
func (f *Fleet) RunAfterApproval(c Caller, id string, until time.Time, run func(context.Context), finished func(model.Approval)) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		finished(model.Approval{ID: id, State: model.ApprovalExpired})
		return
	}
	f.wg.Add(1)
	f.mu.Unlock()
	go func() {
		defer f.wg.Done()
		ap, err := f.WaitHold(f.ctx, c, id, max(0, time.Until(until)))
		if err != nil || f.ctx.Err() != nil {
			finished(model.Approval{ID: id, State: model.ApprovalExpired})
			return
		}
		if ap.State == model.ApprovalPending {
			ap, err = f.CancelHold(c, id)
			if err != nil {
				finished(model.Approval{ID: id, State: model.ApprovalExpired})
				return
			}
		}
		if ap.State == model.ApprovalAllowed {
			run(f.ctx)
		}
		finished(ap)
	}()
}
