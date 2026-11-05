package fleet

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/term/screen"
)

// termBuffer is each terminal viewer's queue of output chunks. A viewer that falls this far
// behind loses chunks (Lagged reports it) rather than slowing the agent.
const termBuffer = 1024

// TerminalSub is one viewer of an agent's terminal output.
type TerminalSub struct {
	AgentID string
	// Replay is the recent output at the moment of attaching (send it first).
	Replay []byte
	ch     chan []byte
	once   sync.Once
	lagged atomic.Bool
}

// C delivers output chunks; it is closed when the process exits or the agent is removed.
func (s *TerminalSub) C() <-chan []byte { return s.ch }

// Lagged reports whether output was dropped because the viewer fell behind.
func (s *TerminalSub) Lagged() bool { return s.lagged.Load() }

func (s *TerminalSub) close() { s.once.Do(func() { close(s.ch) }) }

// AttachTerminal subscribes to an agent's terminal output (any caller may watch).
func (f *Fleet) AttachTerminal(idOrName string) (*TerminalSub, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.resolveLocked(idOrName)
	if err != nil {
		return nil, err
	}
	if r.proc == nil {
		return nil, errf(KindConflict, "%s is not running", r.a.Name)
	}
	s := &TerminalSub{AgentID: r.a.ID, Replay: r.proc.Replay(), ch: make(chan []byte, termBuffer)}
	if r.terms == nil {
		r.terms = map[*TerminalSub]struct{}{}
	}
	r.terms[s] = struct{}{}
	return s, nil
}

// DetachTerminal ends a subscription.
func (f *Fleet) DetachTerminal(s *TerminalSub) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.agents[s.AgentID]; ok {
		delete(r.terms, s)
	}
	s.close()
}

// TerminalInput writes raw bytes to an agent's terminal (owner only).
func (f *Fleet) TerminalInput(c Caller, idOrName string, b []byte) error {
	if !c.Owner {
		return errf(KindForbidden, "only the owner can type into a terminal")
	}
	f.mu.Lock()
	r, err := f.resolveLocked(idOrName)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	proc := r.proc
	r.humanInputAt = time.Now()
	f.mu.Unlock()
	if proc == nil {
		return errf(KindConflict, "the agent is not running")
	}
	return proc.Write(b, true) // c.Owner checked above
}

// TerminalResize resizes an agent's terminal (owner only).
func (f *Fleet) TerminalResize(c Caller, idOrName string, cols, rows int) error {
	if !c.Owner {
		return errf(KindForbidden, "only the owner can resize a terminal")
	}
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 1000 {
		return errf(KindInvalid, "invalid terminal size %dx%d", cols, rows)
	}
	f.mu.Lock()
	r, err := f.resolveLocked(idOrName)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	proc := r.proc
	f.mu.Unlock()
	if proc == nil {
		return errf(KindConflict, "the agent is not running")
	}
	return proc.Resize(cols, rows)
}

// screenCols, screenRows render Screen (fleet control's "screen" tool) when the live terminal's
// own size is not otherwise known; matches internal/runner/term's default.
const screenCols, screenRows = 120, 32

// TypeText types text into an agent's terminal and presses Enter unless submit is false
// (fleet control's "type"; callers follow the same tier rule as start/stop/restart - the owner
// on anyone, a head or lead on its own subtree).
//
// The owner types as at the keyboard: raw bytes, anything goes. An agent's text is not
// keystrokes (agentTypeText): it is delivered like mail, through Process.Send, so it can't
// answer a menu, run a slash command or a shell escape, or change the TUI's permissions.
func (f *Fleet) TypeText(c Caller, idOrName, text string, submit bool) error {
	if !c.Owner && c.Peer == "" {
		return f.agentTypeText(c, idOrName, text, submit)
	}
	proc, err := f.procForWriteAct(c, idOrName)
	if err != nil {
		return err
	}
	if submit {
		text += "\r"
	}
	// A peer writes raw text (unlike agentTypeText's mail-relay), but never with the owner's
	// unguarded strength: Write(owner=true) skips the pending-approval/menu gate and a shell's
	// owner-only refusal, which only the literal owner at the real keyboard may bypass
	// (go-reviewer-o, #161 review - machine protection, rule 1).
	if err := proc.Write([]byte(text), c.Owner); err != nil {
		return writeActErr(err, idOrName)
	}
	return nil
}

// MaxTypedBytes caps an agent's typed text (the owner's is not capped), as mail is.
const MaxTypedBytes = MaxBodyBytes

