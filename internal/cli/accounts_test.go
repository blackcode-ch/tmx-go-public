package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/accounts"
	"github.com/blackcode-ch/tmx-go/internal/accounts/accountstest"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestAccountsCommand(t *testing.T) {
	d := cliDaemon(t)
	out, errs, code := tmx(t, "accounts")
	if code != 0 || !strings.Contains(out, "Claude") || !strings.Contains(out, "not checked yet") {
		t.Fatalf("before a check: %d %s %s", code, out, errs)
	}
	f := accountstest.Install(t, d.AccountsBin, model.HarnessClaude, model.HarnessCodex)
	f.Set("claude-usage.delay", "0.3") // refresh waits for the answer
	out, errs, code = tmx(t, "accounts", "refresh")
	if code != 0 {
		t.Fatalf("refresh: %d %s %s", code, out, errs)
	}
	t.Logf("tmx accounts refresh:\n%s", out)
	for _, want := range []string{
		"signed in: me@example.com · Max · subscription · Example Org",
		"Current session", "8% used", "resets Oct 29 at 9:10pm",
		"Current week (all models)", "54% used",
		"signed in: me@example.com · Plus · subscription", "5-hour limit", "12.5% used", "Weekly limit",
		"Antigravity", "not installed (Install the Antigravity CLI",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "$") || strings.Contains(out, "checking now") {
		t.Errorf("refresh output:\n%s", out)
	}
	out, _, code = tmx(t, "accounts", "--json")
	var p accounts.Payload
	if code != 0 || json.Unmarshal([]byte(out), &p) != nil || len(p.Accounts) != 4 || !p.Accounts[0].Connected {
		t.Fatalf("json: %d %s", code, out)
	}
	if _, _, code := tmx(t, "accounts", "refresh", "nope"); code != 2 {
		t.Fatalf("unknown harness: %d", code)
	}
	if _, _, code := tmx(t, "accounts", "nope"); code != 2 {
		t.Fatalf("unknown subcommand: %d", code)
	}
	f.Set("claude-status.json", accountstest.ClaudeStatusOut)
	if out, _, _ = tmx(t, "accounts", "refresh", "claude"); !strings.Contains(out, "not signed in") {
		t.Fatalf("signed out:\n%s", out)
	}
}

func TestWriteAccounts(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	p := accounts.Payload{Accounts: []accounts.Account{{
		Label: "Codex", Installed: true, Connected: true, CheckedAt: now.Add(-3 * time.Minute).UnixMilli(),
		Account:    accounts.Identity{Method: accounts.MethodAPIKey},
		LimitsNote: "Signed in with an API key: no plan limits",
		Limits:     []accounts.Limit{{Label: "5-hour limit", UsedPercent: 33.3, ResetsAt: now.Add(time.Hour).UnixMilli()}},
		Login:      accounts.Login{State: accounts.LoginFailed, Message: "The sign-in ended with exit code 1"},
		Error:      "Couldn't run codex: boom",
	}}}
	var b strings.Builder
	if err := writeAccounts(&b, p, now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Codex", "signed in: API key", "33.3% used", "resets at 13:00", "Signed in with an API key: no plan limits",
		"error: Couldn't run codex: boom", "sign-in failed: The sign-in ended with exit code 1", "checked 3 min ago"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("no %q in:\n%s", want, b.String())
		}
	}
}
