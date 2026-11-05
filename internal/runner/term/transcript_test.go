package term

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
)

// entry waits for a timeline entry of kind whose text is text, and returns it with the
// entries seen before it.
func (e *tuiEvents) entry(kind model.EntryKind, text string) (model.TimelineEntry, []model.TimelineEntry) {
	e.t.Helper()
	var seen []model.TimelineEntry
	ev := e.until(string(kind)+" "+text, func(ev runner.Event) bool {
		if ev.Kind != runner.EvTimeline || ev.Entry == nil {
			return false
		}
		if ev.Entry.Kind == kind && ev.Entry.Text == text {
			return true
		}
		seen = append(seen, *ev.Entry)
		return false
	})
	return *ev.Entry, seen
}

func TestClaudeScreenConversationReachesTheTimeline(t *testing.T) {
	_, ev := startFakeTUI(t, nil, "--session-id", "sess-2")
	p := ev.p
	if err := p.Send("first"); err != nil { // typed by tmx (the fleet records it)
		t.Fatal(err)
	}
	_, before := ev.entry(model.EntryAssistant, "got: first")
	for _, e := range before {
		if e.Kind == model.EntryUser {
			t.Fatalf("tmx's own message came back as typed by a person: %+v", e)
		}
	}
	res, _ := ev.entry(model.EntryResult, "")
	if res.Meta["source"] != "screen" {
		t.Fatalf("result %+v", res)
	}
	ev.state(model.StateIdle)

	// Someone types into the screen: the message, the reply and the end of the turn.
	if err := p.Write([]byte("\x1b[200~by hand\x1b[201~\r"), true); err != nil {
		t.Fatal(err)
	}
	u, _ := ev.entry(model.EntryUser, "by hand")
	if u.Meta["source"] != "screen" || u.Meta["from"] != model.Owner {
		t.Fatalf("typed by hand: %+v", u)
	}
	ev.entry(model.EntryAssistant, "got: by hand")
	ev.entry(model.EntryResult, "")
}

func TestResumedScreenSkipsTheHistory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake TUI uses stty")
	}
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	cfg := filepath.Join(home, "claude-config") // the agent's isolated CLAUDE_CONFIG_DIR
	dir := harness.ClaudeProjectDirIn(cfg, cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"type":"user","message":{"role":"user","content":"old question"}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"old answer"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "sess-3.jsonl"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	spec := runner.Spec{Harness: model.HarnessClaude, SessionID: "sess-3", Command: exe, Cwd: cwd, Cols: 100, Rows: 30,
		Env: []string{"TMXG_FAKE_TUI=1", "TMXG_FAKE_FLAVOR=claude", claude.EnvConfigDir + "=" + cfg}}
	p, err := New().Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Stop(ctx)
	})
	ev := watchTUI(t, p)
	if err := p.Send("new"); err != nil {
		t.Fatal(err)
	}
	_, before := ev.entry(model.EntryAssistant, "got: new")
	for _, e := range before {
		if e.Text == "old answer" || e.Text == "old question" {
			t.Fatalf("history came back: %+v", e)
		}
	}
}

// A screen that looks busy while it starts, with nothing typed, ends no turn.
func TestNoResultWithoutInput(t *testing.T) {
	_, ev := startFakeTUI(t, nil, "--session-id", "sess-4")
	ev.state(model.StateIdle)
	p := ev.p.(*process)
	p.mu.Lock()
	p.setStateLocked(model.StateWorking) // as if the screen showed its busy line
	p.mu.Unlock()
	ev.state(model.StateIdle)
	if err := ev.p.Send("hi"); err != nil {
		t.Fatal(err)
	}
	_, before := ev.entry(model.EntryResult, "")
	for _, e := range before {
		if e.Kind == model.EntryResult {
			t.Fatalf("a result before anything went in: %+v", before)
		}
	}
}
