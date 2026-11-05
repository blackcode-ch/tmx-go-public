// Package gate is the owner gate: proof that the owner is at this machine before a sensitive
// action (allowing a held command, bypassing permissions, deleting a project, changing the gate).
// The proof comes from the operating system and is checked by the daemon itself: on macOS the
// daemon asks LocalAuthentication (Touch ID, with the login password as fallback), on Windows
// Windows Hello (face, fingerprint or PIN; auth_windows.go). A client
// never proves anything by a header; it only names a confirmation the daemon already verified,
// bound to exactly one request and usable once. See DESIGN.md "Safety".
//
// Lock-out safety: only tmx actions are gated, never the owner's OS login or files.
// Missing state fails closed once enrolled. A never-enrolled installation persists
// explicit off on its first start. Offline recovery is described by Recovery and DESIGN.md.
//
// The grace window (like sudo's timestamp, grace.go): after a successful proof the gate stops
// asking the owner's own requests for a while (1 hour unless the owner picks another length).
package gate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// Authenticator asks the operating system to verify the owner.
// MethodSimulated is the Method of a gatesim test build's authenticator (auth_sim.go). Releases
// check that no shipped binary reports it (tmx version --gate) or carries GatesimBuildMarker.
const MethodSimulated = "simulated"

// GatesimBuildMarker is a string only a gatesim build contains; the release job greps for it.
const GatesimBuildMarker = "TMX-GATESIM-TEST-BUILD-NEVER-RELEASE"

type Authenticator interface {
	// Method is a stable id ("touchid", "windows-hello") and Label a phrase for people ("Touch ID or your Mac password").
	Method() string
	Label() string
	// Available returns nil when the owner can be asked on this machine now.
	Available() error
	// Authenticate shows the OS prompt with reason ("tmx is trying to <reason>") and blocks
	// until the owner answers or ctx ends (which withdraws the prompt).
	Authenticate(ctx context.Context, reason string) error
}

var (
	ErrCancelled   = errors.New("the confirmation was cancelled")
	ErrUnavailable = errors.New("owner confirmation is not available on this machine")
	ErrBusy        = errors.New("another confirmation is already waiting for the owner")
	ErrTooMany     = errors.New("too many confirmation prompts; wait a minute")
)

const (
	// PromptTimeout bounds one OS prompt; it is withdrawn afterwards.
	PromptTimeout = 90 * time.Second
	// UseWindow is how long a confirmed proof may be used for its one request.
	UseWindow = 2 * time.Minute
	keepDone  = 10 * time.Minute
	maxPerMin = 10
)

// Confirmation states.
const (
	StatePending   = "pending"   // the OS prompt is on screen
	StateConfirmed = "confirmed" // the owner proved presence; the request may be retried once
	StateFailed    = "failed"    // wrong answer, cancelled or timed out
	StateUsed      = "used"
)

// Confirmation is one proof of presence for one request.
type Confirmation struct {
	ID          string `json:"id"`
	Reason      string `json:"reason"`
	State       string `json:"state"`
	Error       string `json:"error,omitempty"`
	Method      string `json:"method"`
	MethodLabel string `json:"method_label"`
	// PIN: the owner PIN answers it (POST /api/gate/confirmations/{id}/pin at the machine);
	// Hello: the OS prompt (Windows Hello / Touch ID) is open for it too (#187).
	PIN       bool  `json:"pin,omitempty"`
	Hello     bool  `json:"hello,omitempty"`
	CreatedAt int64 `json:"created_at"`
	ExpiresAt int64 `json:"expires_at,omitempty"` // a confirmed proof's use deadline
	caller    string
	ui        bool
	grace     string
	key       string
	done      chan struct{}
	cancel    context.CancelFunc
}

