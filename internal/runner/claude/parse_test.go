package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// feed runs every line of a fixture through p and returns the events and replies.
func feed(t *testing.T, p *parser, fixture string) ([]runner.Event, [][]byte) {
	t.Helper()
	f, err := os.Open("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var evs []runner.Event
	var replies [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		o := p.line(sc.Bytes())
		evs = append(evs, o.events...)
		replies = append(replies, o.replies...)
	}
	return evs, replies
}

// describe renders an event compactly for comparisons.
func describe(ev runner.Event) string {
	switch ev.Kind {
	case runner.EvState:
		return "state:" + string(ev.State)
	case runner.EvSession:
		return "session:" + ev.SessionID
	case runner.EvApproval:
		return "approval:" + ev.Approval.ID + ":" + ev.Approval.Tool
	case runner.EvUsage:
		return fmt.Sprintf("usage:%g:%d", ev.CostUSD, ev.Turns)
	case runner.EvTimeline:
		e := ev.Entry
		s := "timeline:" + string(e.Kind)
		if e.Tool != "" {
			s += ":" + e.Tool
		}
		if e.Text != "" {
			s += ":" + e.Text
		}
		if e.IsError {
			s += ":error"
		}
		return s
	}
	return string(ev.Kind)
}

func describeAll(evs []runner.Event) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = describe(ev)
	}
	return out
}

func TestParseTranscript(t *testing.T) {
	p := newParser("tmx-1", quiet)
	p.state = model.StateStarting
	p.busy = true // as after Send: system/init must not flip it to idle
	p.state = model.StateWorking

	evs, replies := feed(t, p, "transcript.jsonl")
	got := describeAll(evs)
	want := []string{
		// the initialize reply arrives while busy: no idle
		"session:5b0c3c56-1f4e-4a8e-9a39-9a0e1c1d2e3f",
		"timeline:tool_use:Bash",
		"approval:perm-1:Bash",
		"state:waiting",
		"timeline:tool_result:(Bash completed with no output)",
		"timeline:assistant:done",
		"timeline:result:done",
		"usage:0.078:2",
		"state:idle",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events:\n got %q\nwant %q", got, want)
	}
	if len(replies) != 0 {
		t.Fatalf("unexpected replies %q", replies)
	}

	use := evs[1].Entry
	if use.ToolUseID != "toolu_01" || string(use.Input) != `{"command":"echo hello > a.txt"}` {
		t.Errorf("tool_use entry = %+v", use)
	}
	ap := evs[2].Approval
	if ap.Description != "Write hello to a.txt" || !json.Valid(ap.Suggestions) || !strings.Contains(string(ap.Input), "description") {
		t.Errorf("approval = %+v", ap)
	}
	if res := evs[4].Entry; res.ToolUseID != "toolu_01" || res.IsError {
		t.Errorf("tool_result entry = %+v", res)
	}
	meta := evs[6].Entry.Meta
	if meta["num_turns"] != 2 || meta["total_cost_usd"] != 0.078 || meta["duration_ms"] != 5123.0 || meta["subtype"] != "success" {
		t.Errorf("result meta = %v", meta)
	}
}

func TestParseInitReplyMeansIdle(t *testing.T) {
	p := newParser("tmx-1", quiet)
	p.state = model.StateStarting
	o := p.line([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"tmx-1","response":{}}}`))
	if got := describeAll(o.events); !slices.Equal(got, []string{"state:idle"}) {
		t.Fatalf("got %q", got)
	}
	// someone else's response changes nothing
	o = p.line([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"tmx-7"}}`))
	if len(o.events) != 0 {
		t.Fatalf("got %q", describeAll(o.events))
	}
}

