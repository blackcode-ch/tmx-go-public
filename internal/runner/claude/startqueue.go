package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// The start queue. A claude reads and rewrites its .claude.json while it starts; several
// starting at once corrupted ~/.claude.json on 7 Oct (the Python tmx's answer: one start queue).
// So every claude of one CLAUDE_CONFIG_DIR (one daemon: agents, restarts, resumes, summaries,
// Accounts) starts through one queue: one start at a time, the next only once the previous is
// past its startup write window.
//
// The "done starting" signal: StartSettle after the process started (claude's startup writes
// happen in its first seconds; there is no reliable earlier "ready" on its terminal), or at once
// when the process has already exited (Release). A ticket nobody settles or releases (a start
// that hangs) is released after StartMaxHold, so the queue never blocks forever.
var (
	StartSettle  = 3 * time.Second
	StartMaxHold = 30 * time.Second
	// ConfigBackups is how many backups of .claude.json are kept (BackupDir).
	ConfigBackups = 5
)

// StartQueue serializes the claude starts of one CLAUDE_CONFIG_DIR.
type StartQueue struct {
	dir     string
	slot    chan struct{}
	Settle  time.Duration // set before first use (tests)
	MaxHold time.Duration
	Backups int

	mu     sync.Mutex
	holder string // the label of the start holding the turn ("" = none)
}

// Holder labels: who holds the turn, so a waiting start can say what it waits for.
const (
	HolderSignIn = "claude-sign-in" // Accounts' `claude auth login`
	HolderAgent  = "agent"
	HolderOther  = "other"
)

// Wait reasons an OnWait callback gets (WaitReason).
const (
	WaitSignIn = "waiting: Claude sign-in in progress"
	WaitOther  = "waiting for another claude to finish starting"
)

// WaitReason says what a start waiting behind holder waits for.
func WaitReason(holder string) string {
	if holder == HolderSignIn {
		return WaitSignIn
	}
	return WaitOther
}

// Holder is the label of the start that holds the turn ("" = free).
func (q *StartQueue) Holder() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.holder
}

var (
	queuesMu sync.Mutex
	queues   = map[string]*StartQueue{}
)

// QueueFor is dir's start queue (one per folder, so one per daemon).
func QueueFor(dir string) *StartQueue {
	key := realPath(dir)
	if samePath("A", "a") {
		key = strings.ToLower(key)
	}
	queuesMu.Lock()
	defer queuesMu.Unlock()
	q := queues[key]
	if q == nil {
		q = &StartQueue{dir: dir, slot: make(chan struct{}, 1), Settle: StartSettle, MaxHold: StartMaxHold, Backups: ConfigBackups}
		queues[key] = q
	}
	return q
}

// StartTicket is one claude start's turn in the queue.
type StartTicket struct {
	once sync.Once
	q    *StartQueue
	done chan struct{}
}

// BeginStart waits for env's CLAUDE_CONFIG_DIR's turn (or ctx), then backs up its .claude.json.
// The caller starts claude, then calls Settled (started) or Release (failed or exited). label
// names the caller (Holder*); onWait, if set, gets WaitReason while this start waits behind
// another (again when the holder changes) and "" once it has the turn or gave up. It is
// called without any lock of the queue's, from the caller's goroutine.
func BeginStart(ctx context.Context, env []string, label string, onWait func(reason string)) (*StartTicket, error) {
	dir := ConfigDir(env)
	if err := CheckConfigDir(env); err != nil {
		return nil, err
	}
	return QueueFor(dir).begin(ctx, label, onWait, QueueFor(dir).MaxHold, nil)
}

// BeginStartHeld is BeginStart for a run that may write .claude.json until it ends (Accounts:
// `claude auth login` while a human signs in, status, usage, logout): the turn is held from the
// moment it is taken until the caller's Release, which it calls once its process exited (or did
// not start). It is bounded all the same: after max, stop is called (it must stop the caller's
// process, if any, and return once it exited), then the turn is released and this is logged.
func BeginStartHeld(ctx context.Context, env []string, label string, max time.Duration, stop func()) (*StartTicket, error) {
	if err := CheckConfigDir(env); err != nil {
		return nil, err
	}
	return QueueFor(ConfigDir(env)).BeginHeld(ctx, label, max, stop)
}

