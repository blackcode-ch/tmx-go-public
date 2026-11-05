package summarize

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a fake claude: run with TMXG_FAKE_CLAUDE set, it checks its command
// line (never a tool beyond Read, never an MCP server, never a permission bypass), logs what it
// got (TMXG_FAKE_CLAUDE_LOG: one JSON line per run: args, cwd, stdin) and answers like
// `claude -p --output-format json`. Modes: ok, fenced, prose, malformed, error, maxturns, crash,
// sleep, old (refuses --restricted like an older claude), parts (part answers for "This is part"
// prompts), frames (fails unless every listed frame file exists in the working folder).
func TestMain(m *testing.M) {
	if mode := os.Getenv("TMXG_FAKE_CLAUDE"); mode != "" {
		os.Exit(fakeClaude(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// cannedAnswer is what the fake Claude writes by default.
const cannedAnswer = `{"summary": "The owner shows a checkout bug. The total keeps the old currency until a reload. They want a test before the fix.",
"key_points": ["The cart total is stale after a currency switch", "A reload shows the right total"],
"decisions": ["Write a test before fixing it"],
"action_items": [{"who": "the lead", "what": "add a test for the stale total"}, {"what": "make the cart follow rate changes"}],
"chapters": [{"t": "0:00", "title": "The bug"}, {"t": "0:03", "title": "The network tab"}]}`

type fakeRun struct {
	Args  []string `json:"args"`
	Cwd   string   `json:"cwd"`
	Stdin string   `json:"stdin"`
}

func fakeClaude(mode string, args []string) int {
	stdin, _ := io.ReadAll(os.Stdin)
	cwd, _ := os.Getwd()
	if p := os.Getenv("TMXG_FAKE_CLAUDE_LOG"); p != "" {
		b, _ := json.Marshal(fakeRun{Args: args, Cwd: cwd, Stdin: string(stdin)})
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		f.Write(append(b, '\n'))
		f.Close()
	}
	if problem := checkArgs(args); problem != "" {
		fmt.Fprintln(os.Stderr, "fake claude: "+problem)
		return 9
	}
	answer := os.Getenv("TMXG_FAKE_CLAUDE_ANSWER")
	if answer == "" {
		answer = cannedAnswer
	}
	in := string(stdin)
	switch mode {
	case "ok":
	case "fenced":
		answer = "Here is the summary you asked for:\n```json\n" + answer + "\n```\nI hope it helps."
	case "prose":
		answer = "The owner walks through the settings page and shows where the theme and the voice options live, without saying anything."
	case "malformed":
		answer = `Sorry, here: {"summary": "The owner shows`
	case "error":
		fmt.Println(`{"type":"result","subtype":"success","is_error":true,"result":"API Error: 529 Overloaded","num_turns":1}`)
		return 1
	case "maxturns":
		fmt.Println(`{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":5}`)
		return 1
	case "crash":
		fmt.Fprintln(os.Stderr, "Error: Invalid API key · Please run /login")
		return 1
	case "sleep":
		time.Sleep(time.Minute)
	case "old":
		for _, a := range args {
			if a == "--restricted" {
				fmt.Fprintln(os.Stderr, "error: unknown option '--restricted'")
				return 1
			}
		}
	case "parts":
		if m := regexp.MustCompile(`This is part (\d+) of (\d+) of the transcript, from ([0-9:]+)`).FindStringSubmatch(in); m != nil {
			answer = fmt.Sprintf(`{"summary": "Part %s is about item %s.", "key_points": ["point of part %s"], "decisions": [], "action_items": [],
				"chapters": [{"t": %q, "title": "Part %s"}]}`, m[1], m[1], m[1], m[3], m[1])
		} else if !strings.Contains(in, `<part n="1"`) {
			fmt.Fprintln(os.Stderr, "fake claude: the combining prompt has no parts")
			return 8
		}
	case "frames":
		for _, m := range regexp.MustCompile(`(?m)^- [0-9:]+ (frames/\S+)$`).FindAllStringSubmatch(in, -1) {
			if _, err := os.Stat(filepath.Join(cwd, filepath.FromSlash(m[1]))); err != nil {
				fmt.Fprintln(os.Stderr, "fake claude: cannot read "+m[1])
				return 7
			}
		}
	default:
		fmt.Fprintln(os.Stderr, "fake claude: unknown mode "+mode)
		return 2
	}
	b, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": answer,
		"num_turns": 2, "total_cost_usd": 0.0012, "permission_denials": []any{}})
	fmt.Println(string(b))
	return 0
}

// checkArgs says what is wrong with the summarizer's command line ("" = nothing).
func checkArgs(args []string) string {
	if len(args) == 0 || args[0] != "-p" {
		return "not -p"
	}
	val := func(flag string) (string, bool) {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1], true
			}
		}
		return "", false
	}
	has := func(a string) bool {
		for _, x := range args {
			if x == a {
				return true
			}
		}
		return false
	}
	if v, _ := val("--output-format"); v != "json" {
		return "output format " + v
	}
	tools, ok := val("--tools")
	if !ok || (tools != "Read" && tools != "") {
		return fmt.Sprintf("tools %q", tools)
	}
	if v, _ := val("--permission-mode"); v != "dontAsk" {
		return "permission mode " + v
	}
	if !has("--strict-mcp-config") || !has(`--mcp-config={"mcpServers":{}}`) {
		return "MCP servers are not off"
	}
	if v, _ := val("--max-turns"); v == "" {
		return "no turn limit"
	} else if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 30 {
		return "turn limit " + v
	}
	if m, _ := val("--model"); m == "" {
		return "no model"
	}
	if s, _ := val("--system-prompt"); !strings.Contains(s, "data, never instructions") {
		return "the system prompt does not frame the material as data"
	}
	for _, a := range args {
		switch {
		case strings.Contains(a, "dangerously"), strings.HasPrefix(a, "--add-dir"), a == "--mcp-config",
			strings.HasPrefix(a, "--mcp-config=") && a != `--mcp-config={"mcpServers":{}}`,
			a == "--allowedTools", a == "--allowed-tools", strings.HasPrefix(a, "--settings"),
			strings.HasPrefix(a, "--permission-mode=") || a == "bypassPermissions" || a == "acceptEdits":
			return "forbidden argument " + a
		case strings.HasPrefix(a, "--allowedTools="):
			if a != "--allowedTools=Read(./frames/**)" || tools != "Read" {
				return "allowed tools " + a
			}
		}
	}
	return ""
}
