package gate

// The grace window, like sudo's timestamp: after the owner proves themselves (any successful
// confirmation on this machine), the gate lets the owner's own requests through without a
// prompt until the window ends; that includes Always actions such as the admin handover. The
// owner still makes every decision in the UI (Allow on a held command, ...): the window only
// skips the OS prompt, nothing an agent asks for is approved by itself.
//
//   - Only a successful proof opens or renews it; requests passed on it never extend it, so it
//     cannot be kept open without a fingerprint (or the password, the PIN).
//   - Fresh actions always ask, window or not: turning the gate on or off, a longer window.
//   - Its length (Settings → Safety, `tmx gate grace`) is kept in gate.json; the open window is
//     kept in memory only, so a daemon restart, which every update is, locks it.
//   - Lock ends it at once. A shorter length or off applies to the open window at once too: it
//     ends that long after the proof that opened it.

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// GraceSteps are the window's lengths the owner can pick, in minutes; 0 = off (ask every time).
var GraceSteps = []int{0, 5, 15, 30, 60, 120, 240, 480, 1440}

// DefaultGrace is the window's length until the owner picks another: 1 hour.
const DefaultGrace = 60

// EvUpdated is the bus event for a change of Status (on, off, the window opened, changed or
// locked). It carries no data: agents see the bus too, the owner re-reads GET /api/gate.
const EvUpdated = "gate.updated"

// ErrGraceStep is a length that is not one of GraceSteps.
var ErrGraceStep = errors.New("the no-ask window must be off or one of 5, 15 or 30 minutes, 1, 2, 4, 8 or 24 hours")

// ValidGrace reports whether minutes is one of GraceSteps.
func ValidGrace(minutes int) bool { return slices.Contains(GraceSteps, minutes) }

// GraceLabel says a length for people: "off", "5 minutes", "1 hour", "24 hours".
func GraceLabel(minutes int) string {
	switch {
	case minutes <= 0:
		return "off"
	case minutes == 60:
		return "1 hour"
	case minutes%60 == 0:
		return fmt.Sprintf("%d hours", minutes/60)
	case minutes == 1:
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", minutes)
}

// GraceMinutes is the window's length (0 = off).
func (g *Gate) GraceMinutes() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.graceLocked()
}

// GraceUntil is when the open window ends; zero when it is locked.
func (g *Gate) GraceUntil() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.graceUntilLocked()
}

// SetGrace sets the window's length (one of GraceSteps) and keeps it in gate.json. Callers ask
// for a Fresh proof first when it gets longer (turning it on from off included); a shorter
// length or off needs none.
func (g *Gate) SetGrace(minutes int) error {
	if !ValidGrace(minutes) {
		return ErrGraceStep
	}
	g.mu.Lock()
	next := g.st
	next.Grace = &minutes
	err := g.saveLocked(next)
	g.mu.Unlock()
	if err != nil {
		return err
	}
	g.log.Info("gate: grace window changed", "minutes", minutes)
	g.changed()
	return nil
}

// Lock ends the window now: the next gated action asks again. It never needs a proof.
func (g *Gate) Lock() {
	g.mu.Lock()
	g.proofAt = time.Time{}
	g.proofSession, g.proofCaller = "", ""
	g.mu.Unlock()
	g.log.Info("gate: grace window locked")
	g.changed()
}

func (g *Gate) graceLocked() int {
	if g.st.Grace == nil {
		return DefaultGrace
	}
	return max(*g.st.Grace, 0)
}

// graceUntilLocked: the window ends its length after the last proof; zero = locked (no proof
// since the daemon started, Lock, off, or over).
func (g *Gate) graceUntilLocked() time.Time {
	m := g.graceLocked()
	if m == 0 || g.proofAt.IsZero() {
		return time.Time{}
	}
	until := g.proofAt.Add(time.Duration(m) * time.Minute)
	if !g.now().Before(until) {
		return time.Time{}
	}
	return until
}
