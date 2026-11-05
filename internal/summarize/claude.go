package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// One summary is one claude run, `claude -p` (checked against Claude Code 2.1.311):
//
//	claude -p --output-format json --model M --max-turns N --tools Read|"" --permission-mode dontAsk
//	  --allowedTools=Read(./frames/**) --disallowedTools=Bash,... --strict-mcp-config --mcp-config={"mcpServers":{}}
//	  --no-session-persistence --disable-slash-commands --restricted --system-prompt S   < material
//
// The working folder is the recording's folder. --tools leaves Read as the only tool (none when
// there are no frames); --restricted also confines file tools to the working folder and ignores
// the user's and the folder's settings (so no hooks run), dontAsk refuses anything that would ask,
// and no MCP server starts. Older claudes that do not know an optional flag (restricted,
// disable-slash-commands, no-session-persistence) get the run again without it. The answer is
// the JSON result's "result" text.

// optionalFlags may be dropped for a claude that does not know them.
var optionalFlags = map[string]bool{"--restricted": true, "--disable-slash-commands": true, "--no-session-persistence": true}

// noMCP is an MCP configuration without servers (with --strict-mcp-config: none at all).
const noMCP = `{"mcpServers":{}}`

// disallowed are denied on top (--tools already leaves them out).
const disallowed = "Bash,Edit,Write,MultiEdit,NotebookEdit,WebFetch,WebSearch,Task,Agent"

// ask is one claude run.
type ask struct {
	dir    string // working folder: the recording's
	system string
	prompt string // on stdin
	frames int    // frames it may read; 0 = no tools at all
	model  string
	paths  []string // the frames' paths relative to dir (the local model gets them as images)
	clinic bool     // patient data: local models only
}

// turns is the run's --max-turns: one per frame (Claude may read several at once), and a few
// for the answer.
func (a ask) turns() int {
	if a.frames == 0 {
		return 2
	}
	return a.frames + 3
}

// args is claude's command line for a, without the flags in drop.
func args(a ask, drop map[string]bool) []string {
	tools := ""
	if a.frames > 0 {
		tools = "Read"
	}
	out := []string{"-p",
		"--output-format", "json",
		"--model", a.model,
		"--max-turns", strconv.Itoa(a.turns()),
		"--tools", tools,
		"--permission-mode", "dontAsk",
	}
	if a.frames > 0 {
		out = append(out, "--allowedTools=Read(./"+FramesDir+"/**)") // variadic flags: the "=" form
	}
	out = append(out, "--disallowedTools="+disallowed,
		"--strict-mcp-config", "--mcp-config="+noMCP)
	for _, f := range []string{"--no-session-persistence", "--disable-slash-commands", "--restricted"} {
		if !drop[f] {
			out = append(out, f)
		}
	}
	return append(out, "--system-prompt", a.system)
}

// result is the part of claude's --output-format json answer read here.
type result struct {
	Type      string  `json:"type"`
	Subtype   string  `json:"subtype"`
	IsError   bool    `json:"is_error"`
	Result    string  `json:"result"`
	NumTurns  int     `json:"num_turns"`
	CostUSD   float64 `json:"total_cost_usd"`
	Denials   []any   `json:"permission_denials"`
	SessionID string  `json:"session_id"`
}

// parseResult reads claude's stdout: one JSON object (or, from a verbose claude, a list of
// messages with the result last). ok is false when it is not that at all.
func parseResult(out []byte) (result, bool) {
	out = bytes.TrimSpace(out)
	var r result
	if json.Unmarshal(out, &r) == nil && r.Type == "result" {
		return r, true
	}
	var list []result
	if json.Unmarshal(out, &list) == nil {
		for i := len(list) - 1; i >= 0; i-- {
			if list[i].Type == "result" {
				return list[i], true
			}
		}
	}
	// stream-json, one object per line: the last result line
	lines := bytes.Split(out, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var l result
		if json.Unmarshal(bytes.TrimSpace(lines[i]), &l) == nil && l.Type == "result" {
			return l, true
		}
	}
	return result{}, false
}

var unknownOptRx = regexp.MustCompile(`unknown option '(--[a-z-]+)'`)

