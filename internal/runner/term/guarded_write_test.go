package term

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	pty "github.com/aymanbagabas/go-pty"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/term/screen"
)

// blockingPty is a minimal pty.Pty whose Write signals entered (so a test can deterministically
// wait until a write is inside its pty.Write, i.e. past guardedWrite's pending check) then
// blocks until release is closed - no sleeps, no polling.
type blockingPty struct {
	entered chan struct{}
	release chan struct{}
	writes  chan []byte
}

func newBlockingPty() *blockingPty {
	return &blockingPty{entered: make(chan struct{}, 1), release: make(chan struct{}), writes: make(chan []byte, 16)}
}

func (b *blockingPty) Write(p []byte) (int, error) {
	b.entered <- struct{}{}
	<-b.release
	b.writes <- append([]byte(nil), p...)
	return len(p), nil
}
func (b *blockingPty) Read([]byte) (int, error) { return 0, io.EOF }
func (b *blockingPty) Close() error             { return nil }
func (b *blockingPty) Name() string             { return "fake-pty" }
func (b *blockingPty) Command(name string, args ...string) *pty.Cmd {
	return &pty.Cmd{Path: name, Args: args}
}
func (b *blockingPty) CommandContext(context.Context, string, ...string) *pty.Cmd { return &pty.Cmd{} }
func (b *blockingPty) Resize(int, int) error                                      { return nil }
func (b *blockingPty) Fd() uintptr                                                { return 0 }

// newGuardedTestProcess builds a bare process+watcher pair around a blockingPty: no real PTY,
// no subprocess, so the only thing under test is guardedWrite's own locking against the
// detector's own locking (menuLocked/evaluate's p.mu section), reproduced here directly by
// calling the same methods the watcher calls.
func newGuardedTestProcess(t *testing.T) (*process, *blockingPty) {
	t.Helper()
	bp := newBlockingPty()
	return newTestProcess(t, bp), bp
}

// newTestProcess is a Claude harness process around pt, with no program behind it.
func newTestProcess(t *testing.T, pt pty.Pty) *process {
	t.Helper()
	p := &process{c: &localConduit{pt: pt}, events: make(chan runner.Event, 64), ring: newRing(1 << 16), state: model.StateIdle}
	info, _ := harness.Get(model.HarnessClaude)
	p.w = &watcher{p: p, info: info, scr: screen.New(80, 24, nil), gateAt: map[string]time.Time{}, ready: true}
	t.Cleanup(p.w.scr.Close)
	return p
}

// within fails the test if fn does not return within d, so a deadlock fails instead of hanging.
func within(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %s: deadlocked", what, d)
	}
}

// menuScreen is a permission menu as Claude draws it.
const menuScreen = "Bash command\r\n  rm -rf build\r\nDo you want to proceed?\r\n❯ 1. Yes\r\n  2. No\r\n"

// TestGuardedWriteRefusesTransitionLandingBeforeTheWrite is the reviewer's repro: a fake
// process (here, the detector goroutine standing in for the watcher's own evaluate/menuLocked)
// inserts a pending transition right before the underlying Write. guardedWrite checks under the
// same p.mu the detector takes to flip w.pending, and an admitted key holds the detector off
// until it has landed, so the two cannot interleave: the detector's transition either lands
// wholly before the write's check (write refused, nothing reaches the pty) or after the key has
// landed. This test forces the "before" ordering deterministically and checks the write was
// refused with nothing written; a second test below forces "after" and checks it wasn't.
func TestGuardedWriteRefusesTransitionLandingBeforeTheWrite(t *testing.T) {
	p, bp := newGuardedTestProcess(t)

	// The detector claims p.mu first and sets pending, simulating menuLocked's own transition,
	// then releases the pty write it's holding open underneath (there isn't one yet - this
	// ordering is enforced by the caller below never starting until this goroutine is done).
	done := make(chan struct{})
	go func() {
		p.mu.Lock()
		p.w.pending = &pendingMenu{id: "menu-1", sig: "sig"}
		p.mu.Unlock()
		close(done)
	}()
	<-done // the transition has fully landed before guardedWrite is ever called

	close(bp.release) // nothing should reach the pty, but unblock it just in case
	err := p.w.guardedWrite([]byte{0x1b}, false)
	if err != runner.ErrApprovalPending {
		t.Fatalf("err = %v, want ErrApprovalPending", err)
	}
	select {
	case got := <-bp.writes:
		t.Fatalf("a refused write still reached the pty: %q", got)
	default:
	}
}

