package fleet

import (
	"fmt"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

const ToolOwnerRecovery = "owner_recovery_notice"

// OwnerRecoveryNotice records recovery before any capability is issued. The
// approval event drives the host UI notification and persistent Needs-you card.
// This is a notice, never an approval that a file-token caller can turn into proof.
func (f *Fleet) OwnerRecoveryNotice(host string, expires time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.headLocked()
	if h == nil {
		return fmt.Errorf("host head unavailable for recovery notice")
	}
	text := fmt.Sprintf("SECURITY: restricted owner recovery requested for https://%s; expires %s. No owner actions until a fresh host proof. Review Owner devices on the host.", host, expires.UTC().Format(time.RFC3339))
	h.seq++
	entry := model.TimelineEntry{Seq: h.seq, AgentID: h.a.ID, At: model.Now(), Kind: model.EntrySystem, Text: text, Tool: ToolOwnerRecovery}
	if err := f.st.AppendTimeline(entry); err != nil {
		return err
	}
	ap := model.Approval{ID: model.NewID(), AgentID: h.a.ID, Kind: model.ApprovalPermission, Tool: ToolOwnerRecovery, Description: text, Question: "Restricted owner recovery", State: model.ApprovalPending, CreatedAt: model.Now(), Options: []string{}}
	if err := f.st.PutApproval(ap); err != nil {
		return err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvTimelineAppended, AgentID: h.a.ID, Data: entry})
	f.Publish(model.BusEvent{Type: model.EvApprovalCreated, AgentID: h.a.ID, Data: ap})
	f.bus.Publish(model.BusEvent{Type: "security.owner_recovery", AgentID: h.a.ID, Data: map[string]any{"host": host, "expires": expires}})
	time.AfterFunc(time.Until(expires), func() {
		f.ExpireApproval(ap.ID, "Recovery capability expired; this notice never grants owner authority.")
	})
	return nil
}