// Status is GET /api/gate.
type Status struct {
	Enabled     bool     `json:"enabled"`
	Available   bool     `json:"available"`
	Method      string   `json:"method"`
	MethodLabel string   `json:"method_label"`
	Unavailable string   `json:"unavailable,omitempty"`
	ChangedAt   int64    `json:"changed_at,omitempty"`
	Gated       []string `json:"gated"`
	// File is the gate's state file. Deleting it does not disable the gate.
	File     string `json:"file,omitempty"`
	Recovery string `json:"recovery,omitempty"`
	// GraceMinutes is how long the gate stops asking after a proof (0 = off: it asks every
	// time); GraceUntil is when the open window ends (Unix ms; absent = locked). GraceSteps are
	// the lengths Settings offers.
	GraceMinutes int   `json:"grace_minutes"`
	GraceUntil   int64 `json:"grace_until,omitempty"`
	GraceSteps   []int `json:"grace_steps"`
	// PINSet: the owner PIN is set; it answers every confirmation, on every device (#187).
	// HelloShortcut: the OS prompt is also offered at this machine (Settings > Safety). Without
	// a PIN the OS prompt is the only way in, whatever HelloShortcut says.
	PINSet        bool `json:"pin_set"`
	HelloShortcut bool `json:"hello_shortcut"`
}

// GatedActions lists what the gate protects once on (Settings shows it). The last ones always
// ask: the admin handover also while the gate is off, the gate's own changes also inside the
// grace window.
var GatedActions = []string{
	"Allowing a command the guard held",
	"Setting a permission mode to Bypass permissions",
	"Switching the dangerous-command guard off",
	"Deleting a project",
	"Stopping an agent started outside tmx to adopt it",
	"Handing Companion a task with money, accounts or outward actions",
	"Switching the head agent to another harness",
	"Turning peer-to-peer on (off is never gated: it only ever narrows what this machine reaches)",
	"Changing update settings or the GitHub token for updates",
	"Installing an update by hand (automatic updates still install by themselves)",
	"Making someone a group's admin (also while the gate is off)",
	"Turning the owner gate on or off, or making the no-ask window longer (every time)",
}

// Action is a request that may need the owner's proof.
type Action struct {
	// PromptReason is optional private detail shown by the authenticator, never
	// stored in Confirmation.Reason or written to logs (e.g. a pairing code).
	PromptReason string
	Reason       string // completes "tmx is trying to ...": "allow `sudo true` for worker-1"
	Key          string // binds the proof to one request (method, path and body digest)
	Always       bool   // needs a proof even while the gate is off (turning it on)
	// Fresh needs its own proof even inside the grace window: changes that weaken the gate
	// itself (turning it on or off, a longer window).
	Fresh bool
	// Owner: the request came with the owner's own token. Only such a request may pass on the
	// grace window; anything else (an agent, Companion) never does.
	Owner bool
	// Caller binds a confirmation to its authenticated caller and UI instance.
	Caller string
	UI     bool
	// Session is the in-memory credential issued after proof, never the file token.
	Session string
}

// NeedConfirmation means the owner has to confirm first: wait for Confirmation, then retry
// the same request naming its ID.
type NeedConfirmation struct{ Confirmation Confirmation }

func (e *NeedConfirmation) Error() string {
	return "owner confirmation needed: " + e.Confirmation.Reason
}

type fileState struct {
	Enabled   bool   `json:"enabled"`
	Method    string `json:"method,omitempty"`
	ChangedAt int64  `json:"changed_at,omitempty"`
	// Grace is the grace window in minutes; missing = DefaultGrace, 0 = off. The open window
	// itself is never written down (grace.go).
	Grace *int `json:"grace_minutes,omitempty"`
	// HelloShortcut: also offer the OS prompt (Windows Hello / Touch ID) at this machine. Missing
	// in an existing gate.json (written before #187) = on, as it was; a new install writes off.
	HelloShortcut *bool `json:"hello_shortcut,omitempty"`
}

// Gate is the daemon's owner gate.
type Gate struct {
	path   string
	marker Marker
	auth   Authenticator
	log    *slog.Logger
	now    func() time.Time

	mu      sync.Mutex
	st      fileState
	confs   map[string]*Confirmation
	prompts []time.Time
	// proofAt is the last successful proof: it opened the grace window (zero = locked). In
	// memory only, so a daemon restart, which every update is, locks the window.
	proofAt      time.Time
	proofSession string
	proofCaller  string
	notify       func(Status)
	pinSet       func() bool // whether the owner PIN is set (ownerpin); nil = no PIN
}

