package accounts

import (
	"context"
	"errors"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	pty "github.com/aymanbagabas/go-pty"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
	"github.com/blackcode-ch/tmx-go/internal/runner/term"
)

// The harnesses sign in through their own command (`claude auth login`, `codex login`, `agy
// auth login` or agy's first run) in a pseudo-terminal the owner sees in the app, framed like an
// agent's terminal. They open the browser themselves. tmx only relays the terminal: what is
// typed or printed is never logged or stored, and the replay is dropped with the next sign-in.

const (
	loginReplay            = 256 << 10
	loginCols, loginRows   = 100, 30
	subBuffer              = 256
	maxTermCols, maxTermRs = 1000, 500
)

// signedInPoll is how often a sign-in that stays open (def.signedIn) is checked.
var signedInPoll = 2 * time.Second

// ErrNoSignOut: the harness has no sign-out command (Gemini CLI: /signout inside it).
var ErrNoSignOut = errors.New("this harness has no sign-out command")

// ExitDisabled is the exit code of a production start's disabled stub (harness.ExitDisabled).
const ExitDisabled = harness.ExitDisabled

// loginSession is one sign-in in a pty.
type loginSession struct {
	id   string
	h    model.Harness
	path string // the executable
	pt   pty.Pty
	cmd  *pty.Cmd
	ring *term.Ring
	wmu  sync.Mutex // serialises writes to the pty

	mu    sync.Mutex // guards subs, done; the ring is written under it so a replay is exact
	subs  map[*LoginSub]struct{}
	isEnd bool

	cancelled atomic.Bool
	timedOut  atomic.Bool
	finished  atomic.Bool // stopped by tmx because the account shows the sign-in (def.signedIn)
	exitCode  int
	done      chan struct{}
}

// LoginSub is a viewer of a sign-in terminal: Replay first, then C until it is closed (the
// sign-in ended).
type LoginSub struct {
	Replay []byte
	c      chan []byte
	s      *loginSession
}

// C delivers the terminal's output; it is closed when the sign-in ends.
func (sub *LoginSub) C() <-chan []byte { return sub.c }

func (ls *loginSession) ended() bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.isEnd
}

func startLogin(h model.Harness, dir, path string, args, env []string) (*loginSession, error) {
	pt, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("open a terminal: %w", err)
	}
	if err := pt.Resize(loginCols, loginRows); err != nil {
		pt.Close()
		return nil, fmt.Errorf("size the terminal: %w", err)
	}
	cmd := pt.Command(path, args...)
	cmd.Dir = dir
	cmd.Env = env
	term.PrepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		pt.Close()
		return nil, fmt.Errorf("start %s: %w", path, err)
	}
	if runtime.GOOS == "windows" {
		sysinfo.RegisterChild(cmd.Process.Pid, false)
	}
	term.AfterStart(pt)
	ls := &loginSession{id: model.NewID(), h: h, path: path, pt: pt, cmd: cmd, ring: term.NewRing(loginReplay),
		subs: map[*LoginSub]struct{}{}, done: make(chan struct{}), exitCode: -1}
	readDone := make(chan struct{})
	go ls.read(readDone)
	go ls.wait(readDone)
	return ls, nil
}

func (ls *loginSession) read(done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 32<<10)
	for {
		n, err := ls.pt.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			ls.mu.Lock()
			ls.ring.Write(chunk)
			for sub := range ls.subs {
				select {
				case sub.c <- chunk:
				default: // a viewer that does not keep up is dropped; it can attach again
					delete(ls.subs, sub)
					close(sub.c)
				}
			}
			ls.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (ls *loginSession) wait(readDone <-chan struct{}) {
	_ = ls.cmd.Wait()
	select {
	case <-readDone:
	case <-time.After(500 * time.Millisecond): // something it started may still hold the tty
	}
	ls.pt.Close()
	<-readDone
	ls.mu.Lock()
	if st := ls.cmd.ProcessState; st != nil {
		ls.exitCode = st.ExitCode()
	}
	ls.isEnd = true
	for sub := range ls.subs {
		close(sub.c)
	}
	clear(ls.subs)
	ls.mu.Unlock()
	close(ls.done)
}

func (ls *loginSession) attach() *LoginSub {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	sub := &LoginSub{Replay: ls.ring.Bytes(), c: make(chan []byte, subBuffer), s: ls}
	if ls.isEnd {
		close(sub.c)
	} else {
		ls.subs[sub] = struct{}{}
	}
	return sub
}

func (ls *loginSession) detach(sub *LoginSub) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if _, ok := ls.subs[sub]; ok {
		delete(ls.subs, sub)
		close(sub.c)
	}
}

func (ls *loginSession) write(b []byte) error {
	if ls.ended() {
		return ErrNoLogin
	}
	ls.wmu.Lock()
	defer ls.wmu.Unlock()
	_, err := ls.pt.Write(b)
	return err
}

