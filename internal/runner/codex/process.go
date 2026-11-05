// Package codex runs OpenAI's Codex CLI in chat mode: `codex app-server`, JSON-RPC on
// stdio, mapped to runner events (turns, items, approvals as EvApproval). See DESIGN.md
// "Codex app-server protocol".
package codex

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/codexguard"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// EventBuffer is the capacity of Process.Events; the consumer must drain it.
const EventBuffer = 1024

const (
	stderrTail = 8 << 10
	killGrace  = 3 * time.Second
)

// deniedDefault is the fleet's deny message when the owner gave none; only a real reason is
// passed on to the turn.
const deniedDefault = "The owner denied this."

type chatRunner struct{ log *slog.Logger }

// New returns the Codex chat-mode runner.
func New() runner.Runner { return chatRunner{log: slog.Default()} }

func (chatRunner) Supports(h model.Harness) bool { return h == model.HarnessCodex }

// Start launches the app-server and opens (or resumes) the conversation. ctx only bounds the
// start itself.
func (r chatRunner) Start(ctx context.Context, spec runner.Spec) (runner.Process, error) {
	if spec.Harness != model.HarnessCodex {
		return nil, fmt.Errorf("the codex runner does not run harness %q", spec.Harness)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := harness.ValidateCodexLaunch(spec); err != nil {
		return nil, err
	}
	spec, lease, err := harness.PrepareCodexGuard(spec)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if lease != nil && !started {
			lease.Close()
		}
	}()
	path, err := harness.LookPath(model.HarnessCodex, spec.Command)
	if err != nil {
		return nil, err
	}
	argv0, args := path, harness.CodexAppServerArgs(spec)
	if spec.LowPriority {
		argv0, args = sysinfo.LowPriorityCommand(argv0, args) // macOS: low disk I/O policy
	}
	cmd := hiddenexec.Command(argv0, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = claude.MergeEnv(claude.BaseEnv(), spec.Env...)
	cmd.WaitDelay = 2 * time.Second
	setProcAttr(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p := &process{
		cmd: cmd, stdin: stdin,
		events:     make(chan runner.Event, EventBuffer),
		stderr:     &tailBuffer{max: stderrTail},
		done:       make(chan struct{}),
		log:        r.log.With("agent", spec.Name),
		guardLease: lease,
	}
	cmd.Stderr = p.stderr
	p.parser = &parser{
		log: p.log, nextID: p.nextID, resumeID: spec.SessionID, threadParams: threadParams(spec),
		pending: map[string]pendingReq{}, items: map[string]wireItem{},
	}
	p.parser.initID = p.nextID()
	if lease != nil {
		p.parser.guardExecutable = spec.CodexGuardExecutable
		home, _ := os.UserHomeDir()
		p.parser.guardContext = &guard.Context{OS: runtime.GOOS, Cwd: spec.Cwd, Own: spec.Cwd, Home: home, Temp: []string{os.TempDir()}, StateDir: spec.CodexGuardHome, Env: os.Getenv}
		p.parser.guardStates = spec.GuardStates // the owner's rule switches, read per check
		for _, entry := range spec.Env {
			key, value, ok := strings.Cut(entry, "=")
			if !ok {
				continue
			}
			if key == guard.EnvState {
				p.parser.guardContext.StateDir = value
			}
			if key == guard.EnvWorkspace {
				p.parser.guardContext.Workspace = value
			}
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}
	started = true
	sysinfo.RegisterChild(cmd.Process.Pid, false)
	if spec.LowPriority {
		_ = sysinfo.LowerPriority(cmd.Process.Pid) // best effort; children inherit it
	}
	var o out
	p.parser.setState(&o, model.StateStarting)
	p.emitAll(o.events)
	go p.run(stdout)
	init := request(p.parser.initID, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "tmx", "title": "tmx", "version": config.Version},
		"capabilities": nil,
	})
	if err := p.write(init); err != nil {
		p.log.Warn("codex: initialize not sent", "err", err)
	}
	return p, nil
}

// threadParams: the conversation's settings from the spec and tmx's policy.
func threadParams(spec runner.Spec) map[string]any {
	sandbox, approval := harness.CodexPolicy(spec)
	if sandbox == "" {
		sandbox = "danger-full-access"
	}
	params := map[string]any{"cwd": spec.Cwd, "approvalPolicy": approval, "sandbox": sandbox}
	if spec.Model != "" {
		params["model"] = spec.Model
	}
	if spec.SystemPrompt != "" {
		params["developerInstructions"] = spec.SystemPrompt
	}
	return params
}

type process struct {
	guardLease *codexguard.Lease
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	wmu        sync.Mutex // serialises stdin writes
	seq        atomic.Int64

	mu     sync.Mutex // guards parser and emission order
	parser *parser
	events chan runner.Event
	closed bool

	stderr   *tailBuffer
	done     chan struct{}
	stopOnce sync.Once
	log      *slog.Logger
}

func (p *process) Events() <-chan runner.Event { return p.events }
func (p *process) PID() int                    { return p.cmd.Process.Pid }
func (p *process) Replay() []byte              { return nil }
func (p *process) Resize(int, int) error       { return nil }

func (p *process) Write([]byte, bool) error {
	return errors.New("raw input is only for terminal mode")
}

func (p *process) nextID() string { return fmt.Sprintf("tmx-%d", p.seq.Add(1)) }

func (p *process) emit(ev runner.Event) {
	if !p.closed {
		p.events <- ev
	}
}

func (p *process) emitAll(evs []runner.Event) {
	for _, ev := range evs {
		p.emit(ev)
	}
}

func (p *process) write(line []byte) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if p.stdin == nil {
		return errors.New("codex: input closed")
	}
	_, err := p.stdin.Write(line)
	return err
}

