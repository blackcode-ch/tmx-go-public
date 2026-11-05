package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

type client struct {
	t     *testing.T
	base  string
	token string
}

func (c client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			rd = strings.NewReader(s)
		} else {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		if json.Unmarshal(b, &e) != nil || e.Error == "" {
			c.t.Errorf("%s %s: status %d without {error}: %s", method, path, resp.StatusCode, b)
		}
	}
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

func TestHealthIsPublicRestNeedsToken(t *testing.T) {
	d := apitest.New(t)
	anon := client{t: t, base: d.URL}
	var h struct {
		OK      bool
		Machine string
	}
	if anon.do("GET", "/api/health", nil, &h) != 200 || !h.OK || h.Machine != "testbox" {
		t.Fatalf("health: %+v", h)
	}
	if s := anon.do("GET", "/api/snapshot", nil, nil); s != 401 {
		t.Fatalf("anon snapshot: %d", s)
	}
	bad := client{t: t, base: d.URL, token: "nope"}
	if s := bad.do("GET", "/api/snapshot", nil, nil); s != 401 {
		t.Fatalf("bad token: %d", s)
	}
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	var snap model.Snapshot
	if owner.do("GET", "/api/snapshot", nil, &snap) != 200 || snap.Head == nil || snap.Policy.Levels == nil {
		t.Fatalf("snapshot: %+v", snap)
	}
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	if s := agent.do("GET", "/api/snapshot", nil, nil); s != 200 {
		t.Fatalf("agent snapshot: %d", s)
	}
	// Owner-only routes refuse agents.
	for _, r := range []struct{ m, p string }{
		{"GET", "/api/approvals"}, {"GET", "/api/policy"}, {"POST", "/api/agents/head/send"},
		{"DELETE", "/api/agents/head"}, {"POST", "/api/approvals/x"},
	} {
		if s := agent.do(r.m, r.p, `{}`, nil); s != 403 {
			t.Errorf("agent %s %s: %d", r.m, r.p, s)
		}
	}
	if s := owner.do("GET", "/api/nothing", nil, nil); s != 404 {
		t.Fatalf("unknown route: %d", s)
	}
}

func TestCORS(t *testing.T) {
	d := apitest.New(t)
	try := func(origin, method string) *http.Response {
		req, _ := http.NewRequest(method, d.URL+"/api/health", nil)
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	for _, o := range []string{"wails://wails", "http://wails.localhost", strings.Replace(d.URL, "127.0.0.1", "localhost", 1), d.URL} {
		resp := try(o, "OPTIONS")
		if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != o {
			t.Errorf("origin %s: %d %q", o, resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
		}
	}
	if resp := try("https://evil.example", "GET"); resp.StatusCode != 403 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("foreign origin: %d", resp.StatusCode)
	}
	// DNS rebinding: a foreign Host is refused.
	req, _ := http.NewRequest("GET", d.URL+"/api/health", nil)
	req.Host = "evil.example"
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("foreign host: %d", resp.StatusCode)
	}
}

func TestProjectHarnessSelectionAPI(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	for _, tc := range []struct {
		name, harness string
		want          model.Harness
	}{
		{"default", "", model.HarnessClaude}, {"explicit-claude", "claude", model.HarnessClaude}, {"selected-codex", "codex", model.HarnessCodex},
	} {
		var p model.Project
		if tc.want == model.HarnessCodex && runtime.GOOS != "windows" {
			if s := owner.do("POST", "/api/projects", map[string]string{"name": tc.name, "harness": tc.harness}, nil); s != 403 {
				t.Fatalf("unsupported guard adapter: status %d", s)
			}
			continue
		}
		if s := owner.do("POST", "/api/projects", map[string]string{"name": tc.name, "harness": tc.harness}, &p); s != 201 {
			t.Fatalf("%s: status %d", tc.name, s)
		}
		var a model.Agent
		if s := owner.do("GET", "/api/agents/"+p.LeadID, nil, &a); s != 200 || a.ID != p.LeadID || a.ProjectID != p.ID || a.Harness != tc.want {
			t.Fatalf("%s: lead status %d, %+v, project %+v", tc.name, s, a, p)
		}
	}
	folder := filepath.Join(d.Workspace, "invalid-harness")
	if s := owner.do("POST", "/api/projects", map[string]string{"name": "invalid-harness", "folder": folder, "harness": "bogus"}, nil); s != 400 {
		t.Fatalf("invalid harness: status %d", s)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatalf("invalid harness created folder: %v", err)
	}
	var snap model.Snapshot
	if s := owner.do("GET", "/api/snapshot", nil, &snap); s != 200 {
		t.Fatalf("snapshot status %d", s)
	}
	for _, p := range snap.Projects {
		if p.Slug == "invalid-harness" {
			t.Fatalf("invalid project persisted: %+v", p)
		}
	}
}