func (ls *loginSession) resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > maxTermCols || rows > maxTermRs {
		return errors.New("terminal size out of range")
	}
	if ls.ended() {
		return ErrNoLogin
	}
	return ls.pt.Resize(cols, rows)
}

// stop ends the sign-in: SIGHUP/SIGTERM to its session, SIGKILL after grace.
func (ls *loginSession) stop(grace time.Duration) {
	term.Terminate(ls.cmd)
	select {
	case <-ls.done:
		return
	case <-time.After(grace):
	}
	term.Kill(ls.cmd)
	<-ls.done
}

// loginHoldSlack: the start queue's backstop for a sign-in comes this long after LoginTimeout
// and the stop's grace (watchLogin normally releases the turn well before).
const loginHoldSlack = time.Minute

// Login starts the sign-in of h's account name ("" = "default") in a terminal, or returns the
// one running. ErrNotInstalled when h is not installed.
func (s *Service) Login(h, name string) (string, error) {
	e := s.entry(h, name)
	if e == nil || h == "" {
		return "", ErrUnknown
	}
	e.loginMu.Lock()
	defer e.loginMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", errors.New("tmx is stopping")
	}
	if e.sess != nil && !e.sess.ended() {
		id := e.sess.id
		s.mu.Unlock()
		return id, nil
	}
	s.mu.Unlock()
	path, err := s.lookPath(e.def.h)
	if err != nil {
		return "", fmt.Errorf("%s is %w", e.def.label, ErrNotInstalled)
	}
	sc := s.scope(e)
	args := e.def.login(s.ctx, sc, path)
	// `claude auth login` may write .claude.json until it ends, so it holds the daemon's claude
	// start-queue turn from the moment it is taken until its process exited: watchLogin
	// releases it (after LoginTimeout it stops the sign-in first). The queue's backstop, a
	// little later, does the same should that ever not happen.
	var ticket *claude.StartTicket
	var started atomic.Pointer[loginSession]
	if e.def.h == model.HarnessClaude {
		backstop := func() {
			if ls := started.Load(); ls != nil {
				ls.timedOut.Store(true)
				ls.stop(s.o.StopGrace)
			}
		}
		if ticket, err = claude.BeginStartHeld(s.ctx, sc.env(), claude.HolderSignIn, s.o.LoginTimeout+s.o.StopGrace+loginHoldSlack, backstop); err != nil {
			return "", err
		}
	}
	ls, err := startLogin(e.def.h, s.workDir(), path, args, sc.env(append([]string{"TERM=xterm-256color"}, e.def.loginEnv...)...))
	if err != nil {
		if ticket != nil {
			ticket.Release() // nothing started
		}
		return "", err
	}
	started.Store(ls)
	s.mu.Lock()
	e.sess = ls
	e.login = Login{State: LoginRunning, Message: "Finish signing in in your browser"}
	s.changedLocked()
	s.wg.Add(1)
	s.mu.Unlock()
	s.publish()
	go s.watchLogin(e, ls, ticket)
	return ls.id, nil
}

// watchLogin ends the sign-in after LoginTimeout, then checks the account and says how it went.
// ticket (claude's start-queue turn, or nil) is released only once the sign-in process exited:
// on timeout it is stopped first (fail closed), never left running without the turn.
func (s *Service) watchLogin(e *entry, ls *loginSession, ticket *claude.StartTicket) {
	defer s.wg.Done()
	t := time.NewTimer(s.o.LoginTimeout)
	// A harness whose sign-in runs inside its app (Gemini CLI) stays open after it: it is
	// stopped once its account shows the sign-in.
	var poll <-chan time.Time
	if e.def.signedIn != nil {
		tick := time.NewTicker(signedInPoll)
		defer tick.Stop()
		poll = tick.C
	}
wait:
	for {
		select {
		case <-ls.done:
			t.Stop()
			break wait
		case <-t.C:
			ls.timedOut.Store(true)
			s.log.Warn("sign-in took too long; stopping it", "harness", e.def.h, "timeout", s.o.LoginTimeout)
			ls.stop(s.o.StopGrace) // returns once the process has exited (killed after StopGrace)
			break wait
		case <-poll:
			if e.def.signedIn(s.scope(e)) {
				t.Stop()
				ls.finished.Store(true)
				ls.stop(s.o.StopGrace)
				break wait
			}
		}
	}
	if ticket != nil {
		ticket.Release() // before checkAndApply, which starts claude through the same queue
	}
	if s.ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	if !ls.cancelled.Load() {
		e.login = Login{State: LoginRunning, Message: "Checking the account"}
		s.changedLocked()
	}
	s.mu.Unlock()
	s.publish()
	s.checkAndApply(e)
	s.mu.Lock()
	if e.sess == ls {
		e.login = loginOutcome(ls, e.acct)
	}
	s.changedLocked()
	s.mu.Unlock()
	s.publish()
}

