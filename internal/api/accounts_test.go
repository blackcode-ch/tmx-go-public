package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/blackcode-ch/tmx-go/internal/accounts"
	"github.com/blackcode-ch/tmx-go/internal/accounts/accountstest"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func accountOf(p accounts.Payload, h string) accounts.Account {
	for _, a := range p.Accounts {
		if a.Harness == h {
			return a
		}
	}
	return accounts.Account{}
}

// waitAccounts polls GET /api/accounts until fn holds.
func waitAccounts(t *testing.T, owner client, what string, fn func(accounts.Payload) bool) accounts.Payload {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var p accounts.Payload
		owner.do("GET", "/api/accounts", nil, &p)
		if fn(p) {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %+v", what, p)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAccountsOwnerOnly(t *testing.T) {
	d := apitest.New(t)
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	tok, err := config.CompanionToken(config.Paths{Home: d.Home}, true)
	if err != nil {
		t.Fatal(err)
	}
	companion := client{t: t, base: d.URL, token: tok}
	routes := []struct{ method, path string }{
		{"GET", "/api/accounts"}, {"POST", "/api/accounts/refresh"}, {"POST", "/api/accounts/claude/login"},
		{"DELETE", "/api/accounts/claude/login"}, {"GET", "/api/accounts/claude/login/terminal"}, {"POST", "/api/accounts/claude/logout"},
	}
	for _, r := range routes {
		if s := agent.do(r.method, r.path, nil, nil); s != http.StatusForbidden {
			t.Errorf("agent %s %s: %d", r.method, r.path, s)
		}
		if s := companion.do(r.method, r.path, nil, nil); s != http.StatusForbidden {
			t.Errorf("companion %s %s: %d", r.method, r.path, s)
		}
		if s := (client{t: t, base: d.URL}).do(r.method, r.path, nil, nil); s != http.StatusUnauthorized {
			t.Errorf("no token %s %s: %d", r.method, r.path, s)
		}
	}
	// An agent's events never carry the owner's accounts.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws := strings.Replace(d.URL, "http://", "ws://", 1) + "/api/events?token="
	conn, _, err := websocket.Dial(ctx, ws+d.AgentToken(t, "head"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.Read(ctx) // snapshot
	accountstest.Install(t, d.AccountsBin, model.HarnessClaude)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	owner.do("POST", "/api/accounts/refresh", map[string]string{"harness": "claude"}, nil)
	waitAccounts(t, owner, "refreshed", func(p accounts.Payload) bool { return accountOf(p, "claude").Connected })
	owner.do("POST", "/api/agents/head/send", map[string]string{"text": "hello"}, nil) // something the agent does see
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), accounts.EvUpdated) || strings.Contains(string(b), "me@example.com") {
			t.Fatalf("an agent got %s", b)
		}
		if strings.Contains(string(b), model.EvMessageCreated) {
			break
		}
	}
}

func TestAccountsAPI(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}

	// The exact shape, before anything was checked.
	var raw map[string]any
	if s := owner.do("GET", "/api/accounts", nil, &raw); s != 200 {
		t.Fatalf("GET: %d", s)
	}
	list := raw["accounts"].([]any)
	if len(list) != 4 || raw["updated_at"] == nil {
		t.Fatalf("payload %v", raw)
	}
	first := list[0].(map[string]any)
	for _, k := range []string{"harness", "label", "installed", "connected", "account", "limits", "limits_note", "checked_at",
		"refreshing", "error", "login", "install", "name", "launch_default"} {
		if _, ok := first[k]; !ok {
			t.Errorf("no %q in %v", k, first)
		}
	}
	if len(first) != 14 || first["harness"] != "claude" || first["label"] != "Claude" || list[1].(map[string]any)["harness"] != "codex" ||
		list[2].(map[string]any)["harness"] != "antigravity" || list[3].(map[string]any)["harness"] != "gemini" {
		t.Fatalf("accounts %v", list)
	}
	if lim, ok := first["limits"].([]any); !ok || len(lim) != 0 {
		t.Fatalf("limits %v", first["limits"])
	}
	for _, k := range []string{"email", "plan", "method", "org"} {
		if _, ok := first["account"].(map[string]any)[k]; !ok {
			t.Errorf("no account.%s", k)
		}
	}
	if l := first["login"].(map[string]any); l["state"] != "idle" || len(l) != 2 {
		t.Fatalf("login %v", l)
	}

	// Not installed: a sign-in is a conflict.
	if s := owner.do("POST", "/api/accounts/claude/login", nil, nil); s != http.StatusConflict {
		t.Fatalf("login, not installed: %d", s)
	}
	if s := owner.do("POST", "/api/accounts/nope/login", nil, nil); s != http.StatusNotFound {
		t.Fatalf("login, unknown: %d", s)
	}
	if s := owner.do("POST", "/api/accounts/refresh", map[string]string{"harness": "nope"}, nil); s != http.StatusNotFound {
		t.Fatalf("refresh, unknown: %d", s)
	}
	if s := owner.do("POST", "/api/accounts/refresh", "{nope", nil); s != http.StatusBadRequest {
		t.Fatalf("refresh, bad body: %d", s)
	}

	// Installed and signed in.
	accountstest.Install(t, d.AccountsBin, model.HarnessClaude, model.HarnessCodex)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events, _, err := websocket.Dial(ctx, strings.Replace(d.URL, "http://", "ws://", 1)+"/api/events?token="+d.OwnerToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer events.CloseNow()
	events.Read(ctx) // snapshot
	var p accounts.Payload
	if s := owner.do("POST", "/api/accounts/refresh", "", &p); s != http.StatusAccepted || !accountOf(p, "claude").Refreshing || !accountOf(p, "codex").Refreshing {
		t.Fatalf("refresh all: %d %+v", s, p)
	}
	p = waitAccounts(t, owner, "refreshed", func(p accounts.Payload) bool {
		return accountOf(p, "claude").Connected && !accountOf(p, "claude").Refreshing && accountOf(p, "codex").Connected
	})
	c := accountOf(p, "claude")
	if c.Account != (accounts.Identity{Email: "me@example.com", Plan: "Max", Method: "subscription", Org: "Example Org"}) || len(c.Limits) != 3 {
		t.Fatalf("claude %+v", c)
	}
	// accounts.updated with the full payload.
	for {
		_, b, err := events.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ev struct {
			Type string           `json:"type"`
			Data accounts.Payload `json:"data"`
		}
		json.Unmarshal(b, &ev)
		if ev.Type == accounts.EvUpdated && len(ev.Data.Accounts) == 4 && accountOf(ev.Data, "claude").Connected {
			break
		}
	}

	// Sign-out.
	var out accounts.Payload
	if s := owner.do("POST", "/api/accounts/claude/logout", nil, &out); s != 200 || accountOf(out, "claude").Connected {
		t.Fatalf("logout: %d %+v", s, accountOf(out, "claude"))
	}
	if s := owner.do("DELETE", "/api/accounts/codex/login", nil, &out); s != 200 || len(out.Accounts) != 4 {
		t.Fatalf("cancel without a sign-in: %d", s)
	}
}

