// Package fake is a scripted runner.Runner for tests: tests emit events on a Process and
// inspect what the fleet sent to it (Send/Write/Answer/Interrupt/Stop).
package fake

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// Runner records every Start. Set StartErr to make starts fail, OnStart to script a new
// process (called before Start returns), OnSend to script replies to user turns. Set the
// hooks with Script (or before the runner is shared) to stay race-free.
type Runner struct {
	mu       sync.Mutex
	procs    []*Process
	StartErr error
	OnStart  func(*Process)
	OnSend   func(p *Process, text string)
	nextPID  atomic.Int64
	notifyCh chan *Process
}

// Script sets the hooks under the runner's lock.
func (r *Runner) Script(onStart func(*Process), onSend func(*Process, string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.OnStart, r.OnSend = onStart, onSend
}

// FailStarts makes every following Start return err (nil = succeed again).
func (r *Runner) FailStarts(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.StartErr = err
}

func New() *Runner { return &Runner{notifyCh: make(chan *Process, 64)} }

// Started delivers each process as it is started (buffered; tests may ignore it).
func (r *Runner) Started() <-chan *Process { return r.notifyCh }

func (r *Runner) Start(ctx context.Context, spec runner.Spec) (runner.Process, error) {
	r.mu.Lock()
	err, onStart := r.StartErr, r.OnStart
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	p := &Process{
		Spec:       spec,
		events:     make(chan runner.Event, 1024),
		pid:        int(r.nextPID.Add(1)) + 1000,
		runner:     r,
		pendAfterN: -1,
		done:       make(chan struct{}),
	}
	r.mu.Lock()
	r.procs = append(r.procs, p)
	r.mu.Unlock()
	if onStart != nil {
		onStart(p)
	}
	select {
	case r.notifyCh <- p:
	default:
	}
	return p, nil
}

// Procs returns every process started so far, oldest first.
func (r *Runner) Procs() []*Process {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*Process(nil), r.procs...)
}

// Last returns the newest process started for an agent (nil if none).
func (r *Runner) Last(agentID string) *Process {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.procs) - 1; i >= 0; i-- {
		if r.procs[i].Spec.AgentID == agentID {
			return r.procs[i]
		}
	}
	return nil
}

// Answer is one recorded Process.Answer call.
type Answer struct {
	RequestID string
	Decision  runner.Decision
}

// Process is a fake runner.Process.
type Process struct {
	Spec   runner.Spec
	runner *Runner
	events chan runner.Event
	pid    int

	mu         sync.Mutex
	sent       []string
	written    []byte
	answers    []Answer
	interrupts int
	stopped    bool
	cols, rows int
	replay     []byte
	closed     bool
	pending    bool // SetPending: an owner-only approval is waiting, as the term runner's watcher would report
	pendAfterN int  // PendAfter: go pending after this many more non-owner Write calls (-1 = unset)
	done       chan struct{}
}

var ErrExited = errors.New("fake: process has exited")

// Emit queues an event for the fleet. It is a no-op after the process exited.
func (p *Process) Emit(ev runner.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.events <- ev
}

// State emits a state event.
func (p *Process) State(s model.AgentState) { p.Emit(runner.Event{Kind: runner.EvState, State: s}) }

// Entry emits a timeline event.
func (p *Process) Entry(kind model.EntryKind, text string) {
	p.Emit(runner.Event{Kind: runner.EvTimeline, Entry: &model.TimelineEntry{Kind: kind, Text: text}})
}

// Turn emits a complete chat turn: working, an assistant entry, a result, usage, idle.
func (p *Process) Turn(reply string, costUSD float64, turns int) {
	p.State(model.StateWorking)
	p.Entry(model.EntryAssistant, reply)
	p.Entry(model.EntryResult, "")
	p.Emit(runner.Event{Kind: runner.EvUsage, CostUSD: costUSD, Turns: turns})
	p.State(model.StateIdle)
}

// Exit emits EvExit and closes the event channel (once).
func (p *Process) Exit(code int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.events <- runner.Event{Kind: runner.EvExit, ExitCode: code, Err: err}
	p.closed = true
	close(p.events)
	close(p.done)
}

// Done is closed when the process has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

func (p *Process) Events() <-chan runner.Event { return p.events }

func (p *Process) Send(text string) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrExited
	}
	p.sent = append(p.sent, text)
	p.mu.Unlock()
	p.runner.mu.Lock()
	f := p.runner.OnSend
	p.runner.mu.Unlock()
	if f != nil {
		f(p, text)
	}
	return nil
}

func (p *Process) Write(b []byte, owner bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrExited
	}
	if !owner && p.pendAfterN == 0 {
		p.pending, p.pendAfterN = true, -1
	}
	if !owner && p.pending {
		return runner.ErrApprovalPending
	}
	if !owner && p.pendAfterN > 0 {
		p.pendAfterN--
	}
	p.written = append(p.written, b...)
	return nil
}

// SetPending scripts whether a non-owner Write is refused with runner.ErrApprovalPending, as
// the term runner's watcher would while a menu is pending.
func (p *Process) SetPending(pending bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending, p.pendAfterN = pending, -1
}

// PendAfter scripts a transition to pending landing mid-sequence: the next n non-owner writes
// (e.g. SendKeys's one-call-per-key) go through as usual, then the (n+1)th and every write
// after it are refused - as if an approval went pending between those two writes.
func (p *Process) PendAfter(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending, p.pendAfterN = false, n
}

func (p *Process) Resize(cols, rows int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cols, p.rows = cols, rows
	return nil
}

func (p *Process) Answer(requestID string, d runner.Decision) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrExited
	}
	p.answers = append(p.answers, Answer{RequestID: requestID, Decision: d})
	return nil
}

func (p *Process) Interrupt() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interrupts++
	return nil
}

// Stop records the call and exits the process with code 0.
func (p *Process) Stop(ctx context.Context) error {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	p.Exit(0, nil)
	return nil
}

// SetReplay sets what Replay returns.
func (p *Process) SetReplay(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replay = append([]byte(nil), b...)
}

func (p *Process) Replay() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.replay...)
}

func (p *Process) PID() int { return p.pid }

// Sent returns the user turns sent so far.
func (p *Process) Sent() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.sent...)
}

// Written returns the raw bytes written so far.
func (p *Process) Written() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.written...)
}

// Answers returns the approval answers so far.
func (p *Process) Answers() []Answer {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Answer(nil), p.answers...)
}

func (p *Process) Interrupts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.interrupts
}

func (p *Process) Stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

func (p *Process) Size() (cols, rows int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cols, p.rows
}