func loginOutcome(ls *loginSession, a Account) Login {
	switch {
	case ls.cancelled.Load():
		return Login{State: LoginIdle, Message: "Sign-in cancelled"}
	case ls.timedOut.Load():
		return Login{State: LoginFailed, Message: "The sign-in took longer than 10 minutes and was stopped"}
	case ls.finished.Load():
		// stopped by tmx once signed in: its exit code says nothing (the account below does)
		if !a.Connected {
			return Login{State: LoginFailed, Message: "The sign-in ended, but " + a.Label + " is not signed in"}
		}
		if a.Account.Email != "" {
			return Login{State: LoginSucceeded, Message: "Signed in as " + a.Account.Email}
		}
		return Login{State: LoginSucceeded, Message: "Signed in"}
	case ls.exitCode == ExitDisabled:
		// The sign-in looks past such stubs (harness.SignInPath): this one was all there was.
		return Login{State: LoginFailed, Message: fmt.Sprintf("%s is disabled on this machine by tmx's start script (%s "+
			"exits %d), and no other %s was found: install %s", a.Label, ls.path, ExitDisabled, a.Label, a.Label)}
	case ls.exitCode != 0:
		return Login{State: LoginFailed, Message: fmt.Sprintf("The sign-in ended with exit code %d", ls.exitCode)}
	case !a.Connected:
		return Login{State: LoginFailed, Message: "The sign-in ended, but " + a.Label + " is not signed in"}
	case a.Account.Email != "":
		return Login{State: LoginSucceeded, Message: "Signed in as " + a.Account.Email}
	}
	return Login{State: LoginSucceeded, Message: "Signed in"}
}

// CancelLogin stops h's running sign-in (none running: nothing to do).
func (s *Service) CancelLogin(h, name string) error {
	e := s.entry(h, name)
	if e == nil || h == "" {
		return ErrUnknown
	}
	s.mu.Lock()
	ls := e.sess
	s.mu.Unlock()
	if ls == nil || ls.ended() {
		return nil
	}
	ls.cancelled.Store(true)
	ls.stop(s.o.StopGrace)
	return nil
}

func (s *Service) session(h, name string) (*loginSession, error) {
	e := s.entry(h, name)
	if e == nil || h == "" {
		return nil, ErrUnknown
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.sess == nil {
		return nil, ErrNoLogin
	}
	return e.sess, nil
}

// AttachLogin watches h's sign-in terminal: the running one, or the last one's replay (C is
// then closed at once). ErrNoLogin when there was none since tmx started.
func (s *Service) AttachLogin(h, name string) (*LoginSub, error) {
	ls, err := s.session(h, name)
	if err != nil {
		return nil, err
	}
	return ls.attach(), nil
}

// DetachLogin stops a viewer.
func (s *Service) DetachLogin(sub *LoginSub) {
	if sub != nil {
		sub.s.detach(sub)
	}
}

// LoginInput types into h's running sign-in.
func (s *Service) LoginInput(h, name string, b []byte) error {
	ls, err := s.session(h, name)
	if err != nil {
		return err
	}
	return ls.write(b)
}

// LoginResize sizes h's sign-in terminal.
func (s *Service) LoginResize(h, name string, cols, rows int) error {
	ls, err := s.session(h, name)
	if err != nil {
		return err
	}
	return ls.resize(cols, rows)
}

// Logout signs h out with its own command, then checks the account again. A running sign-in
// is cancelled first.
func (s *Service) Logout(ctx context.Context, h, name string) (Payload, error) {
	e := s.entry(h, name)
	if e == nil || h == "" {
		return Payload{}, ErrUnknown
	}
	_ = s.CancelLogin(h, name)
	e.loginMu.Lock()
	defer e.loginMu.Unlock()
	path, err := s.lookPath(e.def.h)
	if err != nil {
		return Payload{}, fmt.Errorf("%s is %w", e.def.label, ErrNotInstalled)
	}
	sc := s.scope(e)
	run := sc.run
	if e.def.h == model.HarnessClaude {
		run = sc.claudeRun
	}
	args := e.def.logout(ctx, sc, path)
	if args == nil {
		return Payload{}, fmt.Errorf("%s: %w: run it and type /signout", e.def.label, ErrNoSignOut)
	}
	r := run(ctx, s.o.CheckTimeout, path, args...)
	if r.err != nil {
		return Payload{}, fmt.Errorf("%s's sign-out: %w", e.def.label, r.err)
	}
	s.checkAndApply(e)
	s.mu.Lock()
	e.login = Login{State: LoginIdle}
	s.changedLocked()
	connected := e.acct.Connected
	s.mu.Unlock()
	s.publish()
	if r.exitCode != 0 && connected {
		return s.Payload(), fmt.Errorf("%s's sign-out failed (exit code %d): %s", e.def.label, r.exitCode, firstLine(r.text(), 160))
	}
	return s.Payload(), nil
}
