package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as a fake `claude` speaking the stream-json protocol when
// TMXG_FAKE_CLAUDE=1, so the process tests need neither the real CLI nor a shell script.
func TestMain(m *testing.M) {
	// The test binary is also `tmx host [--launch] DIR` (host_test.go).
	if len(os.Args) > 1 && os.Args[1] == "host" {
		os.Exit(testHostMain(os.Args[2:]))
	}
	if os.Getenv("TMXG_FAKE_CLAUDE") == "1" {
		fakeClaude()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeClaude: initialize → success; a user turn → init, tool_use, can_use_tool, then on the
// answer a tool_result echoing the (possibly updated) command, text and result. Special
// prompts: "args" replies with os.Args, "crash" exits 3 with stderr, "hang" ignores
// SIGTERM and EOF.
func fakeClaude() {
	out := json.NewEncoder(os.Stdout)
	send := func(v any) { _ = out.Encode(v) }
	sid := "fake-session"
	for i, a := range os.Args {
		if a == "--resume" && i+1 < len(os.Args) {
			sid = os.Args[i+1]
		}
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(nil, 1<<20)
	next := func() map[string]any {
		if !in.Scan() {
			return nil
		}
		var m map[string]any
		if json.Unmarshal(in.Bytes(), &m) != nil {
			return map[string]any{}
		}
		return m
	}
	result := func(text string, turns int) {
		send(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": text,
			"session_id": sid, "num_turns": turns, "total_cost_usd": 0.01 * float64(turns), "duration_ms": 12})
	}
	turns := 0
	for msg := next(); msg != nil; msg = next() {
		switch msg["type"] {
		case "control_request":
			req, _ := msg["request"].(map[string]any)
			send(map[string]any{"type": "control_response", "response": map[string]any{
				"subtype": "success", "request_id": msg["request_id"], "response": map[string]any{}}})
			if req["subtype"] == "interrupt" {
				send(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": sid})
			}
		case "user":
			turns++
			text := msg["message"].(map[string]any)["content"].(string)
			send(map[string]any{"type": "system", "subtype": "init", "session_id": sid, "tools": []string{"Bash"}})
			switch text {
			case "args":
				b, _ := json.Marshal(os.Args[1:])
				result(string(b), turns)
				continue
			case "crash":
				fmt.Fprintln(os.Stderr, "boom: something broke")
				os.Exit(3)
			case "hang":
				signal.Ignore(syscall.SIGTERM)
				send(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
					map[string]any{"type": "text", "text": "hanging"}}}})
				for {
					time.Sleep(time.Hour)
				}
			case "wait":
				// stays in the turn until interrupted
				continue
			}
			input := map[string]any{"command": strings.TrimPrefix(text, "run ")}
			send(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
				map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": input}}}})
			send(map[string]any{"type": "control_request", "request_id": "perm-1", "request": map[string]any{
				"subtype": "can_use_tool", "tool_name": "Bash", "input": input, "description": "run it"}})
			ans := next()
			for ans != nil && ans["type"] != "control_response" {
				ans = next()
			}
			if ans == nil {
				return
			}
			resp := ans["response"].(map[string]any)["response"].(map[string]any)
			res := map[string]any{"type": "tool_result", "tool_use_id": "toolu_1"}
			if resp["behavior"] == "allow" {
				upd, _ := resp["updatedInput"].(map[string]any)
				res["content"] = fmt.Sprintf("ran: %v", upd["command"])
			} else {
				res["content"] = fmt.Sprintf("denied: %v", resp["message"])
				res["is_error"] = true
			}
			send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{res}}})
			send(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "done"}}}})
			result("done", turns)
		}
	}
}
