package claude

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// TestLive drives the real claude (costs a few cents): TMXG_LIVE=1 go test -run TestLive ./internal/runner/claude
//
// --session-id finding (Claude Code 2.1.306, 21 Oct 2026): honored in -p stream-json mode; the
// system/init session_id equals the UUID passed. So a caller may pre-assign the id of a new
// conversation (ExtraArgs) instead of waiting for the first turn's system/init.
func TestLive(t *testing.T) {
	if os.Getenv("TMXG_LIVE") != "1" {
		t.Skip("set TMXG_LIVE=1 to run against the real claude")
	}
	dir := t.TempDir()
	sid := newUUID()
	p, err := New().Start(context.Background(), runner.Spec{
		Name: "live", Harness: model.HarnessClaude, Model: "claude-haiku-4-5-20251001", Cwd: dir,
		ExtraArgs: []string{"--session-id", sid},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		go func() {
			for range p.Events() {
			}
		}()
		_ = p.Stop(ctx)
	}()

	sent := false
	var gotSession string
	var approvals int
	var result *model.TimelineEntry
	timeout := time.After(3 * time.Minute)
	for result == nil {
		select {
		case ev, ok := <-p.Events():
			if !ok {
				t.Fatal("claude exited early")
			}
			t.Logf("%s", describe(ev))
			switch ev.Kind {
			case runner.EvState:
				if ev.State == model.StateIdle && !sent {
					sent = true
					if err := p.Send("Use the Bash tool to run exactly this command and nothing else: echo hello > a.txt"); err != nil {
						t.Fatal(err)
					}
				}
			case runner.EvSession:
				gotSession = ev.SessionID
			case runner.EvApproval:
				approvals++
				if err := p.Answer(ev.Approval.ID, runner.Decision{Allow: true}); err != nil {
					t.Fatal(err)
				}
			case runner.EvExit:
				t.Fatalf("exit %d: %v", ev.ExitCode, ev.Err)
			case runner.EvTimeline:
				if ev.Entry.Kind == model.EntryResult {
					result = ev.Entry
				}
			}
		case <-timeout:
			t.Fatal("timed out")
		}
	}
	if gotSession == "" {
		t.Error("no EvSession")
	}
	if gotSession != sid {
		t.Errorf("--session-id %s not honored: system/init reported %s", sid, gotSession)
	}
	if approvals == 0 {
		t.Error("no approval was requested")
	}
	if result.IsError {
		t.Errorf("result is an error: %+v", result)
	}
	b, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(b) != "hello\n" {
		t.Errorf("a.txt = %q, %v", b, err)
	}
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
