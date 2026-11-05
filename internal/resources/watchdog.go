package resources

import (
	"context"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Watchdog is deliberately separate from Run and its mutex. It restores only
// fleet-recorded resource pauses through the existing identity-checked #76 path.
// It never controls a process directly or admits a new process.
func (m *Manager) Watchdog(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.restoreOnDegradation()
		}
	}
}
func (m *Manager) restoreOnDegradation() {
	defer func() {
		if m.mu.TryLock() {
			m.clearWatchdogRestorationsLocked()
			m.mu.Unlock()
		}
	}()
	if m.Fresh() || m.fleet == nil {
		return
	}
	for _, a := range m.fleet.LiveAgents() {
		if a.State != model.StatePaused {
			continue
		}
		switch a.PausedReason {
		case BrakeDisk, BrakeMemory, BrakeWriteBudget:
			if m.fleet.UnbrakeAgent(a.ID, a.PausedReason, "governor degraded; restored by independent watchdog") {
				m.watchRestored.Store(a.ID, true)
			}
		}
	}
}

func (m *Manager) clearWatchdogRestorationsLocked() {
	m.watchRestored.Range(func(key, value any) bool {
		id := key.(string)
		delete(m.led.Paused, id)
		delete(m.diskTurns, id)
		m.dirty = true
		m.watchRestored.Delete(key)
		return true
	})
}
