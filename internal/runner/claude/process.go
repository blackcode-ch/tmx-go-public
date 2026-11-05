// Package claude runs Claude Code in chat mode: `claude -p` speaking stream-json on
// stdin/stdout, mapped to runner events. See DESIGN.md "Claude stream-json protocol".
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/host"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// EventBuffer is the capacity of Process.Events. The consumer must drain the channel
// continuously: when it is full, reading claude's stdout (and so claude) stalls.
const EventBuffer = 1024

const (
	stderrTail = 8 << 10
	killGrace  = 3 * time.Second
)

type chatRunner struct{ log *slog.Logger }

// New returns the chat-mode runner.
func New() runner.Runner { return chatRunner{log: slog.Default()} }

// Start launches claude. ctx only bounds the start itself: the process lives until Stop or
// until it exits.
func (r chatRunner) Start(ctx context.Context, spec runner.Spec) (runner.Process, error) {
	if spec.Harness != "" && spec.Harness != model.HarnessClaude {
		return nil, fmt.Errorf("chat mode supports only the claude harness, not %q", spec.Harness)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := runner.RefuseElevated(spec); err != nil {
		return nil, err
	}
	path, err := LookPath(spec)
	if err != nil {
		return nil, err
	}
	argv0, args := path, ChatArgs(spec)
	if spec.LowPriority {
		argv0, args = sysinfo.LowPriorityCommand(path, args) // macOS: low disk I/O policy
	}
	cmd := hiddenexec.Command(argv0, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = MergeEnv(BaseEnv(), spec.Env...)
	// One start at a time per CLAUDE_CONFIG_DIR (startqueue.go); BeginStart also checks it.
	ticket, err := BeginStart(ctx, cmd.Env, HolderAgent, spec.OnStartWait)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if started {
			ticket.Settled()
		} else {
			ticket.Release()
		}
	}()
	var c pipes
	if spec.HostDir != "" && spec.HostExe != "" {
		// A session host runs claude: the agent outlives this daemon (runner/host).
		hc, err := host.Spawn(ctx, spec.HostExe, spec.HostDir, host.Spec{Path: argv0, Args: args, Env: cmd.Env,
			Cwd: spec.Cwd, Piped: true, Token: spec.HostToken})
		if err != nil {
			return nil, fmt.Errorf("start claude: %w", err)
		}
		sysinfo.RegisterHosted(hc.PID(), hc.HostPID(), hc.HostStart(), false)
		c = hostPipes{hc}
	} else {
		cmd.WaitDelay = 2 * time.Second // grandchildren holding stderr must not block Wait
		setProcAttr(cmd)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		lp := &localPipes{cmd: cmd, stdin: stdin, stdout: stdout, stderr: &tailBuffer{max: stderrTail}}
		cmd.Stderr = lp.stderr
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start claude: %w", err)
		}
		sysinfo.RegisterChild(cmd.Process.Pid, false)
		c = lp
	}
	started = true
	if spec.LowPriority {
		_ = sysinfo.LowerPriority(c.PID()) // best effort; children inherit it
	}
	p := r.newProcess(c, spec, 0)
	p.parser.sessionID = spec.SessionID
	var o out
	p.parser.setState(&o, model.StateStarting)
	p.emitAll(o.events)
	go p.run()
	if err := p.write(controlRequest(p.parser.initID, "initialize")); err != nil {
		p.log.Warn("claude: initialize not sent", "err", err)
	}
	return p, nil
}

func (r chatRunner) newProcess(c pipes, spec runner.Spec, offset uint64) *process {
	p := &process{
		c:      c,
		in:     true,
		events: make(chan runner.Event, EventBuffer),
		done:   make(chan struct{}),
		log:    r.log.With("agent", spec.Name),
		offset: offset,
		asked:  map[string]runner.ApprovalRequest{},
	}
	p.parser = newParser(p.nextID(), p.log)
	return p
}