// agentTypeText is TypeText for a head or lead. The text is cleaned (runner.PlainText: no
// Esc or other control bytes that could end the paste and become keys), refused when it
// starts with / or ! (a slash command or the TUI's shell escape), prefixed like mail so the
// target knows it is not the owner speaking (mailRules: a turn without the prefix is the
// owner's), and handed to Process.Send: typed as a paste when the TUI's input box is idle,
// queued while a menu or a turn is on the screen. It reaches only a harness's TUI in terminal
// mode (a shell would run it; a chat agent takes mail).
func (f *Fleet) agentTypeText(c Caller, idOrName, text string, submit bool) error {
	if !submit {
		return errf(KindInvalid, "an agent's typed text is always sent; only the owner can leave a draft in the input box")
	}
	if len(text) > MaxTypedBytes {
		return errf(KindInvalid, "the text is longer than %d KB", MaxTypedBytes>>10)
	}
	text = strings.TrimSpace(runner.PlainText(text))
	if text == "" {
		return errf(KindInvalid, "there is nothing to type")
	}
	if text[0] == '/' || text[0] == '!' {
		return errf(KindForbidden, "text starting with %q is a command for the agent's tool; only the owner can type that (send a message instead)", text[:1])
	}
	f.mu.Lock()
	r, proc, err := f.agentInputTargetLocked(c, idOrName)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	if wait := TypingQuiet - time.Since(r.humanInputAt); wait > 0 {
		f.mu.Unlock()
		return errf(KindConflict, "the owner typed into %s in the last %s; try again in %s", r.a.Name, TypingQuiet, wait.Round(time.Second))
	}
	line := f.mailLineLocked(model.Message{From: c.AgentID, Body: text})
	f.appendLocked(r, model.TimelineEntry{Kind: model.EntryMessageIn, Text: text,
		Meta: map[string]any{"from": c.AgentID, "from_name": f.nameOfLocked(c.AgentID), "typed": true}})
	f.mu.Unlock()
	return proc.Send(line)
}

// agentInputTargetLocked resolves the target of an agent's type or keys: the tier rule, then
// a running harness TUI in terminal mode (runner/term's watcher reads its screen and gates
// input). A shell is the owner's alone: what is typed into it runs, with no guard and no
// approval. A chat agent has no terminal; it takes mail.
func (f *Fleet) agentInputTargetLocked(c Caller, idOrName string) (*agentRec, runner.Process, error) {
	r, err := f.resolveLocked(idOrName)
	if err != nil {
		return nil, nil, err
	}
	if err := f.authorizeActLocked(c, r); err != nil {
		return nil, nil, err
	}
	if r.a.Harness == model.HarnessShell {
		return nil, nil, errf(KindForbidden, "only the owner can type into a shell")
	}
	if info, ok := harness.Get(r.a.Harness); !ok || info.Patterns == nil {
		return nil, nil, errf(KindForbidden, "only the owner can type into %s", r.a.Name)
	}
	if r.a.Mode != model.ModeTerminal {
		return nil, nil, errf(KindInvalid, "%s runs in chat mode and has no terminal; send it a message instead", r.a.Name)
	}
	if r.proc == nil {
		return nil, nil, errf(KindConflict, "%s is not running", r.a.Name)
	}
	if f.pausedLocked(r.a.ID) || mailHeldLocked(r) {
		return nil, nil, errf(KindConflict, "%s is paused or switching; try again later", r.a.Name)
	}
	// A fast path with a clear answer; the runner's own gate (a menu on the screen, a candidate
	// or a pending approval) is the race-free one.
	if r.waiting > 0 && r.a.State == model.StateWaiting {
		return nil, nil, errf(KindForbidden, "%s has an approval waiting for the owner; only the owner can type now", r.a.Name)
	}
	return r, r.proc, nil
}

// SendKeys presses one or more keys in an agent's terminal: named keys (enter, esc, ctrl+c,
// up, down, shift+tab, ...) or single characters, space-separated (fleet control's "keys").
// The owner may press any. A head or lead only esc and ctrl+c (runner.NonOwnerKey), into a
// harness's TUI in terminal mode, and not while a menu is on its screen: every other key can
// answer a menu, cycle the TUI's permission mode (shift+tab) or recall a command from history.
func (f *Fleet) SendKeys(c Caller, idOrName, keys string) error {
	seqs, err := screen.Keys(keys)
	if err != nil {
		return errf(KindInvalid, "%s", err)
	}
	var proc runner.Process
	if c.Owner || c.Peer != "" {
		if proc, err = f.procForWriteAct(c, idOrName); err != nil {
			return err
		}
	} else {
		for i, seq := range seqs {
			if !runner.NonOwnerKey(seq) {
				return errf(KindForbidden, "an agent may press only esc and ctrl+c; only the owner can press %q", strings.Fields(keys)[i])
			}
		}
		f.mu.Lock()
		r, p, err := f.agentInputTargetLocked(c, idOrName)
		if err == nil {
			f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem,
				Text: fmt.Sprintf("%s pressed %s", f.nameOfLocked(c.AgentID), strings.Join(strings.Fields(keys), " "))})
		}
		f.mu.Unlock()
		if err != nil {
			return err
		}
		proc = p
	}
	// Write(owner=true) is the literal owner's unguarded strength (skips the pending-approval/
	// menu gate and a shell's owner-only refusal): never a peer, even though it may press any
	// key, unlike a head or lead agent (go-reviewer-o, #161 review).
	for _, seq := range seqs {
		if err := proc.Write(seq, c.Owner); err != nil {
			return writeActErr(err, idOrName)
		}
	}
	return nil
}

