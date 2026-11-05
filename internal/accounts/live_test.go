package accounts

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/harness"
)

// TestLiveClaude reads the real Claude Code's account and usage, only reading (no sign-in or
// sign-out, no model turn): TMXG_LIVE=1 go test -run TestLiveClaude -v ./internal/accounts
//
// Checked on 29 Oct 2026 against Claude Code 2.1.311 (a Max subscription): see DESIGN.md "AI
// accounts and usage".
func TestLiveClaude(t *testing.T) {
	if os.Getenv("TMXG_LIVE") != "1" {
		t.Skip("set TMXG_LIVE=1 to read the real claude's account")
	}
	path, err := harness.LookPath(Claude, "")
	if err != nil {
		t.Skip(err)
	}
	s := New(Options{Dir: t.TempDir()})
	defer s.Close()
	start := time.Now()
	r := claudeCheck(s.ctx, &scope{Service: s}, path)
	took := time.Since(start)
	if !r.known {
		t.Fatalf("not read: %s", r.err)
	}
	b, _ := json.MarshalIndent(map[string]any{"connected": r.connected, "account": r.id, "limits": r.limits,
		"limits_note": r.note, "error": r.err, "took": took.Round(100 * time.Millisecond).String()}, "", "  ")
	out := string(b)
	if r.id.Email != "" { // never print the address (the org's name may hold it too)
		out = strings.ReplaceAll(out, r.id.Email, "<email>")
	}
	t.Logf("claude: %s", out)
	if r.connected && r.id.Method == MethodSubscription && len(r.limits) == 0 {
		t.Errorf("a subscription without limits: %s", r.note)
	}
}
