package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// OpenFailSafe never makes the daemon unavailable over the jobs ledger. A marker
// keeps jobs disabled across future starts until an operator inspects recovery;
// it prevents an archived poison ledger from becoming a fresh auto-resume state.
// If the disk cannot write the marker, keep the original ledger in place.
func OpenFailSafe(o Options) (*Manager, error) {
	_, markerErr := os.Stat(o.Path + ".disabled")
	var err error
	if errors.Is(markerErr, os.ErrNotExist) {
		var m *Manager
		m, err = New(o)
		if err == nil {
			return m, nil
		}
	} else {
		err = errors.New("job recovery inspection required")
	}
	if markerErr != nil && errors.Is(markerErr, os.ErrNotExist) {
		if e := os.WriteFile(o.Path+".disabled", []byte("Inspect the saved jobs ledger before re-enabling job workers.\n"), 0600); e == nil {
			// Rename only our own ledger, never any other state file. Failure to archive
			// is harmless: the disabled marker preserves the original for inspection.
			_ = os.Rename(o.Path, fmt.Sprintf("%s.corrupt-%d", o.Path, time.Now().UnixNano()))
		}
	}
	cx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	close(done)
	return &Manager{o: o, disabled: true, entries: map[string]*entry{}, ctx: cx, stop: stop, done: done}, err
}
