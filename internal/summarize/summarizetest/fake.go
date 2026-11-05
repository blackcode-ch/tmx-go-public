// Package summarizetest installs a fake claude for summaries into a throwaway tmx home, for tests
// of the API, the CLI and MCP, and for trying the UI on a throwaway daemon (config.json
// claude_command = Path(home)). It is a shell script that reads the prompt, appends its arguments
// to claude.log next to it, and answers like `claude -p --output-format json` with a fixed
// summary. Unix only.
package summarizetest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const script = `#!/bin/sh
# A fake claude -p for summaries (internal/summarize/summarizetest).
dir=$(dirname "$0")
printf '%s\n' "$*" >> "$dir/claude.log"
cat > /dev/null
sleep "${TMXG_FAKE_CLAUDE_DELAY:-0}"
cat "$dir/claude-answer.json"
`

// Answer is the summary the fake writes unless Install is given another.
const Answer = `{"summary": "The owner walks through a checkout bug. After a currency switch the cart total keeps the old price until a reload. They ask for a test before the fix.",
"key_points": ["The cart total is stale after a currency switch", "A reload shows the right total", "The rates request itself is correct"],
"decisions": ["Write a test before fixing it"],
"action_items": [{"who": "the lead", "what": "add a test for the stale total"}, {"what": "make the cart follow rate changes"}],
"chapters": [{"t": "0:00", "title": "The bug"}, {"t": "0:01", "title": "The network tab"}, {"t": "0:02", "title": "Next steps"}]}`

// Supported reports whether the fake runs here (it is a shell script).
func Supported() bool { return runtime.GOOS != "windows" }

// Path is where Install puts the fake: <home>/bin/claude.
func Path(home string) string { return filepath.Join(home, "bin", "claude") }

// Install puts the fake claude at Path(home); every summary is answer ("" = Answer). It skips
// the test on Windows.
func Install(t testing.TB, home, answer string) {
	t.Helper()
	if !Supported() {
		t.Skip("the fake claude is a shell script")
	}
	if err := InstallFiles(home, answer); err != nil {
		t.Fatal(err)
	}
}

// InstallFiles is Install outside a test (a throwaway daemon for trying the UI).
func InstallFiles(home, answer string) error {
	if answer == "" {
		answer = Answer
	}
	p := Path(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": answer,
		"num_turns": 2, "total_cost_usd": 0.001})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(p), "claude-answer.json"), append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(script), 0o755)
}

// Log is what the fake was started with, one line of arguments per run.
func Log(home string) string {
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(Path(home)), "claude.log"))
	return string(b)
}
