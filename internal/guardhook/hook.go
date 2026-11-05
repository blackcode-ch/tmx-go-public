// Package guardhook is `tmx hook pretooluse`: Claude Code's PreToolUse hook for guarded agents.
// It reads the tool call on stdin, classifies it with package guard and answers in Claude's
// hook format on stdout. Allowed calls get no answer at all (Claude's own permission mode
// decides as usual); a block is denied with the reason; a hold asks the daemon, which puts it
// in Needs you, and waits for the owner. It fails closed: when the daemon cannot be reached or
// anything goes wrong, a held or blocked call is denied. Only a plain allow never needs the
// daemon. See DESIGN.md "Safety".
package guardhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Input is what Claude Code sends a PreToolUse hook (verified with Claude Code 2.1.306).
type Input struct {
	SessionID      string          `json:"session_id"`
	Cwd            string          `json:"cwd"`
	PermissionMode string          `json:"permission_mode"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
}

// Output is the hook's answer.
type Output struct {
	HookSpecificOutput Decision `json:"hookSpecificOutput"`
}

type Decision struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"` // allow | deny
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

// Options configures one run.
type Options struct {
	Client  *client.Client // the daemon with the agent's token; nil = unreachable
	Context guard.Context
	Wait    time.Duration // how long a hold waits for the owner (guard.HoldWait)
	Poll    time.Duration // one long-poll (default 50s)
}

// FromEnv builds Options from the environment the daemon gave the agent.
// Own/Workspace/StateDir come from the agent's own TMXG_GUARD_* environment,
// which is appropriate for Claude Code's PreToolUse hook (a direct child of
// the daemon's own launch, not of the agent). A Codex shim must not use this:
// see FromManifest.
func FromEnv() Options {
	return fromEnvAnd(os.Getenv(guard.EnvFolder), os.Getenv(guard.EnvWorkspace), os.Getenv(guard.EnvState))
}

// FromManifest builds Options the same way as FromEnv, except own/workspace/
// stateDir come from the pinned Codex shim manifest rather than the agent's
// environment: a shim runs as the agent's own child process (invoked from
// pip, uv, etc.) and could otherwise widen its own boundary by setting
// TMXG_GUARD_* before the shim runs. The daemon URL/token stay from the
// environment: a forged one just fails the call closed.
func FromManifest(own, workspace, stateDir string) Options {
	return fromEnvAnd(own, workspace, stateDir)
}

func fromEnvAnd(own, workspace, stateDir string) Options {
	o := Options{Wait: guard.HoldWait, Poll: 50 * time.Second}
	if u, tok := os.Getenv("TMXG_URL"), os.Getenv("TMXG_AGENT_TOKEN"); u != "" && tok != "" {
		o.Client = client.New(u, tok)
		o.Client.HTTP.Timeout = 0 // long-polls; every call has its own context
	}
	home, _ := os.UserHomeDir()
	temp := []string{os.TempDir()}
	for _, k := range []string{"TMPDIR", "TEMP", "TMP"} {
		if v := os.Getenv(k); v != "" {
			temp = append(temp, v)
		}
	}
	var names []string
	if exe, err := os.Executable(); err == nil {
		names = append(names, exe, filepath.Base(exe))
	}
	o.Context = guard.Context{
		OS: runtime.GOOS, Own: own, Workspace: workspace,
		StateDir: stateDir, Home: home, Temp: temp, Env: os.Getenv, TmxNames: names,
	}
	return o
}

// Run handles one hook call. It always answers (or stays silent for an allow) and returns
// nil; errors become denials.
func Run(ctx context.Context, in io.Reader, out io.Writer, o Options) error {
	if o.Wait <= 0 {
		o.Wait = guard.HoldWait
	}
	if o.Poll <= 0 {
		o.Poll = 50 * time.Second
	}
	b, err := io.ReadAll(io.LimitReader(in, 16<<20))
	var hin Input
	if err == nil {
		err = json.Unmarshal(b, &hin)
	}
	if err != nil {
		return answer(out, "deny", "tmx guard could not read this tool call, so it does not run.")
	}
	// Browser permission is fetched from the owner's host settings for each call,
	// never inherited from an agent-controlled environment or cached across revocation.
	// Missing daemon, malformed responses and lookup failures all retain the refusal.
	// The owner's rule switches are fetched the same way, per call: a flip applies to the next
	// tool call. If the daemon can't be asked (or sends no states), the strict built-in outcomes
	// apply: never looser than a switch the owner set.
	o.Context.AllowBrowserAutomation = false
	o.Context.States = nil
	if o.Client != nil {
		var policy struct {
			AllowBrowserAutomation bool                     `json:"allow_browser_automation"`
			States                 map[string]guard.Outcome `json:"states"`
		}
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := o.Client.Get(pctx, "/api/guard/policy", &policy)
		cancel()
		if err == nil {
			o.Context.AllowBrowserAutomation = policy.AllowBrowserAutomation
			if len(policy.States) > 0 {
				o.Context.States = policy.States
			}
		}
	}
	call := guard.Call{Tool: hin.ToolName, Input: hin.ToolInput, Cwd: hin.Cwd}
	v := guard.Check(o.Context, call)
	req := fleetReq(hin, v)
	switch v.Outcome {
	case guard.Allow:
		return nil
	case guard.Block:
		if o.Client != nil {
			rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_ = o.Client.Post(rctx, "/api/guard/blocks", req, nil) // best effort: the timeline row
			cancel()
		}
		return answer(out, "deny", v.Message()+" This is never allowed; do it another way or ask the owner to do it.")
	}
	return hold(ctx, out, o, v, req)
}

func hold(ctx context.Context, out io.Writer, o Options, v guard.Verdict, req holdReq) error {
	unreachable := v.Message() + " It needs the owner's approval, but the tmx daemon could not be reached to ask them, so it does not run."
	if o.Client == nil {
		return answer(out, "deny", unreachable)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	var ap model.Approval
	err := o.Client.Post(cctx, "/api/guard/holds", req, &ap)
	cancel()
	if err != nil {
		return answer(out, "deny", unreachable+" ("+err.Error()+")")
	}
	deadline := time.Now().Add(o.Wait)
	for ap.State == model.ApprovalPending && time.Now().Before(deadline) {
		poll := min(o.Poll, time.Until(deadline))
		pctx, cancel := context.WithTimeout(ctx, poll+15*time.Second)
		var next model.Approval
		err := o.Client.Get(pctx, fmt.Sprintf("/api/guard/holds/%s?wait=%d", client.Seg(ap.ID), int(poll/time.Millisecond)), &next)
		cancel()
		if err != nil {
			var ae *client.APIError
			if errors.As(err, &ae) || ctx.Err() != nil {
				return answer(out, "deny", v.Message()+" The request to the owner was lost ("+err.Error()+"), so it does not run.")
			}
			time.Sleep(time.Second) // the daemon is restarting; try again until the deadline
			continue
		}
		ap = next
	}
	switch ap.State {
	case model.ApprovalAllowed:
		if ap.Answer == "always" { // fleet.AnswerAlways: the owner's standing Allow always
			return answer(out, "allow", "tmx guard: the owner allows this always.")
		}
		if ap.Answer == "same as approved" { // fleet.AnswerSame: allowed once before
			return answer(out, "allow", "tmx guard: the owner allowed this exact command before.")
		}
		return answer(out, "allow", "tmx guard: the owner allowed this once.")
	case model.ApprovalDenied:
		msg := "tmx guard: the owner denied this (" + v.RuleName + ")."
		if ap.Answer != "" {
			msg += " They said: " + ap.Answer
		}
		return answer(out, "deny", msg+" Do not try to get around it; continue another way or ask the owner.")
	case model.ApprovalPending:
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = o.Client.Do(dctx, http.MethodDelete, "/api/guard/holds/"+client.Seg(ap.ID), nil, nil)
		cancel()
	}
	return answer(out, "deny", v.Message()+fmt.Sprintf(" The owner did not answer within %s, so it does not run. Ask them (ask_owner) or continue another way.", o.Wait.Round(time.Minute)))
}

type holdReq struct {
	Tool      string          `json:"tool"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	Command   string          `json:"command"`
	Rule      string          `json:"rule"`
	Detail    string          `json:"detail,omitempty"`
	Match     string          `json:"match,omitempty"`
	Cwd       string          `json:"cwd,omitempty"`
}

func fleetReq(in Input, v guard.Verdict) holdReq {
	var fields map[string]any
	_ = json.Unmarshal(in.ToolInput, &fields)
	cmd := ""
	for _, k := range []string{"command", "file_path", "notebook_path", "path"} {
		if s, ok := fields[k].(string); ok && s != "" {
			cmd = s
			break
		}
	}
	ti := in.ToolInput
	if len(ti) > 64<<10 || !json.Valid(ti) {
		ti = nil // a Write's whole content is not needed to decide
	}
	if in.ToolName == "Write" || in.ToolName == "Edit" || in.ToolName == "MultiEdit" {
		ti = nil
	}
	return holdReq{Tool: in.ToolName, ToolInput: ti, Command: cmd, Rule: v.Rule, Detail: v.Detail,
		Match: v.Match, Cwd: in.Cwd}
}

func answer(out io.Writer, decision, reason string) error {
	b, _ := json.Marshal(Output{HookSpecificOutput: Decision{HookEventName: "PreToolUse",
		PermissionDecision: decision, PermissionDecisionReason: strings.TrimSpace(reason)}})
	_, err := out.Write(append(b, '\n'))
	return err
}