// TestNonOwnerKeyInFlightHoldsOffANewMenu forces the opposite ordering: a non-owner key was
// admitted and is still inside pty.Write when a menu is drawn. p.mu stays free meanwhile (the
// detector and the output reader keep running), but the detector does not make the menu an
// approval until the key has landed - as if it read the screen after the key - and the admitted
// key lands.
func TestNonOwnerKeyInFlightHoldsOffANewMenu(t *testing.T) {
	p, bp := newGuardedTestProcess(t)

	writeErr := make(chan error, 1)
	go func() { writeErr <- p.w.guardedWrite([]byte{0x03}, false) }()
	<-bp.entered // admitted, now blocked inside pty.Write

	within(t, time.Second, "output", func() { p.w.output([]byte(menuScreen)) })
	for i := 0; i < menuSettle+1; i++ {
		within(t, time.Second, "evaluate", p.w.evaluate)
	}
	p.mu.Lock()
	pending := p.w.pending
	p.mu.Unlock()
	if pending != nil {
		t.Fatal("a menu became an approval while an admitted non-owner key was still on its way in")
	}

	close(bp.release) // the key lands
	if err := <-writeErr; err != nil {
		t.Fatalf("an already-admitted write was refused: %v", err)
	}
	if got := <-bp.writes; string(got) != "\x03" {
		t.Fatalf("written %q", got)
	}

	// Now the menu settles into an approval, and it applies to the next write.
	for i := 0; i < menuSettle; i++ {
		p.w.evaluate()
	}
	p.mu.Lock()
	pending = p.w.pending
	p.mu.Unlock()
	if pending == nil {
		t.Fatal("the menu did not become an approval once the key had landed")
	}
	if err := p.w.guardedWrite([]byte{0x1b}, false); err != runner.ErrApprovalPending {
		t.Fatalf("err = %v, want ErrApprovalPending once pending is set", err)
	}
}

// busyPty is a program busy printing: it reads its input (Write returns) only once the output
// it queued was read off the pty, like a TUI blocked writing to a terminal nobody drains.
type busyPty struct {
	*blockingPty
	out     chan []byte   // queued output; closed: nothing more
	drained chan struct{} // closed once out is empty
	once    sync.Once
}

func newBusyPty(chunks ...string) *busyPty {
	c := &busyPty{blockingPty: newBlockingPty(), out: make(chan []byte, len(chunks)), drained: make(chan struct{})}
	for _, s := range chunks {
		c.out <- []byte(s)
	}
	close(c.out)
	return c
}

func (c *busyPty) Read(b []byte) (int, error) {
	chunk, ok := <-c.out
	if !ok {
		c.once.Do(func() { close(c.drained) })
		return 0, io.EOF
	}
	return copy(b, chunk), nil
}

func (c *busyPty) Write(b []byte) (int, error) {
	c.entered <- struct{}{}
	<-c.drained
	c.writes <- append([]byte(nil), b...)
	return len(b), nil
}

// TestWriteIntoABusyProgramDoesNotDeadlock is the deadlock repro: the program takes its input
// only once its output was read, and reading the output takes p.mu. A write that held p.mu while
// it waited on the pty froze the agent's terminal for good - the owner's paste as much as a
// head's key.
func TestWriteIntoABusyProgramDoesNotDeadlock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		b     []byte
		owner bool
	}{
		{"owner paste", bytes.Repeat([]byte("pasted text "), 8<<10), true},
		{"non-owner key", []byte{0x1b}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pt := newBusyPty("building...\r\n", "still building...\r\n", "done\r\n")
			p := newTestProcess(t, pt)

			wrote := make(chan error, 1)
			go func() { wrote <- p.Write(tc.b, tc.owner) }()
			select {
			case <-pt.entered: // the write waits on the program, the program on its output
			case err := <-wrote:
				t.Fatalf("the write never reached the pty: %v", err)
			case <-time.After(time.Second):
				t.Fatal("the write never reached the pty")
			}

			readDone := make(chan struct{})
			go p.read(readDone)
			select {
			case err := <-wrote:
				if err != nil {
					t.Fatalf("write: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("deadlock: the write held p.mu while the pty was full, so the output was never read")
			}
			if got := <-pt.writes; !bytes.Equal(got, tc.b) {
				t.Fatalf("wrote %d bytes, want %d", len(got), len(tc.b))
			}
			within(t, time.Second, "the output reader", func() { <-readDone })
		})
	}
}

