package fleet

import (
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// CompanionMailHealth reads queue metadata without consuming the agents' inboxes.
func (f *Fleet) CompanionMailHealth() ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := []map[string]any{}
	for _, r := range f.agents {
		msgs, err := f.st.ListMessages(store.MessageFilter{To: r.a.ID, State: model.MsgQueued, Limit: MaxQueued})
		if err != nil {
			return nil, err
		}
		if len(msgs) == 0 {
			continue
		}
		oldest := int64(0)
		for _, m := range msgs {
			oldest = max(oldest, (model.Now()-m.CreatedAt)/1000)
		}
		rows = append(rows, map[string]any{"name": r.a.Name, "host": f.opts.Machine, "state": r.a.State,
			"queued": len(msgs), "oldest_s": oldest, "reason": r.a.PausedReason})
	}
	return rows, nil
}
