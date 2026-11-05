package resources

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// ledger is today's accounting, kept in <state>/resources.json so a daemon restart neither
// forgets what agents wrote today nor leaves a paused process paused for good.
type ledger struct {
	Day     string                    `json:"day"`              // local date, YYYY-MM-DD
	Written map[string]uint64         `json:"written"`          // agent ID → bytes written today
	Extra   map[string]uint64         `json:"extra,omitempty"`  // agent ID → raised budget on top of the default
	Rearm   map[string]uint64         `json:"rearm,omitempty"`  // agent ID → resumed: pause again at this many bytes
	MemOK   map[string]bool           `json:"mem_ok,omitempty"` // agent ID → memory cap waived today
	Paused  map[string][]sysinfo.Proc `json:"paused,omitempty"` // agent ID → processes it suspended
}

func ledgerPath(p config.Paths) string { return filepath.Join(p.Home, "resources.json") }

func loadLedger(p config.Paths) (ledger, error) {
	var l ledger
	b, err := os.ReadFile(ledgerPath(p))
	if errors.Is(err, os.ErrNotExist) {
		return l.fill(), nil
	}
	if err != nil {
		return l.fill(), err
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return ledger{}.fill(), err
	}
	return l.fill(), nil
}

func (l ledger) fill() ledger {
	if l.Written == nil {
		l.Written = map[string]uint64{}
	}
	if l.Extra == nil {
		l.Extra = map[string]uint64{}
	}
	if l.Rearm == nil {
		l.Rearm = map[string]uint64{}
	}
	if l.MemOK == nil {
		l.MemOK = map[string]bool{}
	}
	if l.Paused == nil {
		l.Paused = map[string][]sysinfo.Proc{}
	}
	return l
}

// newDay resets the day's accounting (paused processes stay recorded).
func (l *ledger) newDay(day string) {
	paused := l.Paused
	*l = ledger{Day: day, Paused: paused}.fill()
}

func (l ledger) save(p config.Paths) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(ledgerPath(p), b, 0o600)
}