// New loads secrets/gate.json. Missing enrolled state, unreadable or incomplete
// state fails closed. Missing state with no marker bootstraps explicit off once.
func New(paths config.Paths, auth Authenticator, log *slog.Logger, markers ...Marker) *Gate {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	g := &Gate{path: paths.SecretsDir() + string(os.PathSeparator) + "gate.json", auth: auth, log: log,
		now: time.Now, confs: map[string]*Confirmation{}}
	g.marker = platformMarker(paths)
	if len(markers) > 0 {
		g.marker = markers[0]
	}
	marked, markerErr := g.marker.Read()
	b, err := os.ReadFile(g.path)
	explicitlyEnabled := false
	switch {
	case errors.Is(err, os.ErrNotExist):
		g.st = damagedState()
		if !marked && markerErr == nil {
			absent, absenceErr := g.marker.Absent()
			if absent && absenceErr == nil {
				// Old never-enrolled versions also left gate.json absent. Persist
				// the explicit choice without writing an enrolment marker.
				if e := g.saveLocked(fileState{Enabled: false, HelloShortcut: new(bool)}); e != nil {
					log.Warn("gate: cannot initialize disabled state; treating the gate as on")
				}
			}
		}
	case err != nil:
		log.Warn("gate: cannot read gate.json; treating the gate as on", "err", err)
		g.st = damagedState()
	default:
		var explicit struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.Unmarshal(b, &explicit); err != nil || explicit.Enabled == nil {
			g.st = damagedState()
		} else if err := json.Unmarshal(b, &g.st); err != nil {
			log.Warn("gate: gate.json is damaged; treating the gate as on", "err", err)
			g.st = damagedState()
		} else {
			explicitlyEnabled = g.st.Enabled
		}
	}
	if marked || markerErr != nil {
		if !g.st.Enabled || err != nil || markerErr != nil {
			g.st = damagedState()
		}
	}
	// Migrate only a valid explicit enabled file, never an inferred damaged state.
	if explicitlyEnabled {
		if e := g.marker.Set(true); e != nil {
			g.st = damagedState()
			log.Warn("gate: cannot persist enrolment marker")
		}
	}
	return g
}

// damagedState is the safe reading of an unreadable gate.json: on, and asking every time.
func damagedState() fileState {
	off := 0
	return fileState{Enabled: true, Grace: &off}
}

// SetPINCheck tells the gate how to learn whether the owner PIN is set (the API's ownerpin
// store). Without it the gate has no PIN and asks only through the OS prompt.
func (g *Gate) SetPINCheck(fn func() bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pinSet = fn
}

func (g *Gate) pinSetLocked() bool { return g.pinSet != nil && g.pinSet() }

// helloShortcutLocked: the owner's choice; an existing gate.json without it keeps the OS prompt.
func (g *Gate) helloShortcutLocked() bool { return g.st.HelloShortcut == nil || *g.st.HelloShortcut }

// HelloShortcut reports whether the OS prompt is offered at this machine besides the PIN.
func (g *Gate) HelloShortcut() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.helloShortcutLocked()
}

// ErrNeedPIN: the OS prompt cannot be switched off while no owner PIN is set (it would be the
// last way in).
var ErrNeedPIN = errors.New("set your owner PIN first: without it Windows Hello is the only way to confirm")

// SetHelloShortcut turns the OS prompt at this machine on or off. Callers pass Require first
// (Always and Fresh). Off is refused while no PIN is set.
func (g *Gate) SetHelloShortcut(on bool) error {
	g.mu.Lock()
	if !on && !g.pinSetLocked() {
		g.mu.Unlock()
		return ErrNeedPIN
	}
	next := g.st
	next.HelloShortcut = &on
	err := g.saveLocked(next)
	g.mu.Unlock()
	if err != nil {
		return err
	}
	g.log.Info("gate: Windows Hello shortcut changed", "on", on)
	g.changed()
	return nil
}

// Path is the gate's state file (for the recovery note).
func (g *Gate) Path() string { return g.path }

func (g *Gate) Recovery() string {
	return "keep the existing Python service available; stop only the Go daemon, remove its enrolment markers at " + g.marker.Location() + ", then write {\"enabled\":false,\"grace_minutes\":0} to " + g.path + " before restarting Go. Deleting gate.json alone leaves the gate on."
}