func TestAccountLoginTerminal(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	f := accountstest.Install(t, d.AccountsBin, model.HarnessClaude)
	f.Set("claude-status.json", accountstest.ClaudeStatusOut)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws := strings.Replace(d.URL, "http://", "ws://", 1) + "/api/accounts/claude/login/terminal?cols=90&rows=20&token=" + d.OwnerToken
	if _, _, err := websocket.Dial(ctx, ws, nil); err == nil {
		t.Fatal("a terminal without a sign-in")
	}

	var started struct {
		Session string `json:"session"`
	}
	if s := owner.do("POST", "/api/accounts/claude/login", nil, &started); s != 200 || started.Session == "" {
		t.Fatalf("login: %d %+v", s, started)
	}
	var again struct {
		Session string `json:"session"`
	}
	if owner.do("POST", "/api/accounts/claude/login", nil, &again); again.Session != started.Session {
		t.Fatalf("second login %q, want %q", again.Session, started.Session)
	}
	if l := accountOf(waitAccounts(t, owner, "running", func(p accounts.Payload) bool { return accountOf(p, "claude").Login.State == "running" }), "claude").Login; l.Message == "" {
		t.Fatalf("login %+v", l)
	}
	conn, _, err := websocket.Dial(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var seen strings.Builder
	readUntil := func(want string) {
		t.Helper()
		for !strings.Contains(seen.String(), want) {
			typ, b, err := conn.Read(ctx)
			if err != nil {
				t.Fatalf("waiting for %q in %q: %v", want, seen.String(), err)
			}
			if typ != websocket.MessageBinary {
				t.Fatalf("a %v frame", typ)
			}
			seen.Write(b)
		}
	}
	readUntil("Opening your browser")
	conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":100,"rows":30}`))
	conn.Write(ctx, websocket.MessageText, []byte(`{"type":"input","data":"ok\r"}`))
	readUntil("got:ok")
	for {
		_, _, err := conn.Read(ctx)
		if err == nil {
			continue
		}
		if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
			t.Fatalf("close at the end: %v", err)
		}
		break
	}
	p := waitAccounts(t, owner, "signed in", func(p accounts.Payload) bool { return accountOf(p, "claude").Login.State == "succeeded" })
	if a := accountOf(p, "claude"); !a.Connected || a.Login.Message != "Signed in as me@example.com" {
		t.Fatalf("%+v", a)
	}
	// The last sign-in's screen, then a normal close.
	late, _, err := websocket.Dial(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer late.CloseNow()
	if _, b, err := late.Read(ctx); err != nil || !strings.Contains(string(b), "got:ok") {
		t.Fatalf("replay %q %v", b, err)
	}
	if _, _, err := late.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("late close: %v", err)
	}

	// Cancel.
	if s := owner.do("POST", "/api/accounts/claude/login", nil, &started); s != 200 {
		t.Fatal(s)
	}
	var out accounts.Payload
	if s := owner.do("DELETE", "/api/accounts/claude/login", nil, &out); s != 200 {
		t.Fatal(s)
	}
	waitAccounts(t, owner, "cancelled", func(p accounts.Payload) bool { return accountOf(p, "claude").Login.State == "idle" })
}