func TestParseMalformedNeverPanics(t *testing.T) {
	p := newParser("tmx-1", quiet)
	p.state = model.StateIdle
	evs, replies := feed(t, p, "malformed.jsonl")
	got := describeAll(evs)
	want := []string{
		"state:working", // activity while idle
		"timeline:thinking:weighing options",
		"timeline:tool_result:line one\n[image]\nline two:error",
		"approval:perm-9:Write",
		"state:waiting",
		"approval_cancelled", // claude withdrew perm-9
		"state:working",
		"session:sid-2",
		"timeline:result:error_max_turns:error",
		"usage:0:0",
		"state:idle",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events:\n got %q\nwant %q", got, want)
	}
	if evs[2].Entry.Meta["parent_tool_use_id"] != "toolu_parent" {
		t.Errorf("meta = %v", evs[2].Entry.Meta)
	}
	if string(evs[3].Approval.Input) != "{}" {
		t.Errorf("missing input should become {}, got %s", evs[3].Approval.Input)
	}
	if len(replies) != 1 || !strings.Contains(string(replies[0]), `"request_id":"hook-1"`) ||
		!strings.Contains(string(replies[0]), `"subtype":"error"`) {
		t.Fatalf("replies = %q", replies)
	}
}

func TestToolResultTruncated(t *testing.T) {
	p := newParser("tmx-1", quiet)
	big := strings.Repeat("é", maxToolResult) // 2 bytes each
	line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "t", "content": big},
	}}})
	o := p.line(line)
	e := o.events[len(o.events)-1].Entry
	if len(e.Text) > maxToolResult || !strings.HasPrefix(big, e.Text) || e.Meta["truncated_from"] != len(big) {
		t.Fatalf("len %d meta %v", len(e.Text), e.Meta)
	}
}

func TestArgs(t *testing.T) {
	spec := runner.Spec{
		Model: "claude-sonnet-5-5", SessionID: "sid", PermissionMode: "auto",
		MCPConfigPath: "/x/mcp.json", AllowedTools: []string{"mcp__tmxg", "Read"},
		DisallowedTools: []string{"Bash", "Edit"},
		SystemPrompt:    "be brief", ExtraArgs: []string{"--verbose"},
	}
	got := strings.Join(ChatArgs(spec), " ")
	want := "-p --input-format stream-json --output-format stream-json --verbose --permission-prompt-tool stdio " +
		"--setting-sources  --model claude-sonnet-5-5 --resume sid --permission-mode auto --mcp-config=/x/mcp.json --strict-mcp-config " +
		"--allowedTools=mcp__tmxg,Read --disallowedTools=Bash,Edit --append-system-prompt be brief --verbose"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if got := ChatArgs(runner.Spec{}); len(got) != 10 {
		t.Fatalf("empty spec adds flags: %q", got)
	}
	if got := TerminalArgs(runner.Spec{Model: "m"}); !slices.Equal(got, []string{"--setting-sources", "", "--model", "m"}) {
		t.Fatalf("terminal args %q", got)
	}
}

func TestMergeEnv(t *testing.T) {
	got := MergeEnv([]string{"A=1", "B=2"}, "B=3", "C=4")
	if !slices.Equal(got, []string{"A=1", "B=3", "C=4"}) {
		t.Fatalf("%q", got)
	}
}

func TestParseRateLimitEvent(t *testing.T) {
	p := newParser("tmx-1", quiet)
	p.state = model.StateIdle
	if o := p.line([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1761000000},"uuid":"u"}`)); len(o.events) != 0 {
		t.Fatalf("allowed: %q", describeAll(o.events))
	}
	o := p.line([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1761000000,"rateLimitType":"seven_day"},"uuid":"u"}`))
	if len(o.events) != 1 || o.events[0].Kind != runner.EvRateLimit ||
		*o.events[0].RateLimit != (runner.RateLimit{Status: "rejected", Type: "seven_day", ResetsAt: 1761000000}) {
		t.Fatalf("rejected: %+v", o.events)
	}
	if o := p.line([]byte(`{"type":"rate_limit_event"}`)); len(o.events) != 0 {
		t.Fatalf("no info: %q", describeAll(o.events))
	}
}