// apply emits what the parser produced and writes its replies, in order.
func (p *process) apply(o out) error {
	p.emitAll(o.events)
	var err error
	for _, r := range o.replies {
		if e := p.write(r); e != nil && err == nil {
			err = e
		}
	}
	if o.close {
		go p.closeInput()
	}
	return err
}

// Send starts a user turn (queued until the conversation is open; added to a running turn).
func (p *process) Send(text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var o out
	p.parser.send(&o, text)
	return p.apply(o)
}

func (p *process) Answer(requestID string, d runner.Decision) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var o out
	if err := p.parser.answer(&o, requestID, d); err != nil {
		return err
	}
	if !d.Allow && d.Message != "" && d.Message != deniedDefault && p.parser.busy {
		o.replies = append(o.replies, p.parser.turnStart("[the owner denied that] "+d.Message))
	}
	return p.apply(o)
}

// Interrupt ends the running turn.
func (p *process) Interrupt() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.parser.busy || p.parser.turnID == "" {
		return nil
	}
	return p.write(request(p.nextID(), "turn/interrupt", map[string]any{"threadId": p.parser.threadID, "turnId": p.parser.turnID}))
}

func (p *process) closeInput() {
	p.stopOnce.Do(func() {
		p.wmu.Lock()
		if p.stdin != nil {
			_ = p.stdin.Close()
			p.stdin = nil
		}
		p.wmu.Unlock()
	})
}

// Stop closes stdin (the app-server exits on EOF: verified with 0.163), then on ctx expiry
// terminates the process group and kills it after killGrace.
func (p *process) Stop(ctx context.Context) error {
	p.closeInput()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
	}
	terminate(p.cmd)
	select {
	case <-p.done:
		return nil
	case <-time.After(killGrace):
	}
	kill(p.cmd)
	<-p.done
	return nil
}

func (p *process) run(stdout io.Reader) {
	if p.guardLease != nil {
		defer p.guardLease.Close()
	}
	br := bufio.NewReaderSize(stdout, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			p.mu.Lock()
			if e := p.apply(p.parser.line(line)); e != nil {
				p.log.Debug("codex: reply not sent", "err", e)
			}
			p.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	waitErr := p.cmd.Wait()
	reapGroup(p.cmd)
	ev := runner.Event{Kind: runner.EvExit, ExitCode: -1}
	if st := p.cmd.ProcessState; st != nil {
		ev.ExitCode = st.ExitCode()
	}
	if ev.ExitCode != 0 {
		msg := strings.TrimSpace(p.stderr.String())
		switch {
		case msg != "" && waitErr != nil:
			ev.Err = fmt.Errorf("%v: %s", waitErr, clip(msg, 2000))
		case msg != "":
			ev.Err = errors.New(clip(msg, 2000))
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
