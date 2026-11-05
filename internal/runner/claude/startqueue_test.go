package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// The chat runner starts its claudes through the queue: three launches of one daemon (one
// CLAUDE_CONFIG_DIR) are spaced by the settle delay.
func TestChatStartsQueued(t *testing.T) {
	_, dir := testQueue(t, 150*time.Millisecond, 5*time.Second)
	begin := time.Now()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := New().Start(context.Background(), runner.Spec{Command: os.Args[0], Cwd: t.TempDir(),
				Env: []string{EnvConfigDir + "=" + dir, "TMXG_FAKE_CLAUDE=1"}})
			if runner.Elevated() { // refused before the queue; elevation refusal is tested elsewhere
				if err == nil {
					t.Error("elevated launch not refused")
				}
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			go func() {
				for range p.Events() {
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = p.Stop(ctx)
		}()
	}
	wg.Wait()
	if el := time.Since(begin); !runner.Elevated() && el < 300*time.Millisecond {
		t.Fatalf("3 launches took %v: not one at a time", el)
	}
}

func testQueue(t *testing.T, settle, maxHold time.Duration) (*StartQueue, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "claude-config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	q := QueueFor(dir)
	q.Settle, q.MaxHold = settle, maxHold
	return q, dir
}

// 8 parallel starts run strictly one at a time, each holding its turn until it settled.
func TestStartQueueOneAtATime(t *testing.T) {
	_, dir := testQueue(t, 20*time.Millisecond, 5*time.Second)
	env := []string{EnvConfigDir + "=" + dir}
	var cur, maxSeen, done atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk, err := BeginStart(context.Background(), env, HolderAgent, nil)
			if err != nil {
				t.Error(err)
				return
			}
			n := cur.Add(1)
			for {
				m := maxSeen.Load()
				if n <= m || maxSeen.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond) // "starting"
			cur.Add(-1)                      // the process started; its writes run until it settled
			tk.Settled()
			done.Add(1)
		}()
	}
	wg.Wait()
	if maxSeen.Load() != 1 || done.Load() != 8 {
		t.Fatalf("max concurrent starts %d, done %d", maxSeen.Load(), done.Load())
	}
	// Settled holds the turn: the next start waits for the settle delay.
	start := time.Now()
	tk, err := BeginStart(context.Background(), env, HolderAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	tk.Settled()
	tk2, err := BeginStart(context.Background(), env, HolderAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	tk2.Release()
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("the next start did not wait for the settle delay")
	}
}

// A start that never settles or releases (hung) loses its turn after MaxHold.
func TestStartQueueHungStartTimesOut(t *testing.T) {
	q, _ := testQueue(t, time.Hour, 50*time.Millisecond)
	if _, err := q.Begin(context.Background()); err != nil { // never released
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tk, err := q.Begin(ctx)
	if err != nil {
		t.Fatalf("queue stuck behind a hung start: %v", err)
	}
	tk.Release()
	// Waiting respects ctx.
	if _, err := q.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	short, cancel2 := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel2()
	if _, err := q.Begin(short); err == nil {
		t.Fatal("Begin must give up with its context")
	}
}

func TestBeginStartRefusesShared(t *testing.T) {
	if _, err := BeginStart(context.Background(), nil, HolderAgent, nil); err == nil {
		t.Fatal("no CLAUDE_CONFIG_DIR must be refused")
	}
}

func TestBackupConfigRotates(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".claude.json")
	if err := BackupConfig(dir, 3); err != nil { // missing file: nothing, no error
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		os.WriteFile(cfg, []byte(fmt.Sprintf(`{"n":%d}`, i)), 0o600)
		if err := BackupConfig(dir, 3); err != nil {
			t.Fatal(err)
		}
		if err := BackupConfig(dir, 3); err != nil { // unchanged: not backed up again
			t.Fatal(err)
		}
	}
	read := func(i int) string {
		b, _ := os.ReadFile(filepath.Join(BackupDir(dir), fmt.Sprintf("claude.json.%d", i)))
		return string(b)
	}
	if read(1) != `{"n":5}` || read(2) != `{"n":4}` || read(3) != `{"n":3}` || read(4) != "" {
		t.Fatalf("rotation: %q %q %q %q", read(1), read(2), read(3), read(4))
	}
	// A corrupt file is reported and evicts nothing.
	os.WriteFile(cfg, []byte(`{"n":6,`), 0o600)
	if err := BackupConfig(dir, 3); err != ErrConfigCorrupt {
		t.Fatalf("corrupt: %v", err)
	}
	if read(1) != `{"n":5}` || read(3) != `{"n":3}` {
		t.Fatalf("good backups evicted: %q %q", read(1), read(3))
	}
}

// Begin backs up .claude.json before the start.
func TestBeginBacksUp(t *testing.T) {
	q, dir := testQueue(t, time.Millisecond, time.Second)
	os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{}`), 0o600)
	tk, err := q.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tk.Release()
	if _, err := os.Stat(filepath.Join(BackupDir(dir), "claude.json.1")); err != nil {
		t.Fatal(err)
	}
}

// A held turn (Accounts' runs) is held from the moment it is taken: a start slower than the
// force-release delay keeps it until the caller's Release (its process exited).
func TestHeldTurnSurvivesASlowStart(t *testing.T) {
	q, _ := testQueue(t, 10*time.Millisecond, 50*time.Millisecond)
	tk, err := q.BeginHeld(context.Background(), HolderOther, 5*time.Second, func() { t.Error("backstop ran") })
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // the "process" takes longer to start than MaxHold
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := q.Begin(short); err == nil {
		t.Fatal("a held turn was released before its process exited")
	}
	tk.Release() // exited
	next, err := q.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next.Release()
}

// A held turn past its max fails closed: the holder's process is stopped first, then the
// queue moves on.
func TestHeldTurnBackstopStopsThenReleases(t *testing.T) {
	q, _ := testQueue(t, 10*time.Millisecond, time.Hour)
	var stopped atomic.Bool
	if _, err := q.BeginHeld(context.Background(), HolderOther, 50*time.Millisecond, func() {
		time.Sleep(20 * time.Millisecond) // waits for the process to exit
		stopped.Store(true)
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tk, err := q.Begin(ctx)
	if err != nil {
		t.Fatalf("queue stuck behind a held turn past its max: %v", err)
	}
	if !stopped.Load() {
		t.Fatal("released before the holder's process was stopped")
	}
	tk.Release()
}