// writeActErr maps the runner's race-free refusal (an approval went pending between
// procForWriteAct's check and this write) to the same error a caller would have gotten had
// procForWriteAct's own pre-check seen it in time.
func writeActErr(err error, idOrName string) error {
	switch {
	case errors.Is(err, runner.ErrApprovalPending):
		return errf(KindForbidden, "%s has a menu or an approval waiting for the owner; only the owner can type now", idOrName)
	case errors.Is(err, runner.ErrOwnerOnly):
		return errf(KindForbidden, "only the owner can type that into %s", idOrName)
	}
	return err
}

// maxScreenHistory bounds Screen's history argument, so screenRows+history can't allocate an
// oversized (or, for a negative value near MinInt, overflowing) terminal buffer.
const maxScreenHistory = 2000

// Screen renders what an agent's terminal shows right now (fleet control's "screen"). history
// asks for extra scrollback lines on top of what fits the screen; 0 is the current view only.
func (f *Fleet) Screen(c Caller, idOrName string, history int) (string, error) {
	if history < 0 || history > maxScreenHistory {
		return "", errf(KindInvalid, "invalid history %d (0-%d)", history, maxScreenHistory)
	}
	proc, err := f.procForAct(c, idOrName)
	if err != nil {
		return "", err
	}
	replay := proc.Replay()
	if replay == nil {
		return "", errf(KindInvalid, "%s has no terminal to show (chat mode)", idOrName)
	}
	rows := screenRows
	if history > 0 {
		rows += history
	}
	scr := screen.New(screenCols, rows, func([]byte) {})
	defer scr.Close()
	scr.Write(replay)
	return scr.Text(), nil
}

// peerShellRefused is KindForbidden when c is a peer and r is a shell: a shell is the owner's
// alone (its input runs with no guard and no approval, same as agentInputTargetLocked's rule
// for a head or lead agent), never a remote machine's (go-reviewer-o, #161 review).
func peerShellRefused(c Caller, r *agentRec) error {
	if c.Peer != "" && r.a.Harness == model.HarnessShell {
		return errf(KindForbidden, "only the owner can reach a shell")
	}
	return nil
}

// procForAct resolves idOrName under the same tier rule as start/stop/restart and returns its
// running process (KindConflict if it is not running). Read-only (Screen): does not count as
// human input.
func (f *Fleet) procForAct(c Caller, idOrName string) (runner.Process, error) {
	f.mu.Lock()
	r, err := f.resolveLocked(idOrName)
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	if err := f.authorizeActLocked(c, r); err != nil {
		f.mu.Unlock()
		return nil, err
	}
	if err := peerShellRefused(c, r); err != nil {
		f.mu.Unlock()
		return nil, err
	}
	proc := r.proc
	f.mu.Unlock()
	if proc == nil {
		return nil, errf(KindConflict, "%s is not running", r.a.Name)
	}
	return proc, nil
}

// procForWriteAct is procForAct for the owner's own typing (TypeText, SendKeys): it counts as
// human input, so mail waits TypingQuiet before it is typed into the same input box. Agents
// go through agentInputTargetLocked instead.
func (f *Fleet) procForWriteAct(c Caller, idOrName string) (runner.Process, error) {
	if !c.Owner && c.Peer == "" {
		return nil, errf(KindForbidden, "only the owner types raw input")
	}
	f.mu.Lock()
	r, err := f.resolveLocked(idOrName)
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	if err := peerShellRefused(c, r); err != nil {
		f.mu.Unlock()
		return nil, err
	}
	proc := r.proc
	r.humanInputAt = time.Now()
	f.mu.Unlock()
	if proc == nil {
		return nil, errf(KindConflict, "%s is not running", r.a.Name)
	}
	return proc, nil
}

func (f *Fleet) fanOutLocked(r *agentRec, b []byte) {
	if len(r.terms) == 0 || len(b) == 0 {
		return
	}
	chunk := append([]byte(nil), b...)
	for s := range r.terms {
		select {
		case s.ch <- chunk:
		default:
			s.lagged.Store(true)
		}
	}
}

func (f *Fleet) closeTermsLocked(r *agentRec) {
	for s := range r.terms {
		s.close()
	}
	r.terms = nil
}