// Status describes the gate.
func (g *Gate) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.statusLocked()
}

// RecoverySession requires a surviving in-memory credential from this exact UI's
// earlier host proof. A root file token or invented UI nonce never establishes it.
// This does not authorize owner actions or extend grace.
func (g *Gate) RecoverySession(caller, credential string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return credential != "" && credential == g.proofSession && caller == g.proofCaller && !g.graceUntilLocked().IsZero()
}

// StatusForSession reports an open window only to the UI that owns it.
func (g *Gate) StatusForSession(caller, credential string) Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.statusLocked()
	if credential == "" || credential != g.proofSession || caller != g.proofCaller {
		s.GraceUntil = 0
	}
	return s
}

func (g *Gate) statusLocked() Status {
	s := Status{Enabled: g.st.Enabled, Method: g.auth.Method(), MethodLabel: g.auth.Label(),
		ChangedAt: g.st.ChangedAt, Gated: append([]string(nil), GatedActions...), File: g.path, Recovery: g.Recovery(),
		GraceMinutes: g.graceLocked(), GraceSteps: append([]int(nil), GraceSteps...)}
	if until := g.graceUntilLocked(); !until.IsZero() {
		s.GraceUntil = until.UnixMilli()
	}
	s.PINSet, s.HelloShortcut = g.pinSetLocked(), g.helloShortcutLocked()
	if s.PINSet {
		// Method stays the OS method: for the UI, status method "pin" means a paired browser.
		s.MethodLabel = "your owner PIN"
		if s.HelloShortcut && g.auth.Available() == nil {
			s.MethodLabel = "your owner PIN or " + g.auth.Label()
		}
		s.Available = true
		return s
	}
	if err := g.auth.Available(); err != nil {
		s.Unavailable = err.Error()
	} else {
		s.Available = true
	}
	return s
}

// Enabled reports whether the gate is on.
func (g *Gate) Enabled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.st.Enabled
}

// SetEnabled turns the gate on or off. Callers pass Require first (Always when turning it on,
// Fresh both ways).
func (g *Gate) SetEnabled(on bool) error {
	g.mu.Lock()
	next := g.st
	next.Enabled, next.Method, next.ChangedAt = on, g.auth.Method(), g.now().UnixMilli()
	err := g.marker.Set(on)
	if err == nil {
		err = g.saveLocked(next)
	}
	g.mu.Unlock()
	if err != nil {
		return err
	}
	g.log.Info("gate: owner gate changed", "enabled", on)
	g.changed()
	return nil
}

// saveLocked writes gate.json, then takes next as the state.
func (g *Gate) saveLocked(next fileState) error {
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := config.WriteSecretFile(g.path, append(b, '\n')); err != nil {
		return err
	}
	g.st = next
	return nil
}

// SetNotify sets what is told about every change of Status (on, off, the grace window opened,
// changed or locked); the API publishes it as an event. fn runs outside the gate's lock.
func (g *Gate) SetNotify(fn func(Status)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.notify = fn
}

func (g *Gate) changed() {
	g.mu.Lock()
	fn := g.notify
	var st Status
	if fn != nil {
		st = g.statusLocked()
	}
	g.mu.Unlock()
	if fn != nil {
		fn(st)
	}
}

