// Package slots holds machine-wide counting semaphores for heavy work (builds, test suites,
// browsers): `tmx slot -- <command>` waits for a place of its kind, runs the command and
// gives the place back. A place is a lease: the client renews it, and one that is not
// renewed, or whose client process is gone, lapses, so a crash never keeps a slot.
// See DESIGN.md "Resources".
package slots

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

const (
	// TTL is how long a lease lives without a renewal.
	TTL = 30 * time.Second
	// MaxPerHolder bounds the leases (held and waiting) one agent or the owner may have.
	MaxPerHolder = 16
	sweepEvery   = 2 * time.Second
	maxCommand   = 300
)

var (
	ErrNotFound    = errors.New("no such slot lease; it expired or was released")
	ErrForbidden   = errors.New("this slot lease belongs to someone else")
	ErrUnknownKind = errors.New("unknown slot kind")
	ErrTooMany     = errors.New("too many slot leases for one holder")
)

// State of a lease.
const (
	Held    = "held"
	Waiting = "waiting"
)

// Holder is who asks: an agent (AgentID and Name) or the owner (AgentID "").
type Holder struct {
	AgentID string
	Name    string
}

func (h Holder) owner() bool { return h.AgentID == "" }

// Lease is one place held or waited for.
type Lease struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Holder    string `json:"holder"` // agent name, or "owner"
	AgentID   string `json:"agent_id,omitempty"`
	Command   string `json:"command"`
	PID       int    `json:"pid,omitempty"` // the client process; its exit releases the lease
	State     string `json:"state"`         // held | waiting
	Position  int    `json:"position,omitempty"`
	Since     int64  `json:"since"`
	GrantedAt int64  `json:"granted_at,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
}

// KindView sums up one kind.
type KindView struct {
	Kind    string `json:"kind"`
	Size    int    `json:"size"`
	Held    int    `json:"held"`
	Waiting int    `json:"waiting"`
}

// View is GET /api/slots.
type View struct {
	Kinds  []KindView `json:"kinds"`
	Leases []Lease    `json:"leases"` // held first, then waiting in order
}

// Options configures a Manager.
type Options struct {
	Sizes    map[string]int
	Alive    func(pid int) bool // nil = never checked
	OnChange func(View)         // called after every change, outside the lock
	Now      func() time.Time
}

// Manager is the daemon's set of slots.
type Manager struct {
	opts    Options
	mu      sync.Mutex
	sizes   map[string]int
	leases  map[string]*lease
	waiting map[string][]string // kind → lease IDs in arrival order
}

type lease struct {
	Lease
	granted chan struct{}
}

// New creates a Manager with sizes per kind (kinds with no size do not exist).
func New(o Options) *Manager {
	if o.Now == nil {
		o.Now = time.Now
	}
	m := &Manager{opts: o, leases: map[string]*lease{}, waiting: map[string][]string{}}
	m.sizes = map[string]int{}
	for k, v := range o.Sizes {
		m.sizes[k] = v
	}
	return m
}

// Notify sets the change hook (replacing Options.OnChange).
func (m *Manager) Notify(fn func(View)) {
	m.mu.Lock()
	m.opts.OnChange = fn
	m.mu.Unlock()
}

// SetSizes changes the places per kind; waiters are promoted when a kind grew.
func (m *Manager) SetSizes(sizes map[string]int) {
	m.mu.Lock()
	m.sizes = map[string]int{}
	for k, v := range sizes {
		m.sizes[k] = v
	}
	m.promoteLocked()
	m.mu.Unlock()
	m.changed()
}

// Acquire asks for a place of kind. The lease comes back held, or waiting with its position.
func (m *Manager) Acquire(kind string, h Holder, command string, pid int) (Lease, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "heavy"
	}
	command = strings.TrimSpace(command)
	if len(command) > maxCommand {
		command = command[:maxCommand] + "…"
	}
	m.mu.Lock()
	if _, ok := m.sizes[kind]; !ok {
		m.mu.Unlock()
		return Lease{}, fmt.Errorf("%w %q (known: %s)", ErrUnknownKind, kind, strings.Join(m.kindsLocked(), ", "))
	}
	n := 0
	for _, l := range m.leases {
		if l.AgentID == h.AgentID {
			n++
		}
	}
	if n >= MaxPerHolder {
		m.mu.Unlock()
		return Lease{}, ErrTooMany
	}
	now := m.opts.Now()
	name := h.Name
	if h.owner() {
		name = model.Owner
	}
	l := &lease{Lease: Lease{ID: model.NewID(), Kind: kind, Holder: name, AgentID: h.AgentID, Command: command,
		PID: pid, State: Waiting, Since: now.UnixMilli(), ExpiresAt: now.Add(TTL).UnixMilli()}, granted: make(chan struct{})}
	m.leases[l.ID] = l
	m.waiting[kind] = append(m.waiting[kind], l.ID)
	m.promoteLocked()
	out := m.leaseLocked(l)
	m.mu.Unlock()
	m.changed()
	return out, nil
}

// Wait renews the lease and blocks until it is held, ctx ends or max passed; it returns
// the lease as it is then.
func (m *Manager) Wait(ctx context.Context, id string, h Holder, max time.Duration) (Lease, error) {
	l, state, err := m.renew(id, h)
	if err != nil {
		return Lease{}, err
	}
	if state == Waiting {
		t := time.NewTimer(max)
		defer t.Stop()
		select {
		case <-l.granted:
		case <-ctx.Done():
		case <-t.C:
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.leases[id]; !ok {
		return Lease{}, ErrNotFound
	}
	return m.leaseLocked(l), nil
}

// Renew extends the lease by TTL.
func (m *Manager) Renew(id string, h Holder) (Lease, error) {
	l, _, err := m.renew(id, h)
	if err != nil {
		return Lease{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.leaseLocked(l), nil
}

// renew extends the lease and returns it with its state at that moment.
func (m *Manager) renew(id string, h Holder) (*lease, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.leases[id]
	if !ok {
		return nil, "", ErrNotFound
	}
	if !h.owner() && l.AgentID != h.AgentID {
		return nil, "", ErrForbidden
	}
	l.ExpiresAt = m.opts.Now().Add(TTL).UnixMilli()
	return l, l.State, nil
}

// Release gives the place back (the owner may release anyone's).
func (m *Manager) Release(id string, h Holder) error {
	m.mu.Lock()
	l, ok := m.leases[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if !h.owner() && l.AgentID != h.AgentID {
		m.mu.Unlock()
		return ErrForbidden
	}
	m.dropLocked(l)
	m.promoteLocked()
	m.mu.Unlock()
	m.changed()
	return nil
}

// View lists every kind and lease.
func (m *Manager) View() View {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.viewLocked()
}

// Run sweeps lapsed leases (no renewal, or the client process is gone) until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Sweep()
		}
	}
}

// Sweep drops lapsed leases once and promotes waiters.
func (m *Manager) Sweep() {
	now := m.opts.Now().UnixMilli()
	m.mu.Lock()
	var gone []*lease
	for _, l := range m.leases {
		if now > l.ExpiresAt || (l.PID > 0 && m.opts.Alive != nil && !m.opts.Alive(l.PID)) {
			gone = append(gone, l)
		}
	}
	for _, l := range gone {
		m.dropLocked(l)
	}
	if len(gone) > 0 {
		m.promoteLocked()
	}
	m.mu.Unlock()
	if len(gone) > 0 {
		m.changed()
	}
}

func (m *Manager) dropLocked(l *lease) {
	delete(m.leases, l.ID)
	q := m.waiting[l.Kind]
	for i, id := range q {
		if id == l.ID {
			m.waiting[l.Kind] = append(q[:i:i], q[i+1:]...)
			break
		}
	}
	if l.State == Waiting {
		close(l.granted) // wake a Wait; it then finds the lease gone
	}
}

func (m *Manager) heldLocked(kind string) int {
	n := 0
	for _, l := range m.leases {
		if l.Kind == kind && l.State == Held {
			n++
		}
	}
	return n
}

func (m *Manager) promoteLocked() {
	now := m.opts.Now().UnixMilli()
	for kind, q := range m.waiting {
		held := m.heldLocked(kind)
		for len(q) > 0 && held < m.sizes[kind] {
			l := m.leases[q[0]]
			q = q[1:]
			if l == nil {
				continue
			}
			l.State, l.GrantedAt = Held, now
			close(l.granted)
			held++
		}
		m.waiting[kind] = q
	}
}

func (m *Manager) leaseLocked(l *lease) Lease {
	out := l.Lease
	if out.State == Waiting {
		for i, id := range m.waiting[l.Kind] {
			if id == l.ID {
				out.Position = i + 1
			}
		}
	}
	return out
}

func (m *Manager) kindsLocked() []string {
	out := make([]string, 0, len(m.sizes))
	for k := range m.sizes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) viewLocked() View {
	v := View{Kinds: []KindView{}, Leases: []Lease{}}
	for _, k := range m.kindsLocked() {
		v.Kinds = append(v.Kinds, KindView{Kind: k, Size: m.sizes[k], Held: m.heldLocked(k), Waiting: len(m.waiting[k])})
	}
	for _, l := range m.leases {
		v.Leases = append(v.Leases, m.leaseLocked(l))
	}
	sort.Slice(v.Leases, func(i, j int) bool {
		a, b := v.Leases[i], v.Leases[j]
		if a.State != b.State {
			return a.State == Held
		}
		if a.State == Waiting && a.Kind == b.Kind {
			return a.Position < b.Position
		}
		return a.Since < b.Since
	})
	return v
}

func (m *Manager) changed() {
	m.mu.Lock()
	fn := m.opts.OnChange
	m.mu.Unlock()
	if fn != nil {
		fn(m.View())
	}
}