// Begin waits for the queue's turn (or ctx). The turn is force-released after MaxHold.
func (q *StartQueue) Begin(ctx context.Context) (*StartTicket, error) {
	return q.begin(ctx, HolderOther, nil, q.MaxHold, nil)
}

// BeginHeld waits for the queue's turn (or ctx), held until Release or max (BeginStartHeld).
func (q *StartQueue) BeginHeld(ctx context.Context, label string, max time.Duration, stop func()) (*StartTicket, error) {
	return q.begin(ctx, label, nil, max, stop)
}

// waitRecheck: how often a waiting start looks whether the holder (its reason) changed.
var waitRecheck = time.Second

func (q *StartQueue) begin(ctx context.Context, label string, onWait func(string), max time.Duration, stop func()) (*StartTicket, error) {
	said := ""
	tell := func(reason string) {
		if onWait != nil && reason != said {
			said = reason
			onWait(reason)
		}
	}
	select {
	case q.slot <- struct{}{}:
	default:
		tick := time.NewTicker(waitRecheck)
		defer tick.Stop()
		for got := false; !got; {
			tell(WaitReason(q.Holder()))
			select {
			case q.slot <- struct{}{}:
				got = true
			case <-tick.C:
			case <-ctx.Done():
				tell("")
				return nil, fmt.Errorf("waiting to start claude: %w", ctx.Err())
			}
		}
	}
	q.mu.Lock()
	q.holder = label
	q.mu.Unlock()
	tell("")
	t := &StartTicket{q: q, done: make(chan struct{})}
	go func() { // no turn is held forever: after max, stop the holder's process and move on
		timer := time.NewTimer(max)
		defer timer.Stop()
		select {
		case <-t.done:
		case <-timer.C:
			slog.Default().Warn("claude start queue: a start held its turn too long; stopping it and moving on",
				"dir", q.dir, "max", max)
			if stop != nil {
				stop()
			}
			t.Release()
		}
	}()
	if err := BackupConfig(q.dir, q.Backups); err != nil {
		slog.Default().Warn("claude start queue: .claude.json backup", "dir", q.dir, "err", err)
	}
	return t, nil
}

// Release gives the turn to the next start at once (idempotent).
func (t *StartTicket) Release() {
	t.once.Do(func() {
		close(t.done)
		t.q.mu.Lock()
		t.q.holder = ""
		t.q.mu.Unlock()
		<-t.q.slot
	})
}

// Settled: the process started; the turn passes on after the settle delay.
func (t *StartTicket) Settled() {
	go func() {
		select {
		case <-t.done:
		case <-time.After(t.q.Settle):
		}
		t.Release()
	}()
}

// BackupDir holds the rotating backups of dir's .claude.json: claude.json.1 (newest) … .N.
func BackupDir(dir string) string { return filepath.Join(dir, "tmx-backups") }

// ErrConfigCorrupt: .claude.json is not valid JSON; the good backups were kept.
var ErrConfigCorrupt = errors.New(".claude.json is not valid JSON; backups kept, not overwritten")

// BackupConfig rotates a copy of dir/.claude.json into BackupDir, keeping n. A missing file is
// no error; a corrupt one is never backed up (it would push out the good ones). A copy equal to
// the newest backup is not made again.
func BackupConfig(dir string, n int) error {
	if n <= 0 {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !json.Valid(b) {
		return ErrConfigCorrupt
	}
	bd := BackupDir(dir)
	name := func(i int) string { return filepath.Join(bd, fmt.Sprintf("claude.json.%d", i)) }
	if last, err := os.ReadFile(name(1)); err == nil && bytes.Equal(last, b) {
		return nil
	}
	if err := os.MkdirAll(bd, 0o700); err != nil {
		return err
	}
	_ = os.Remove(name(n))
	for i := n - 1; i >= 1; i-- {
		if err := os.Rename(name(i), name(i+1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return config.WriteFileAtomic(name(1), b, 0o600)
}