// Require lets a request through when the gate is off (and the action is not Always), when
// confirmID names a confirmed, unused proof for this very action, or when the owner asks inside
// the grace window (and the action is not Fresh). Otherwise it starts the OS prompt (or reuses
// the one already open for this action) and returns *NeedConfirmation.
func (g *Gate) Require(a Action, confirmID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.st.Enabled && !a.Always {
		return nil
	}
	g.gcLocked()
	if c, ok := g.confs[confirmID]; ok && confirmID != "" && c.key == a.Key && c.caller == a.Caller {
		switch c.State {
		case StateConfirmed:
			if g.now().UnixMilli() <= c.ExpiresAt {
				c.State = StateUsed
				g.log.Info("gate: confirmation used", "id", c.ID, "reason", c.Reason)
				return nil
			}
		case StatePending:
			return &NeedConfirmation{Confirmation: c.public()}
		}
	}
	if a.Owner && !a.Fresh && a.Session != "" && a.Session == g.proofSession && a.Caller == g.proofCaller {
		// Passing on the window never extends it: only a new proof does.
		if until := g.graceUntilLocked(); !until.IsZero() {
			g.log.Info("gate: passed on the grace window", "reason", a.Reason, "until", until.Format(time.TimeOnly))
			return nil
		}
	}
	for _, c := range g.confs {
		if c.State == StatePending {
			if c.key == a.Key && c.caller == a.Caller {
				return &NeedConfirmation{Confirmation: c.public()}
			}
			return ErrBusy
		}
	}
	// The PIN answers whenever it is set; the OS prompt opens when the owner keeps the shortcut,
	// and always when there is no PIN (never a confirmation nobody can answer).
	pin := g.pinSetLocked()
	hello := !pin || g.helloShortcutLocked()
	if hello {
		if err := g.auth.Available(); err != nil {
			if !pin {
				return fmt.Errorf("%w: %v", ErrUnavailable, err)
			}
			hello = false
		}
	}
	now := g.now()
	recent := g.prompts[:0]
	for _, t := range g.prompts {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	g.prompts = recent
	if len(g.prompts) >= maxPerMin {
		return ErrTooMany
	}
	g.prompts = append(g.prompts, now)
	ctx, cancel := context.WithTimeout(context.Background(), PromptTimeout)
	c := &Confirmation{ID: newID(), Reason: a.Reason, State: StatePending, Method: g.auth.Method(),
		MethodLabel: g.auth.Label(), PIN: pin, Hello: hello, CreatedAt: now.UnixMilli(), key: a.Key, caller: a.Caller,
		ui: a.UI && a.Owner, done: make(chan struct{}), cancel: cancel}
	if pin {
		c.Method, c.MethodLabel = "pin", "your owner PIN"
		if hello {
			c.MethodLabel = "your owner PIN or " + g.auth.Label()
		}
	}
	g.confs[c.ID] = c
	g.log.Info("gate: asking the owner", "id", c.ID, "reason", a.Reason)
	promptReason := a.Reason
	if a.PromptReason != "" {
		promptReason = a.PromptReason
	}
	if c.ui && g.graceLocked() > 0 {
		promptReason += ". Confirming also opens a " + GraceLabel(g.graceLocked()) + " no-ask window for this requesting UI session"
	}
	go g.prompt(ctx, c, promptReason)
	return &NeedConfirmation{Confirmation: c.public()}
}

// prompt waits for the confirmation's answer: the OS prompt (when open for it) or the PIN
// (ConfirmWithPIN), until PromptTimeout. It alone closes done.
func (g *Gate) prompt(ctx context.Context, c *Confirmation, reason string) {
	var err error
	if c.Hello {
		err = g.authenticate(ctx, reason)
	} else {
		<-ctx.Done()
		err = ctx.Err()
	}
	c.cancel()
	g.mu.Lock()
	if c.State != StatePending { // the PIN answered first
		g.mu.Unlock()
		close(c.done)
		return
	}
	if err == nil {
		g.confirmLocked(c, "os")
	} else {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errors.New("no answer in time")
		}
		c.State, c.Error = StateFailed, err.Error()
		g.log.Info("gate: confirmation failed", "id", c.ID, "err", err)
	}
	g.mu.Unlock()
	if err == nil {
		g.changed()
	}
	close(c.done)
}

// confirmLocked marks c confirmed (by "os" or "pin") and opens the grace window for its UI.
func (g *Gate) confirmLocked(c *Confirmation, by string) {
	now := g.now()
	c.State, c.ExpiresAt = StateConfirmed, now.Add(UseWindow).UnixMilli()
	if c.ui {
		c.grace = newID() + newID()
		g.proofAt, g.proofSession, g.proofCaller = now, c.grace, c.caller
	}
	g.log.Info("gate: owner confirmed", "id", c.ID, "by", by)
}

// ErrNoSuchConfirmation: no pending PIN-answerable confirmation of that ID for this caller.
var ErrNoSuchConfirmation = errors.New("no such pending confirmation")