// Reattach connects to the session host an earlier daemon left spec's claude running in
// (spec.HostDir), and goes on with its output exactly where that daemon stopped, with what it
// knew (the session, unanswered permission requests). It also returns the credential claude
// was started with. ErrNoHost when there is no host to reattach to.
func (r chatRunner) Reattach(ctx context.Context, spec runner.Spec) (runner.Process, string, error) {
	if spec.HostDir == "" {
		return nil, "", runner.ErrNoHost
	}
	var h handover
	from := host.End // no handover (the daemon died without one): only what comes next, never twice
	if host.LoadState(spec.HostDir, &h) {
		from = h.Offset
	}
	hc, err := host.Dial(spec.HostDir, from)
	if err != nil {
		return nil, "", err
	}
	host.ClearState(spec.HostDir) // taken over: a crash of this daemon must not replay it again
	sysinfo.RegisterHosted(hc.PID(), hc.HostPID(), hc.HostStart(), false)
	p := r.newProcess(hostPipes{hc}, spec, hc.Start())
	p.parser.sessionID = h.SessionID
	if p.parser.sessionID == "" {
		p.parser.sessionID = spec.SessionID
	}
	if h.InitID != "" {
		p.parser.initID = h.InitID
	}
	p.parser.busy = h.Busy
	var o out
	st := h.State
	if st == "" {
		st = model.StateIdle
	}
	p.parser.setState(&o, st)
	p.mu.Lock()
	p.emitAll(o.events)
	if p.parser.sessionID != "" { // the conversation exists: the fleet resumes it if the host ever goes
		p.emit(runner.Event{Kind: runner.EvSession, SessionID: p.parser.sessionID})
	}
	// Unanswered permission requests are asked again, as they were: the daemon that saw them
	// is gone, and claude still waits for the answers.
	for id, a := range h.Asked {
		if len(a.Input) == 0 {
			continue
		}
		p.parser.pending[id] = a.Input
		a.ID = id
		p.emit(runner.Event{Kind: runner.EvApproval, Approval: &a})
	}
	p.mu.Unlock()
	go p.run()
	return p, hc.Token(), nil
}

// Detach leaves a hosted claude running and ends this process's view of it (a daemon
// restart): its events close without EvExit, and the next daemon gets the handover. Claude
// as the daemon's own child cannot be detached (false).
func (p *process) Detach() bool {
	hp, ok := p.c.(hostPipes)
	if !ok {
		return false
	}
	p.mu.Lock()
	p.detached = true
	p.mu.Unlock()
	_ = hp.Close() // run ends; it hands over (saveHandover)
	return true
}

func (p *process) saveHandover() {
	hp, ok := p.c.(hostPipes)
	if !ok {
		return
	}
	p.mu.Lock()
	h := handover{Offset: p.offset, SessionID: p.parser.sessionID, State: p.parser.state, Busy: p.parser.busy,
		Asked: map[string]runner.ApprovalRequest{}, InitID: p.parser.initID}
	for id := range p.parser.pending {
		if a, ok := p.asked[id]; ok {
			h.Asked[id] = a
		}
	}
	p.mu.Unlock()
	if err := hp.SaveState(h); err != nil {
		p.log.Warn("claude: handover not saved; the next daemon replays what the host kept", "err", err)
	}
}

type process struct {
	c   pipes
	in  bool       // stdin is open (guarded by wmu)
	wmu sync.Mutex // serialises stdin writes
	seq atomic.Int64

	mu       sync.Mutex // guards parser, emission order, offset and detached
	parser   *parser
	events   chan runner.Event
	closed   bool
	offset   uint64 // in claude's output: the end of the last complete line handled
	detached bool
	asked    map[string]runner.ApprovalRequest // approvals emitted and not withdrawn (handover)

	done     chan struct{} // closed after EvExit was emitted and events closed
	stopOnce sync.Once
	log      *slog.Logger
}

func (p *process) Events() <-chan runner.Event { return p.events }
func (p *process) PID() int                    { return p.c.PID() }
func (p *process) Replay() []byte              { return nil }
func (p *process) Resize(int, int) error       { return nil }

func (p *process) Write([]byte, bool) error {
	return errors.New("raw input is only for terminal mode")
}

func (p *process) nextID() string { return fmt.Sprintf("tmx-%d", p.seq.Add(1)) }

// emit sends one event; the caller holds p.mu or is the only goroutine left.
func (p *process) emit(ev runner.Event) {
	if ev.Approval != nil {
		switch ev.Kind {
		case runner.EvApproval:
			p.asked[ev.Approval.ID] = *ev.Approval
		case runner.EvApprovalCancelled:
			delete(p.asked, ev.Approval.ID)
		}
	}
	if !p.closed {
		p.events <- ev
	}
}

