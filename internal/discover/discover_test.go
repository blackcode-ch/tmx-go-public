package discover

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func writeTranscript(t *testing.T, home, cwd, id string, started time.Time, text string) {
	t.Helper()
	dir := harness.ClaudeProjectDir(home, cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":%q}}`+"\n", started.UTC().Format(time.RFC3339Nano), text)
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeRollout(t *testing.T, codexHome, cwd, id string, started time.Time) {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", started.Format("2006"), started.Format("01"), started.Format("02"))
	_ = os.MkdirAll(dir, 0o755)
	body := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q}}`+"\n"+
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login"}]}}`+"\n",
		id, started.UTC().Format(time.RFC3339Nano), cwd)
	if err := os.WriteFile(filepath.Join(dir, "rollout-x-"+id+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	solo, shared, cx := t.TempDir(), t.TempDir(), t.TempDir()
	now := time.Now().Truncate(time.Second)
	start := now.Add(-10 * time.Minute)
	writeTranscript(t, home, solo, "older-0000-0000-0000", start.Add(-3*time.Hour), "old")
	writeTranscript(t, home, solo, "fresh-1111-1111-1111", start.Add(5*time.Second), "hello there")
	writeTranscript(t, home, shared, "aaaa-aaaa-aaaa-aaaa1", start.Add(-time.Hour), "a")
	writeTranscript(t, home, shared, "bbbb-bbbb-bbbb-bbbb2", start.Add(-2*time.Hour), "b")
	writeRollout(t, filepath.Join(home, ".codex"), cx, "0190-codex-thread-01", start.Add(2*time.Second))

	procs := []Proc{
		{PID: 1, PPID: 0, Started: start.Add(-time.Hour), Args: []string{"/sbin/launchd"}},
		{PID: 10, PPID: 1, Started: start, Args: []string{"/Users/x/.local/bin/claude"}, Cwd: solo},
		{PID: 11, PPID: 1, Started: start, Args: []string{"claude", "--model", "opus"}, Cwd: shared},
		{PID: 12, PPID: 1, Started: start, Args: []string{"claude"}, Cwd: shared},
		{PID: 13, PPID: 1, Started: start, Args: []string{"claude", "--resume", "cccc-cccc-cccc-cccc3"}, Cwd: shared},
		{PID: 20, PPID: 1, Started: start, Args: []string{"node", "/usr/lib/node_modules/@openai/codex/bin/codex.js"}, Cwd: cx},
		{PID: 21, PPID: 20, Started: start, Args: []string{"/usr/lib/node_modules/@openai/codex/vendor/codex"}, Cwd: cx},
		{PID: 30, PPID: 1, Started: start, Args: []string{"claude", "-p", "--output-format", "stream-json"}, Cwd: solo},
		{PID: 40, PPID: 1, Started: start.Add(-time.Hour), Args: []string{"/opt/tmx/tmx", "daemon"}},
		{PID: 41, PPID: 40, Started: start, Args: []string{"claude"}, Cwd: solo},
		{PID: 50, PPID: 1, Started: start.Add(-time.Hour), Args: []string{"/py/python", "-m", "tmx.host", "spec.json"}},
		{PID: 51, PPID: 50, Started: start, Args: []string{"claude"}, Cwd: solo},
		{PID: 60, PPID: 99, Started: start, Args: []string{"claude"}, Cwd: solo}, // below our own daemon
		{PID: 99, PPID: 1, Started: start.Add(-time.Hour), Args: []string{"tmx-test"}},
		{PID: 70, PPID: 1, Started: start, Args: []string{"opencode"}, Cwd: solo},
		{PID: 80, PPID: 1, Started: start, Args: []string{"claude"}}, // folder unreadable
		{PID: 90, PPID: 1, Started: start, Args: []string{"/bin/zsh"}},
		{PID: 91, PPID: 1, Started: start, Args: []string{"claude"}, Cwd: solo}, // managed by this daemon
	}
	s := &Scanner{Home: home, Self: 99, List: func() ([]Proc, error) { return procs, nil },
		Cwds: func([]int) map[int]string { return map[int]string{} }, Now: func() time.Time { return now }}
	got, err := s.Scan([]int{91})
	if err != nil {
		t.Fatal(err)
	}
	by := map[int]Agent{}
	for _, a := range got {
		by[a.PID] = a
	}
	for _, pid := range []int{21, 60, 90, 91} {
		if _, ok := by[pid]; ok {
			t.Errorf("pid %d listed", pid)
		}
	}
	check := func(pid int, adoptable bool, conv, source string) {
		t.Helper()
		a, ok := by[pid]
		if !ok {
			t.Fatalf("pid %d missing from %+v", pid, got)
		}
		if a.Adoptable != adoptable || adoptable && (a.ConversationID != conv || a.ConversationSource != source) {
			t.Errorf("pid %d: adoptable=%v conv=%q source=%q reason=%q", pid, a.Adoptable, a.ConversationID, a.ConversationSource, a.Reason)
		}
	}
	check(10, true, "fresh-1111-1111-1111", "transcript started with this process")
	check(11, true, "", "3 claude sessions share this folder; pick the conversation")
	check(13, true, "cccc-cccc-cccc-cccc3", "command line")
	check(20, true, "0190-codex-thread-01", "transcript started with this process")
	check(30, false, "", "")
	check(41, false, "", "")
	check(51, false, "", "")
	check(70, false, "", "")
	check(80, false, "", "")
	if by[20].Harness != model.HarnessCodex || by[20].Candidates[0].Preview != "fix the login" {
		t.Fatalf("%+v", by[20])
	}
	if len(by[11].Candidates) != 2 || by[41].Reason != "run by a tmx (pid 40)" {
		t.Fatalf("%+v / %q", by[11].Candidates, by[41].Reason)
	}
	if !s.Owns(by[11], "bbbb-bbbb-bbbb-bbbb2") || s.Owns(by[11], "zzzz-not-there-0000") {
		t.Fatal("Owns")
	}
	if _, err := s.Find(4242, nil); err != ErrNotFound {
		t.Fatal(err)
	}
}

func TestParsers(t *testing.T) {
	for args, want := range map[string]bool{"claude": true, "claude --model x": true, "claude -p hi": false,
		"codex exec run": false, "codex app-server": false, "codex resume abc": true, "claude please exec this": true} {
		if got := interactive(strings.Fields(args)); got != want {
			t.Errorf("%q: %v", args, got)
		}
	}
}

// TestLiveScan prints what this machine shows (TMXG_LIVE=1; read-only).
func TestLiveScan(t *testing.T) {
	if os.Getenv("TMXG_LIVE") != "1" {
		t.Skip("TMXG_LIVE=1 to read this machine's processes")
	}
	all, err := New().Scan(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range all {
		t.Logf("%d %s adoptable=%v reason=%q cwd=%s conv=%s (%s) cands=%d", a.PID, a.Harness, a.Adoptable, a.Reason, a.Cwd, a.ConversationID, a.ConversationSource, len(a.Candidates))
	}
}