func TestProjectAgentAndApprovalRoutes(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	var p model.Project
	if s := owner.do("POST", "/api/projects", map[string]string{"name": "Alpha", "brief": "b"}, &p); s != 201 || p.Slug != "alpha" {
		t.Fatalf("create project: %d %+v", s, p)
	}
	if s := owner.do("POST", "/api/projects", `{"name":`, nil); s != 400 {
		t.Fatalf("bad json: %d", s)
	}
	big := `{"name":"` + strings.Repeat("x", 2<<20) + `"}`
	if s := owner.do("POST", "/api/projects", big, nil); s != 413 {
		t.Fatalf("big body: %d", s)
	}
	lead := client{t: t, base: d.URL, token: d.AgentToken(t, "alpha-lead")}
	var w model.Agent
	if s := lead.do("POST", "/api/agents", map[string]string{"name": "w1"}, &w); s != 201 || w.Model != "claude-sonnet-5" {
		t.Fatalf("lead launch: %d %+v", s, w)
	}
	if s := lead.do("POST", "/api/agents", map[string]string{"name": "w2", "model": "unsupported-model"}, nil); s != 403 {
		t.Fatalf("wrong model: %d", s)
	}
	worker := client{t: t, base: d.URL, token: d.AgentToken(t, "w1")}
	if s := worker.do("POST", "/api/agents", map[string]string{"name": "w3"}, nil); s != 403 {
		t.Fatalf("worker launch: %d", s)
	}
	if s := lead.do("POST", "/api/agents/alpha-lead/stop", nil, nil); s != 403 {
		t.Fatalf("self stop: %d", s)
	}
	var a model.Agent
	if s := lead.do("POST", "/api/agents/w1/stop", nil, &a); s != 200 || a.State != model.StateStopped {
		t.Fatalf("lead stops worker: %d %+v", s, a)
	}
	if s := owner.do("GET", "/api/agents/"+w.ID, nil, &a); s != 200 || a.Name != "w1" {
		t.Fatalf("get by id: %d", s)
	}
	if s := owner.do("GET", "/api/agents/ghost", nil, nil); s != 404 {
		t.Fatalf("missing agent: %d", s)
	}

	// Owner message, timeline.
	var m model.Message
	if s := owner.do("POST", "/api/agents/alpha-lead/send", map[string]string{"text": "hi"}, &m); s != 200 || m.From != "owner" {
		t.Fatalf("send: %d %+v", s, m)
	}
	var tl struct{ Entries []model.TimelineEntry }
	if owner.do("GET", "/api/agents/alpha-lead/timeline?limit=10", nil, &tl); len(tl.Entries) < 2 {
		t.Fatalf("timeline: %+v", tl)
	}
	last := tl.Entries[len(tl.Entries)-1].Seq
	if owner.do("GET", "/api/agents/alpha-lead/timeline?after="+itoa(last), nil, &tl); len(tl.Entries) != 0 {
		t.Fatalf("after: %+v", tl)
	}

	// Permission approval through the API.
	d.Proc(t, "alpha-lead").Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{ID: "rq", Tool: "Bash"}})
	var pend []model.Approval
	deadline := time.Now().Add(2 * time.Second)
	for len(pend) == 0 && time.Now().Before(deadline) {
		owner.do("GET", "/api/approvals", nil, &pend)
		time.Sleep(5 * time.Millisecond)
	}
	if len(pend) != 1 {
		t.Fatal("approval not listed")
	}
	if s := owner.do("POST", "/api/approvals/"+pend[0].ID, map[string]string{"decision": "allow"}, nil); s != 200 {
		t.Fatalf("decide: %d", s)
	}
	if s := owner.do("POST", "/api/approvals/"+pend[0].ID, map[string]string{"decision": "allow"}, nil); s != 409 {
		t.Fatalf("decide twice: %d", s)
	}
	// ask_owner (extension route) by an agent.
	var q model.Approval
	if s := lead.do("POST", "/api/approvals", map[string]any{"question": "ok?", "options": []string{"y", "n"}}, &q); s != 201 || q.Kind != model.ApprovalQuestion {
		t.Fatalf("ask: %d %+v", s, q)
	}
	// Messages: agents send and list their own.
	if s := lead.do("POST", "/api/messages", map[string]string{"to": "owner", "body": "plan ready"}, &m); s != 201 {
		t.Fatalf("message: %d", s)
	}
	var msgs []model.Message
	if lead.do("GET", "/api/messages", nil, &msgs); len(msgs) < 2 {
		t.Fatalf("messages: %+v", msgs)
	}
	// Policy round trip.
	var pol model.Policy
	if owner.do("GET", "/api/policy", nil, &pol); pol.Levels["head"].Model != "claude-opus-5-5" {
		t.Fatalf("policy: %+v", pol)
	}
	if s := owner.do("PUT", "/api/policy", map[string]any{"levels": map[string]any{}}, nil); s != 400 {
		t.Fatalf("bad policy: %d", s)
	}
	if s := owner.do("PUT", "/api/policy", pol, nil); s != 200 {
		t.Fatalf("put policy: %d", s)
	}
	// Delete project.
	if s := owner.do("DELETE", "/api/projects/alpha", nil, nil); s != 200 {
		t.Fatalf("delete: %d", s)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestEventsSocket(t *testing.T) {
	d := apitest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws := strings.Replace(d.URL, "http://", "ws://", 1) + "/api/events?token="
	if _, _, err := websocket.Dial(ctx, ws+"nope", nil); err == nil {
		t.Fatal("bad token accepted")
	}
	conn, _, err := websocket.Dial(ctx, ws+d.OwnerToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	read := func() map[string]any {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ev map[string]any
		json.Unmarshal(b, &ev)
		return ev
	}
	first := read()
	if first["type"] != model.EvSnapshot || first["data"].(map[string]any)["head"] == nil {
		t.Fatalf("first frame: %v", first)
	}
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	owner.do("POST", "/api/agents/head/send", map[string]string{"text": "hello"}, nil)
	seen := map[string]bool{}
	for !(seen[model.EvMessageCreated] && seen[model.EvTimelineAppended]) {
		ev := read()
		seen[ev["type"].(string)] = true
	}
}

func TestTerminalSocket(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	if s := owner.do("POST", "/api/agents", map[string]string{"name": "sh", "harness": "shell"}, nil); s != 201 {
		t.Fatalf("launch owner shell: %d", s)
	}
	p := d.Proc(t, "sh")
	p.SetReplay([]byte("$ "))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws := strings.Replace(d.URL, "http://", "ws://", 1) + "/api/agents/sh/terminal?cols=90&rows=20&token="
	conn, _, err := websocket.Dial(ctx, ws+d.OwnerToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	typ, b, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || string(b) != "$ " {
		t.Fatalf("replay: %v %q", err, b)
	}
	if c, r := p.Size(); c != 90 || r != 20 {
		t.Fatalf("initial size %dx%d", c, r)
	}
	p.Emit(runner.Event{Kind: runner.EvOutput, Output: []byte("out")})
	if _, b, _ = conn.Read(ctx); string(b) != "out" {
		t.Fatalf("output %q", b)
	}
	conn.Write(ctx, websocket.MessageText, []byte(`{"type":"input","data":"ls\r"}`))
	deadline := time.Now().Add(2 * time.Second)
	for string(p.Written()) != "ls\r" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if string(p.Written()) != "ls\r" {
		t.Fatalf("input %q", p.Written())
	}
	// An agent may watch but not type.
	hconn, _, err := websocket.Dial(ctx, ws+d.AgentToken(t, "head"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hconn.CloseNow()
	hconn.Read(ctx) // replay
	hconn.Write(ctx, websocket.MessageText, []byte(`{"type":"input","data":"rm\r"}`))
	time.Sleep(50 * time.Millisecond)
	if string(p.Written()) != "ls\r" {
		t.Fatalf("agent typed: %q", p.Written())
	}
	p.Exit(0, nil)
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("close on exit: %v", err)
	}
}

func TestUIFallback(t *testing.T) {
	d := apitest.New(t)
	resp, err := http.Get(d.URL + "/some/client/route")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(strings.ToLower(string(b)), "<html") {
		t.Fatalf("ui: %d %s", resp.StatusCode, b)
	}
}