func (p *process) write(line []byte) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if !p.in {
		return errors.New("claude: input closed")
	}
	_, err := p.c.Write(line)
	return err
}

// Send starts a user turn. Sending while a turn runs is allowed: claude queues it.
func (p *process) Send(text string) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if !p.in {
		return errors.New("claude: input closed")
	}
	p.mu.Lock()
	p.parser.busy = true
	sid := p.parser.sessionID
	p.emit(runner.Event{Kind: runner.EvTimeline, Entry: &model.TimelineEntry{Kind: model.EntryUser, Text: text}})
	if p.parser.state != model.StateWaiting {
		var o out
		p.parser.setState(&o, model.StateWorking)
		p.emitAll(o.events)
	}
	p.mu.Unlock()
	_, err := p.c.Write(userMessage(text, sid))
	return err
}

func (p *process) Answer(requestID string, d runner.Decision) error {
	p.mu.Lock()
	input, ok := p.parser.pending[requestID]
	if ok {
		delete(p.parser.pending, requestID)
		delete(p.asked, requestID)
		if len(p.parser.pending) == 0 {
			var o out
			p.parser.setState(&o, model.StateWorking)
			p.emitAll(o.events)
		}
	}
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending permission request %q", requestID)
	}
	if !d.Allow {
		return p.write(permissionDeny(requestID, d.Message))
	}
	if d.UpdatedInput != nil {
		if !json.Valid(d.UpdatedInput) {
			return errors.New("updated input is not valid JSON")
		}
		input = d.UpdatedInput
	}
	return p.write(permissionAllow(requestID, input))
}

func (p *process) Interrupt() error {
	return p.write(controlRequest(p.nextID(), "interrupt"))
}

// Stop closes stdin (claude exits after the current turn), then on ctx expiry terminates the
// process group and kills it after killGrace.
func (p *process) Stop(ctx context.Context) error {
	p.stopOnce.Do(func() {
		p.wmu.Lock()
		if p.in {
			_ = p.c.CloseInput()
			p.in = false
		}
		p.wmu.Unlock()
	})
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
	}
	p.c.Terminate()
	select {
	case <-p.done:
		return nil
	case <-time.After(killGrace):
	}
	p.c.Kill()
	<-p.done
	return nil
}

func (p *process) emitAll(evs []runner.Event) {
	for _, ev := range evs {
		p.emit(ev)
	}
}

// run reads stdout until EOF, then reaps the process and emits EvExit (or, detached from a
// host, hands over to the next daemon and closes its events without one).
func (p *process) run() {
	br := bufio.NewReaderSize(p.c, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			p.handle(line)
			p.mu.Lock()
			p.offset += uint64(len(line))
			p.mu.Unlock()
		} else if len(line) > 0 && err != nil {
			p.mu.Lock()
			detached := p.detached
			p.mu.Unlock()
			if !detached {
				p.handle(line) // claude's last words without a newline
			}
		}
		if err != nil {
			break
		}
	}
	code, waitErr := p.c.Wait()
	p.mu.Lock()
	detached := p.detached || errors.Is(waitErr, host.ErrDetached)
	p.mu.Unlock()
	if detached {
		p.saveHandover()
		p.mu.Lock()
		p.closed = true
		close(p.events)
		p.mu.Unlock()
		close(p.done)
		return
	}

	ev := runner.Event{Kind: runner.EvExit, ExitCode: code}
	if ev.ExitCode != 0 {
		msg := p.c.Stderr()
		switch {
		case msg != "" && waitErr != nil:
			ev.Err = fmt.Errorf("%v: %s", waitErr, msg)
		case msg != "":
			ev.Err = errors.New(msg)
		default:
			ev.Err = waitErr
		}
	}
	p.mu.Lock()
	p.emit(ev)
	p.closed = true
	close(p.events)
	p.mu.Unlock()
	close(p.done)
}

func (p *process) handle(line []byte) {
	p.mu.Lock()
	o := p.parser.line(line)
	p.emitAll(o.events)
	p.mu.Unlock()
	for _, r := range o.replies {
		if err := p.write(r); err != nil {
			p.log.Debug("claude: reply not sent", "err", err)
		}
	}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
