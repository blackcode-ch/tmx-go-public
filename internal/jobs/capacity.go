package jobs

import "time"

func active(state string) bool {
	return state == "queued" || state == "running" || state == "cancelling"
}
func expendable(state string) bool { return state == "completed" || state == "cancelled" }

// capacityLocked separates active reservations from retained history. Successful
// and cancelled records expire after a day and can be evicted earlier under
// pressure. Failure/quarantine history is never silently forgotten. Background
// history and live work cannot use the last 10% reserved for owner foreground.
func (m *Manager) capacityLocked(i Identity, foreground bool, task AuditTask, newIdentity bool) bool {
	now := time.Now()
	for id, e := range m.entries {
		if expendable(e.record.State) && now.Sub(e.record.Heartbeat) > 24*time.Hour {
			delete(m.entries, id)
		}
	}
	reserve := 0
	if m.o.MaxJobs > 1 {
		reserve = max(1, m.o.MaxJobs/10)
	}
	limit := m.o.MaxJobs
	if !foreground {
		limit -= reserve
	}
	if newIdentity {
		for len(m.entries) >= limit {
			oldest := ""
			var at time.Time
			for id, e := range m.entries {
				if expendable(e.record.State) && (oldest == "" || e.record.Heartbeat.Before(at)) {
					oldest = id
					at = e.record.Heartbeat
				}
			}
			if oldest == "" {
				return false
			}
			delete(m.entries, oldest)
		}
	}
	total, background, own, peer, chat := 0, 0, 0, 0, 0
	for _, e := range m.entries {
		r := e.record
		if !active(r.State) {
			continue
		}
		total++
		if !r.Foreground {
			background++
		}
		if r.Peer == "" && r.Identity.Owner == i.Owner {
			own++
		}
		if task.Peer != "" && r.Peer == task.Peer {
			peer++
		}
		if task.Chat != "" && r.Chat == task.Chat {
			chat++
		}
	}
	if total >= m.o.MaxJobs || (!foreground && background >= limit) {
		return false
	}
	if task.Peer != "" {
		return peer < 10 && chat < 10
	}
	return own < 100
}