// run writes one answer with the model that fits (the owner, 3 Nov: "we care only about performance"): the cheap
// cloud model by default, and a configured local model (summaries.local_url or via_peer) when the cloud model is not
// available; with summaries.backend "local" the local model first and the cloud model when there is no local route. A clinic recording (patient data, nLPD) is the one exception:
// local models only, and without one it fails with the reason.
func (s *Service) run(ctx context.Context, a ask) (string, error) {
	if a.clinic {
		text, err := s.runLocal(ctx, a)
		if err != nil {
			return "", fmt.Errorf("a clinic recording is summarized by local models only (patient data, nLPD), and %w", err)
		}
		s.wrote(s.localModel())
		return text, nil
	}
	if s.local() {
		text, err := s.runLocal(ctx, a)
		if err == nil {
			s.wrote(s.localModel())
			return text, nil
		}
		s.log.Info("summaries: no local model, the cloud model writes it", "err", err)
		text, cerr := s.runClaude(ctx, a)
		if cerr != nil {
			return "", fmt.Errorf("%v; and the cloud model: %w", err, cerr)
		}
		return text, nil
	}
	text, err := s.runClaude(ctx, a)
	if err == nil || !s.localConfigured() {
		return text, err
	}
	s.log.Info("summaries: the cloud model is not available, the local model writes it", "err", err)
	text, lerr := s.runLocal(ctx, a)
	if lerr != nil {
		return "", fmt.Errorf("%v; and the local model: %w", err, lerr)
	}
	s.wrote(s.localModel())
	return text, nil
}

// runClaude runs claude once and returns its answer's text.
func (s *Service) runClaude(ctx context.Context, a ask) (string, error) {
	s.wrote(a.model)
	path, err := claude.LookPath(runner.Spec{Command: s.o.Claude})
	if err != nil {
		return "", fmt.Errorf("Claude Code is needed to write summaries: %w (or set config.json claude_command)", err)
	}
	for range 4 { // each retry drops one optional flag this claude does not know
		s.mu.Lock()
		drop := make(map[string]bool, len(s.dropped))
		for k := range s.dropped {
			drop[k] = true
		}
		s.mu.Unlock()
		text, stderr, err := s.runOnce(ctx, path, args(a, drop), a)
		if err == nil {
			return text, nil
		}
		if m := unknownOptRx.FindStringSubmatch(stderr); m != nil && optionalFlags[m[1]] && !drop[m[1]] {
			s.log.Info("summaries: this claude does not know a flag; running without it", "flag", m[1])
			s.mu.Lock()
			s.dropped[m[1]] = true
			s.mu.Unlock()
			continue
		}
		return "", err
	}
	return "", errors.New("claude refused its command line")
}