// stuckPty is a program that never reads its input: every Write blocks until release.
type stuckPty struct{ *blockingPty }

func (s stuckPty) Write(b []byte) (int, error) {
	<-s.release
	return len(b), nil
}

// TestQueryAnswersDoNotStallTheOutput: the screen answers a program's terminal queries (here
// the cursor position) by writing to the pty. A program that does not read its input must not
// hold up the output reader through those answers (the screen's lock, and p.mu behind it).
func TestQueryAnswersDoNotStallTheOutput(t *testing.T) {
	pt := stuckPty{newBlockingPty()}
	p := &process{c: &localConduit{pt: pt}, events: make(chan runner.Event, 64), done: make(chan struct{})}
	info, _ := harness.Get(model.HarnessClaude)
	p.w = newWatcher(p, info, runner.Spec{}, harness.Command{}, 80, 24)
	t.Cleanup(func() {
		close(pt.release)
		close(p.done)
		p.w.scr.Close()
	})

	within(t, 3*time.Second, "output with queries", func() {
		for i := 0; i < 200; i++ {
			p.w.output([]byte("\x1b[6n"))
		}
		p.w.evaluate() // reads the screen under p.mu
	})
}

// TestStopDoesNotWaitOnABackedUpShell: Stop types "exit" into a shell but must not wait for
// that write, or a shell not reading its input (a paste still going in) is never signalled.
func TestStopDoesNotWaitOnABackedUpShell(t *testing.T) {
	pt := stuckPty{newBlockingPty()}
	p := &process{c: &localConduit{pt: pt, cmd: &pty.Cmd{}}, shell: true, done: make(chan struct{})}
	p.wmu.Lock() // the owner's paste, stuck inside pty.Write
	t.Cleanup(func() {
		close(pt.release)
		p.wmu.Unlock()
	})
	close(p.done) // the shell has gone, as it would once signalled
	within(t, time.Second, "Stop", func() { _ = p.Stop(context.Background()) })
}

// TestGuardedWriteNeverRefusesTheOwner: the owner types through a pending approval regardless
// (e.g. to answer the menu itself) - same as internal/runner/term/harness_test.go's "a menu
// answered by a human in the terminal" case, but exercised directly against guardedWrite.
func TestGuardedWriteNeverRefusesTheOwner(t *testing.T) {
	p, bp := newGuardedTestProcess(t)
	p.w.pending = &pendingMenu{id: "menu-3", sig: "sig"}
	close(bp.release)
	if err := p.w.guardedWrite([]byte("owner"), true); err != nil {
		t.Fatalf("owner refused: %v", err)
	}
	if got := <-bp.writes; string(got) != "owner" {
		t.Fatalf("written %q", got)
	}
}

// TestNonOwnerWritesAreInterruptKeysOnly: a head or lead may write only Esc or Ctrl-C into a
// harness's TUI; Enter, digits, arrows, shift+tab and text answer menus or change permissions.
// The owner writes anything.
func TestNonOwnerWritesAreInterruptKeysOnly(t *testing.T) {
	p, bp := newGuardedTestProcess(t)
	close(bp.release)
	for _, b := range []string{"\r", "y", "1", "\x1b[A", "\x1b[Z", "\t", "/permissions\r", "\x1b\x1b", "\x03\x03"} {
		if err := p.w.guardedWrite([]byte(b), false); err != runner.ErrOwnerOnly {
			t.Fatalf("non-owner %q: err = %v, want ErrOwnerOnly", b, err)
		}
	}
	for _, b := range []string{"\x1b", "\x03"} {
		if err := p.w.guardedWrite([]byte(b), false); err != nil {
			t.Fatalf("non-owner %q refused: %v", b, err)
		}
		if got := <-bp.writes; string(got) != b {
			t.Fatalf("written %q", got)
		}
		<-bp.entered // every Write signals entered (buffer 1): drain it or the next Write blocks
	}
	if err := p.w.guardedWrite([]byte("/permissions\r"), true); err != nil {
		t.Fatalf("owner refused: %v", err)
	}
	if got := <-bp.writes; string(got) != "/permissions\r" {
		t.Fatalf("written %q", got)
	}
}