// ConfirmWithPIN answers a pending confirmation with the owner PIN, which the caller (the API,
// at the machine) has already verified. Only the caller that started it may answer it.
func (g *Gate) ConfirmWithPIN(id, caller string) error {
	g.mu.Lock()
	c, ok := g.confs[id]
	if !ok || c.caller != caller || c.State != StatePending || !c.PIN {
		g.mu.Unlock()
		return ErrNoSuchConfirmation
	}
	g.confirmLocked(c, "pin")
	cancel := c.cancel
	g.mu.Unlock()
	cancel() // ends the OS prompt, if one is open
	g.changed()
	return nil
}

// authenticate asks the OS; a panic in the OS glue (Windows Hello is raw COM calls) fails this
// confirmation instead of taking the daemon down.
func (g *Gate) authenticate(ctx context.Context, reason string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			g.log.Error("gate: the OS prompt failed", "panic", r)
			err = fmt.Errorf("the %s prompt failed: %v", g.auth.Label(), r)
		}
	}()
	return g.auth.Authenticate(ctx, reason)
}

// Get returns a confirmation.
func (g *Gate) Get(id string) (Confirmation, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.confs[id]
	if !ok {
		return Confirmation{}, false
	}
	return c.public(), true
}

// Wait blocks until the confirmation is no longer pending, or ctx ends.
func (g *Gate) Wait(ctx context.Context, id string) (Confirmation, bool) {
	g.mu.Lock()
	c, ok := g.confs[id]
	g.mu.Unlock()
	if !ok {
		return Confirmation{}, false
	}
	select {
	case <-c.done:
	case <-ctx.Done():
	}
	return g.Get(id)
}

// Cancel withdraws a pending prompt.
func (g *Gate) Cancel(id string) bool {
	g.mu.Lock()
	c, ok := g.confs[id]
	g.mu.Unlock()
	if !ok {
		return false
	}
	c.cancel()
	<-c.done
	return true
}

// Close withdraws every open prompt (daemon shutdown).
func (g *Gate) Close() {
	g.mu.Lock()
	var open []*Confirmation
	for _, c := range g.confs {
		if c.State == StatePending {
			open = append(open, c)
		}
	}
	g.mu.Unlock()
	for _, c := range open {
		c.cancel()
		<-c.done
	}
}

func (g *Gate) gcLocked() {
	cut := g.now().Add(-keepDone).UnixMilli()
	for id, c := range g.confs {
		if c.State != StatePending && c.CreatedAt < cut {
			delete(g.confs, id)
		}
	}
}

func (c *Confirmation) public() Confirmation {
	return Confirmation{ID: c.ID, Reason: c.Reason, State: c.State, Error: c.Error, Method: c.Method,
		MethodLabel: c.MethodLabel, PIN: c.PIN, Hello: c.Hello, CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt}
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Fake is an Authenticator for tests: Answer decides each prompt.
type Fake struct {
	mu      sync.Mutex
	Answer  func(ctx context.Context, reason string) error // nil = confirm
	Unavail error
	Reasons []string
}

// SetAnswer replaces Answer safely while prompts may run.
func (f *Fake) SetAnswer(a func(ctx context.Context, reason string) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Answer = a
}

// SetUnavailable safely models authentication failing while a proven UI survives.
func (f *Fake) SetUnavailable(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Unavail = err
}

// Prompts returns the reasons shown so far.
func (f *Fake) Prompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Reasons...)
}

func (f *Fake) Method() string { return "fake" }
func (f *Fake) Label() string  { return "a test prompt" }
func (f *Fake) Available() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Unavail
}

func (f *Fake) Authenticate(ctx context.Context, reason string) error {
	f.mu.Lock()
	f.Reasons = append(f.Reasons, reason)
	ans := f.Answer
	f.mu.Unlock()
	if ans == nil {
		return nil
	}
	return ans(ctx, reason)
}

// SessionProof returns a proof and its grace credential only to its initiating caller.
func (g *Gate) SessionProof(id, caller string) (Confirmation, string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.confs[id]
	if !ok || c.caller != caller {
		return Confirmation{}, "", false
	}
	credential := ""
	if c.State == StateConfirmed && c.grace == g.proofSession && g.now().UnixMilli() <= c.ExpiresAt {
		credential = c.grace
	}
	return c.public(), credential, true
}
func (g *Gate) OwnsConfirmation(id, caller string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.confs[id]
	return ok && c.caller == caller
}