// runOnce starts claude with argv, feeds it the prompt and waits for its answer (at most
// Options.Timeout). It returns the answer's text, or an error and the end of stderr.
func (s *Service) runOnce(ctx context.Context, path string, argv []string, a ask) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.o.Timeout)
	defer cancel()
	argv0 := path
	if !s.o.NoLowPriority {
		argv0, argv = sysinfo.LowPriorityCommand(path, argv) // macOS: low disk I/O policy
	}
	cmd := hiddenexec.CommandContext(ctx, argv0, argv...)
	cmd.Dir = a.dir
	cmd.Env = claude.MergeEnv(claude.BaseEnv(), claude.EnvConfigDir+"="+s.o.ClaudeConfigDir)
	ticket, err := claude.BeginStart(ctx, cmd.Env, claude.HolderOther, nil) // one claude start at a time (also checks the dir)
	if err != nil {
		return "", "", err
	}
	defer ticket.Release() // it exited: its startup writes are over
	cmd.Stdin = strings.NewReader(a.prompt)
	stdout, stderr := &capped{max: 4 << 20}, &capped{max: 8 << 10, tail: true}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	setProcAttr(cmd)
	cmd.Cancel = func() error { killTree(cmd); return nil }
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("Claude Code does not start (%s): %w", path, err)
	}
	ticket.Settled()
	sysinfo.RegisterChild(cmd.Process.Pid, false)
	if !s.o.NoLowPriority {
		_ = sysinfo.LowerPriority(cmd.Process.Pid)
	}
	werr := cmd.Wait()
	reap(cmd)
	errText := strings.TrimSpace(stderr.String())
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", errText, fmt.Errorf("Claude took longer than %s and was stopped", s.o.Timeout.Round(time.Second))
	case ctx.Err() != nil:
		return "", errText, ctx.Err()
	}
	res, ok := parseResult(stdout.Bytes())
	if !ok {
		if werr != nil {
			return "", errText, fmt.Errorf("claude failed (%v)%s", werr, explain(errText, stdout.String()))
		}
		// A claude that printed plain text: that is the answer.
		return strings.TrimSpace(stdout.String()), errText, nil
	}
	s.log.Info("summaries: claude answered", "turns", res.NumTurns, "denials", len(res.Denials))
	if res.IsError || (res.Subtype != "" && res.Subtype != "success") {
		if res.Subtype == "error_max_turns" {
			return "", errText, fmt.Errorf("Claude used up its %d turns before answering", a.turns())
		}
		why := strings.TrimSpace(res.Result)
		if why == "" {
			why = res.Subtype
		}
		return "", errText, fmt.Errorf("Claude answered with an error: %s", clip(oneLine(why), 300))
	}
	return res.Result, errText, nil
}

func explain(stderr, stdout string) string {
	t := stderr
	if t == "" {
		t = stdout
	}
	if t = clip(oneLine(t), 300); t != "" {
		return ": " + t
	}
	return ""
}

// write has Claude write the summary of r from in: in one run, or part by part and then
// combined. It returns the summary and how many parts there were.
func (s *Service) write(ctx context.Context, r model.Recording, in input, m string) (model.RecordingSummary, int, error) {
	frames := len(in.frames)
	parts, dropped := split(in.lines, r.DurationMS, MaxChars, MaxParts)
	if len(parts) <= 1 && dropped < 0 {
		text, err := s.run(ctx, ask{dir: r.Dir, clinic: r.Clinic(), system: systemPrompt(frames), prompt: singlePrompt(r, in), frames: frames, model: m, paths: framePaths(in)})
		if err != nil {
			return model.RecordingSummary{}, 1, err
		}
		sum, err := parseAnswer(text)
		return sum, 1, err
	}
	sums := make([]model.RecordingSummary, len(parts))
	for i, c := range parts {
		text, err := s.run(ctx, ask{dir: r.Dir, clinic: r.Clinic(), system: partSystemPrompt, prompt: partPrompt(r, in, c, i, len(parts)), model: m})
		if err == nil {
			sums[i], err = parseAnswer(text)
		}
		if err != nil {
			return model.RecordingSummary{}, len(parts), fmt.Errorf("part %d of %d: %w", i+1, len(parts), err)
		}
	}
	text, err := s.run(ctx, ask{dir: r.Dir, clinic: r.Clinic(), system: systemPrompt(frames), prompt: combinePrompt(r, in, parts, sums, dropped), frames: frames, model: m, paths: framePaths(in)})
	if err != nil {
		return model.RecordingSummary{}, len(parts), fmt.Errorf("combining the %d parts: %w", len(parts), err)
	}
	sum, err := parseAnswer(text)
	return sum, len(parts), err
}

func framePaths(in input) []string {
	out := make([]string, len(in.frames))
	for i, f := range in.frames {
		out[i] = f.path
	}
	return out
}

// capped keeps at most max bytes: the first ones, or with tail the last ones.
type capped struct {
	buf  []byte
	max  int
	tail bool
}

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	if c.tail {
		c.buf = append(c.buf, p...)
		if len(c.buf) > c.max {
			c.buf = c.buf[len(c.buf)-c.max:]
		}
		return n, nil
	}
	if room := c.max - len(c.buf); room > 0 {
		c.buf = append(c.buf, p[:min(room, len(p))]...)
	}
	return n, nil
}

func (c *capped) Bytes() []byte  { return c.buf }
func (c *capped) String() string { return string(c.buf) }