// TestNonOwnerKeyRefusedWhileAMenuIsComing: before a menu is pending - seen once by the
// detector (a candidate), or drawn and not yet read at all - a non-owner key is refused too.
func TestNonOwnerKeyRefusedWhileAMenuIsComing(t *testing.T) {
	p, bp := newGuardedTestProcess(t)
	close(bp.release)

	p.mu.Lock()
	p.w.candidate, p.w.candN = "sig", 1
	p.mu.Unlock()
	if err := p.w.guardedWrite([]byte{0x1b}, false); err != runner.ErrApprovalPending {
		t.Fatalf("with a menu candidate: err = %v, want ErrApprovalPending", err)
	}
	p.mu.Lock()
	p.w.candidate, p.w.candN = "", 0
	p.mu.Unlock()

	// A permission menu on the screen that the detector has not read yet.
	p.w.scr.Write([]byte(menuScreen))
	if !p.w.classify().State.Menu() {
		t.Fatal("test screen is not a menu")
	}
	if err := p.w.guardedWrite([]byte{0x1b}, false); err != runner.ErrApprovalPending {
		t.Fatalf("with a menu on the screen: err = %v, want ErrApprovalPending", err)
	}
	select {
	case got := <-bp.writes:
		t.Fatalf("a refused key reached the pty: %q", got)
	default:
	}
	// The owner answers it.
	if err := p.w.guardedWrite([]byte("\r"), true); err != nil {
		t.Fatalf("owner refused: %v", err)
	}
}

// TestShellRefusesNonOwnerInput: a shell has no watcher; whatever reaches it runs.
func TestShellRefusesNonOwnerInput(t *testing.T) {
	bp := newBlockingPty()
	close(bp.release)
	p := &process{c: &localConduit{pt: bp}, shell: true}
	for _, b := range []string{"echo pwned\r", "\x03", "\x1b"} {
		if err := p.Write([]byte(b), false); err != runner.ErrOwnerOnly {
			t.Fatalf("non-owner %q into a shell: err = %v, want ErrOwnerOnly", b, err)
		}
	}
	if err := p.Write([]byte("ls\r"), true); err != nil {
		t.Fatalf("owner refused: %v", err)
	}
	if got := <-bp.writes; string(got) != "ls\r" {
		t.Fatalf("written %q", got)
	}
}

// TestSendQueuesUnlessIdleWithoutMenu: a message (mail, an agent's typed text) is typed at once
// only into an idle input box; with a menu on the way or a turn running it waits for evaluate.
func TestSendQueuesUnlessIdleWithoutMenu(t *testing.T) {
	p, bp := newGuardedTestProcess(t)
	close(bp.release)
	queued := func() int {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.w.queued)
	}

	p.mu.Lock()
	p.w.candN = 1
	p.mu.Unlock()
	_ = p.w.send("one")
	p.mu.Lock()
	p.w.candN = 0
	p.state = model.StateWorking
	p.mu.Unlock()
	_ = p.w.send("two")
	p.mu.Lock()
	p.state = model.StateIdle
	p.mu.Unlock()
	p.w.scr.Write([]byte("Do you want to proceed?\r\n❯ 1. Yes\r\n  2. No\r\n"))
	_ = p.w.send("three")
	if n := queued(); n != 3 {
		t.Fatalf("queued %d, want 3", n)
	}
	select {
	case got := <-bp.writes:
		t.Fatalf("a message was typed into a menu or a running turn: %q", got)
	default:
	}
}

func TestPlainTextDropsControlBytes(t *testing.T) {
	in := "a\x1b[201~b\r\nc\rd\te\x00\x07\x7f\u009bf\xff"
	if got := runner.PlainText(in); got != "a[201~b\nc\nd\tef" {
		t.Fatalf("PlainText = %q", got)
	}
}
