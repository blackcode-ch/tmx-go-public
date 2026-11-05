package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The test binary doubles as a fake `codex app-server` when TMXG_FAKE_CODEX=1, speaking the
// JSON-RPC protocol as captured from codex 0.163 (initialize → thread/start|resume →
// turn/start → turn/started, item/*, server requests for approvals, turn/completed).
func TestMain(m *testing.M) {
	if os.Getenv("TMXG_FAKE_CODEX") == "1" {
		fakeCodex()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeCodex() {
	enc := json.NewEncoder(os.Stdout)
	send := func(v map[string]any) { _ = enc.Encode(v) }
	note := func(method string, params map[string]any) { send(map[string]any{"method": method, "params": params}) }
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(nil, 1<<20)
	next := func() map[string]any {
		if !in.Scan() {
			return nil
		}
		var m map[string]any
		_ = json.Unmarshal(in.Bytes(), &m)
		return m
	}
	thread := ""
	turnN := 0
	// await reads until the response to server request id arrives (other lines are handled
	// as they would be: an interrupt ends the wait).
	await := func(id float64) map[string]any {
		for m := next(); m != nil; m = next() {
			if m["method"] == nil && m["id"] == id {
				r, _ := m["result"].(map[string]any)
				return r
			}
		}
		os.Exit(0)
		return nil
	}
	item := func(kind string, it map[string]any) {
		it["id"] = fmt.Sprintf("item-%d", turnN)
		note("item/"+kind, map[string]any{"threadId": thread, "turnId": fmt.Sprint(turnN), "item": it})
	}
	msg := func(text string) { item("completed", map[string]any{"type": "agentMessage", "text": text}) }
	done := func(status string, errMsg string) {
		t := map[string]any{"id": fmt.Sprint(turnN), "status": status, "durationMs": 42}
		if errMsg != "" {
			t["error"] = map[string]any{"message": errMsg}
		}
		note("turn/completed", map[string]any{"threadId": thread, "turn": t})
	}
	for m := next(); m != nil; m = next() {
		method, _ := m["method"].(string)
		params, _ := m["params"].(map[string]any)
		id := m["id"]
		switch method {
		case "initialize":
			send(map[string]any{"id": id, "result": map[string]any{"userAgent": "fake", "platformOs": "macos"}})
		case "initialized":
		case "thread/start", "thread/resume":
			if method == "thread/resume" {
				thread, _ = params["threadId"].(string)
				if thread == "missing" {
					send(map[string]any{"id": id, "error": map[string]any{"code": -32600, "message": "no rollout found"}})
					continue
				}
			} else {
				thread = "thr-new"
			}
			b, _ := json.Marshal(params)
			os.Setenv("FAKE_THREAD_PARAMS", string(b))
			send(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": thread, "path": "/x"}, "model": "gpt-fake"}})
		case "turn/start":
			turnN++
			input := params["input"].([]any)[0].(map[string]any)
			text := input["text"].(string)
			send(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": fmt.Sprint(turnN), "status": "inProgress"}}})
			note("turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": fmt.Sprint(turnN), "status": "inProgress"}})
			item("started", map[string]any{"type": "userMessage"})
			switch {
			case text == "args":
				b, _ := json.Marshal(map[string]any{"args": os.Args[1:], "token": os.Getenv("TMXG_AGENT_TOKEN"), "thread": os.Getenv("FAKE_THREAD_PARAMS")})
				msg(string(b))
				done("completed", "")
			case strings.HasPrefix(text, "run "):
				cmd := strings.TrimPrefix(text, "run ")
				item("started", map[string]any{"type": "commandExecution", "command": cmd, "cwd": "/w", "status": "inProgress"})
				send(map[string]any{"id": 7, "method": "item/commandExecution/requestApproval", "params": map[string]any{
					"threadId": thread, "turnId": fmt.Sprint(turnN), "itemId": fmt.Sprintf("item-%d", turnN), "command": cmd, "cwd": "/w", "reason": "needs a shell"}})
				r := await(7)
				if r["decision"] == "accept" {
					item("completed", map[string]any{"type": "commandExecution", "command": cmd, "status": "completed", "aggregatedOutput": "ran: " + cmd, "exitCode": 0})
				} else {
					item("completed", map[string]any{"type": "commandExecution", "command": cmd, "status": "declined"})
				}
				msg("done")
				done("completed", "")
			case text == "ask":
				send(map[string]any{"id": 8, "method": "item/tool/requestUserInput", "params": map[string]any{
					"threadId": thread, "turnId": fmt.Sprint(turnN), "itemId": "q", "isBlocking": true,
					"questions": []any{map[string]any{"id": "q1", "header": "Pick", "question": "Which color?", "isOther": false, "isSecret": false,
						"options": []any{map[string]any{"label": "Red", "description": ""}, map[string]any{"label": "Blue", "description": ""}}}}}})
				r := await(8)
				b, _ := json.Marshal(r)
				msg("answers " + string(b))
				done("completed", "")
			case text == "withdraw":
				send(map[string]any{"id": 9, "method": "item/fileChange/requestApproval", "params": map[string]any{"threadId": thread, "itemId": "f", "reason": "edit"}})
				note("serverRequest/resolved", map[string]any{"threadId": thread, "requestId": 9})
				msg("withdrawn")
				done("completed", "")
			case text == "elicit":
				send(map[string]any{"id": "e1", "method": "mcpServer/elicitation/request", "params": map[string]any{"threadId": thread, "serverName": "x", "mode": "form", "message": "?"}})
				r := await2(next, "e1")
				msg("elicitation " + fmt.Sprint(r["action"]))
				done("completed", "")
			case text == "wait":
				for m := next(); m != nil; m = next() {
					if m["method"] == "turn/interrupt" {
						send(map[string]any{"id": m["id"], "result": map[string]any{}})
						done("interrupted", "")
						break
					}
				}
			case text == "fail":
				note("error", map[string]any{"error": map[string]any{"message": "Reconnecting... 1/5"}, "willRetry": true})
				note("error", map[string]any{"error": map[string]any{"message": "401 Unauthorized"}, "willRetry": false})
				done("failed", "401 Unauthorized")
			case text == "crash":
				fmt.Fprintln(os.Stderr, "boom: codex broke")
				os.Exit(3)
			default:
				item("completed", map[string]any{"type": "reasoning", "summary": []any{"thinking about " + text}})
				msg("echo: " + text)
				note("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": fmt.Sprint(turnN),
					"tokenUsage": map[string]any{"total": map[string]any{"totalTokens": 1234}}})
				done("completed", "")
			}
		default:
			if id != nil {
				send(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": "unknown " + method}})
			}
		}
	}
}

func await2(next func() map[string]any, id string) map[string]any {
	for m := next(); m != nil; m = next() {
		if m["method"] == nil && m["id"] == id {
			r, _ := m["result"].(map[string]any)
			return r
		}
	}
	os.Exit(0)
	return nil
}
