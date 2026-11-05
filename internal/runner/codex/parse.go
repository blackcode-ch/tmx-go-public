package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/codexguard"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

const maxToolResult = 64 << 10

// Approval request methods (server → client).
const (
	mCommandApproval = "item/commandExecution/requestApproval"
	mFileApproval    = "item/fileChange/requestApproval"
	mPermissions     = "item/permissions/requestApproval"
	mUserInput       = "item/tool/requestUserInput"
	mElicitation     = "mcpServer/elicitation/request"
	mLegacyExec      = "execCommandApproval"
	mLegacyPatch     = "applyPatchApproval"
)

// pendingReq is a server request waiting for the owner.
type pendingReq struct {
	rawID       json.RawMessage
	method      string
	permissions json.RawMessage // item/permissions: what was asked (granted as asked)
	questionIDs []string        // item/tool/requestUserInput
}

// parser turns app-server lines into runner events and tracks the conversation. Not safe
// for concurrent use; the process serialises access.
type parser struct {
	log    *slog.Logger
	nextID func() string

	initID, threadReqID string
	configReqID         string
	guardExecutable     string
	guardContext        *guard.Context
	guardStates         func() map[string]string // nil = the strict built-in outcomes
	resumeID            string                   // conversation to resume ("" = new)
	threadParams        map[string]any           // thread/start (and resume) settings
	threadID            string
	ready               bool

	state    model.AgentState
	busy     bool // a turn is running
	turnID   string
	lastText string // the turn's last agent message
	tokens   int64

	pending map[string]pendingReq // approval id (the request id's JSON) -> request
	items   map[string]wireItem   // started items by id (context for approvals)
	queued  []string              // user turns sent before the thread was ready
}

// out collects what one line produced.
type out struct {
	events  []runner.Event
	replies [][]byte
	close   bool // nothing more can happen (no thread): end the process
}

func (o *out) emit(ev runner.Event) { o.events = append(o.events, ev) }

func (o *out) entry(e model.TimelineEntry) { o.emit(runner.Event{Kind: runner.EvTimeline, Entry: &e}) }

func (p *parser) setState(o *out, s model.AgentState) {
	if p.state != s {
		p.state = s
		o.emit(runner.Event{Kind: runner.EvState, State: s})
	}
}

func (p *parser) workingOrWaiting(o *out) {
	if len(p.pending) > 0 {
		p.setState(o, model.StateWaiting)
	} else {
		p.setState(o, model.StateWorking)
	}
}

// line handles one stdout line. Malformed or unknown input yields nothing.
func (p *parser) line(b []byte) out {
	var o out
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return o
	}
	var m wireMsg
	if err := json.Unmarshal(b, &m); err != nil {
		p.log.Debug("codex: non-JSON stdout line", "line", clip(string(b), 200))
		return o
	}
	switch {
	case m.Method != "" && len(m.ID) > 0:
		p.serverRequest(&o, m)
	case m.Method != "":
		p.notification(&o, m)
	case len(m.ID) > 0:
		p.response(&o, m)
	}
	return o
}

func idString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func (p *parser) response(o *out, m wireMsg) {
	id := idString(m.ID)
	switch id {
	case p.initID:
		if m.Error != nil {
			o.entry(model.TimelineEntry{Kind: model.EntryError, IsError: true, Text: "codex initialize failed: " + m.Error.Message})
			o.close = true
			return
		}
		o.replies = append(o.replies, notification("initialized"))
		if p.guardExecutable != "" {
			p.configReqID = p.nextID()
			o.replies = append(o.replies, request(p.configReqID, "config/read", map[string]any{"includeLayers": true, "cwd": p.threadParams["cwd"]}))
		} else {
			o.replies = append(o.replies, p.threadRequest())
		}
	case p.configReqID:
		if p.guardExecutable == "" {
			return
		}
		if err := codexguard.ValidateEffective(m.Result, p.guardExecutable); m.Error != nil || err != nil {
			o.entry(model.TimelineEntry{Kind: model.EntryError, IsError: true, Text: "Codex effective configuration failed the Windows guard check; no turn was started"})
			o.close = true
			return
		}
		o.replies = append(o.replies, p.threadRequest())
	case p.threadReqID:
		if m.Error != nil {
			if p.resumeID != "" {
				o.entry(model.TimelineEntry{Kind: model.EntryError, IsError: true,
					Text: fmt.Sprintf("codex could not resume conversation %s (%s); starting a new one", p.resumeID, m.Error.Message)})
				p.resumeID = ""
				o.replies = append(o.replies, p.threadRequest())
				return
			}
			o.entry(model.TimelineEntry{Kind: model.EntryError, IsError: true, Text: "codex could not start a conversation: " + m.Error.Message})
			o.close = true
			return
		}
		var r wireThreadResult
		_ = json.Unmarshal(m.Result, &r)
		p.threadID, p.ready = r.Thread.ID, true
		if p.threadID != "" {
			o.emit(runner.Event{Kind: runner.EvSession, SessionID: p.threadID})
		}
		if len(p.queued) > 0 {
			text := strings.Join(p.queued, "\n\n")
			p.queued = nil
			o.replies = append(o.replies, p.turnStart(text))
			p.busy = true
			p.workingOrWaiting(o)
		} else if !p.busy {
			p.setState(o, model.StateIdle)
		}
	default:
		if m.Error != nil && strings.HasPrefix(id, "turn-") {
			o.entry(model.TimelineEntry{Kind: model.EntryError, IsError: true, Text: "codex did not take the message: " + m.Error.Message})
			p.busy = false
			p.setState(o, model.StateIdle)
			return
		}
		if strings.HasPrefix(id, "turn-") && p.turnID == "" {
			var r struct {
				Turn wireTurn `json:"turn"`
			}
			if json.Unmarshal(m.Result, &r) == nil {
				p.turnID = r.Turn.ID
			}
		}
	}
}

// threadRequest starts or resumes the conversation.
func (p *parser) threadRequest() []byte {
	p.threadReqID = p.nextID()
	params := map[string]any{}
	for k, v := range p.threadParams {
		params[k] = v
	}
	if p.resumeID != "" {
		params["threadId"] = p.resumeID
		params["excludeTurns"] = true
		return request(p.threadReqID, "thread/resume", params)
	}
	return request(p.threadReqID, "thread/start", params)
}

func (p *parser) turnStart(text string) []byte {
	return request("turn-"+p.nextID(), "turn/start", map[string]any{"threadId": p.threadID, "input": textInput(text)})
}

// send starts a user turn, or queues it until the thread exists. While a turn runs, codex
// adds the input to it (turn/start steers an active turn).
func (p *parser) send(o *out, text string) {
	o.emit(runner.Event{Kind: runner.EvTimeline, Entry: &model.TimelineEntry{Kind: model.EntryUser, Text: text}})
	if !p.ready {
		p.queued = append(p.queued, text)
		return
	}
	p.busy = true
	p.workingOrWaiting(o)
	o.replies = append(o.replies, p.turnStart(text))
}

func (p *parser) notification(o *out, m wireMsg) {
	switch m.Method {
	case "turn/started":
		var n wireTurnNote
		_ = json.Unmarshal(m.Params, &n)
		p.turnID, p.busy, p.lastText = n.Turn.ID, true, ""
		p.workingOrWaiting(o)
	case "item/started":
		var n wireItemNote
		_ = json.Unmarshal(m.Params, &n)
		p.itemStarted(o, n.Item)
	case "item/completed":
		var n wireItemNote
		_ = json.Unmarshal(m.Params, &n)
		p.itemCompleted(o, n.Item)
	case "turn/completed":
		var n wireTurnNote
		_ = json.Unmarshal(m.Params, &n)
		p.turnCompleted(o, n.Turn)
	case "thread/tokenUsage/updated":
		var n struct {
			TokenUsage struct {
				Total struct {
					TotalTokens int64 `json:"totalTokens"`
				} `json:"total"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(m.Params, &n) == nil {
			p.tokens = n.TokenUsage.Total.TotalTokens
		}
	case "error":
		var n wireErrorNote
		_ = json.Unmarshal(m.Params, &n)
		if !n.WillRetry && n.Error.Message != "" {
			o.entry(model.TimelineEntry{Kind: model.EntryError, IsError: true, Text: n.Error.Message})
		}
	case "mcpServer/startupStatus/updated":
		var n wireMCPStatus
		_ = json.Unmarshal(m.Params, &n)
		if n.Status == "failed" {
			msg := "MCP server " + n.Name + " failed to start"
			if n.Error != nil && *n.Error != "" {
				msg += ": " + *n.Error
			}
			o.entry(model.TimelineEntry{Kind: model.EntryError, IsError: true, Text: msg})
		}
	case "serverRequest/resolved":
		var n wireResolved
		_ = json.Unmarshal(m.Params, &n)
		key := string(n.RequestID)
		if _, ok := p.pending[key]; ok {
			delete(p.pending, key)
			o.emit(runner.Event{Kind: runner.EvApprovalCancelled, Approval: &runner.ApprovalRequest{ID: key}})
			if p.busy {
				p.workingOrWaiting(o)
			}
		}
	default:
		p.log.Debug("codex: ignored notification", "method", m.Method)
	}
}

func (p *parser) itemStarted(o *out, it wireItem) {
	if it.ID != "" {
		p.items[it.ID] = it
	}
	if !p.busy {
		p.busy = true
		p.workingOrWaiting(o)
	}
	switch it.Type {
	case "commandExecution":
		o.entry(model.TimelineEntry{Kind: model.EntryToolUse, Tool: "Bash", ToolUseID: it.ID,
			Input: mustJSON(map[string]any{"command": it.Command, "cwd": it.Cwd})})
	case "fileChange":
		o.entry(model.TimelineEntry{Kind: model.EntryToolUse, Tool: "apply_patch", ToolUseID: it.ID,
			Input: mustJSON(map[string]any{"changes": it.Changes})})
	case "mcpToolCall":
		args := it.Arguments
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage("{}")
		}
		o.entry(model.TimelineEntry{Kind: model.EntryToolUse, Tool: "mcp__" + it.Server + "__" + it.Tool, ToolUseID: it.ID, Input: args})
	case "webSearch":
		o.entry(model.TimelineEntry{Kind: model.EntryToolUse, Tool: "WebSearch", ToolUseID: it.ID,
			Input: mustJSON(map[string]any{"query": it.Query})})
	}
}

func (p *parser) itemCompleted(o *out, it wireItem) {
	delete(p.items, it.ID)
	switch it.Type {
	case "agentMessage":
		if it.Text != "" {
			p.lastText = it.Text
			o.entry(model.TimelineEntry{Kind: model.EntryAssistant, Text: it.Text})
		}
	case "reasoning":
		if t := strings.TrimSpace(strings.Join(it.Summary, "\n\n")); t != "" {
			o.entry(model.TimelineEntry{Kind: model.EntryThinking, Text: t})
		}
	case "plan":
		if it.Text != "" {
			o.entry(model.TimelineEntry{Kind: model.EntryAssistant, Text: it.Text, Meta: map[string]any{"codex_item": "plan"}})
		}
	case "commandExecution":
		failed := it.Status == "failed" || it.Status == "declined" || it.ExitCode != nil && *it.ExitCode != 0
		text := it.AggregatedOutput
		if it.Status == "declined" && text == "" {
			text = "declined"
		}
		o.entry(toolResult(it.ID, text, failed))
	case "fileChange":
		text := fmt.Sprintf("%s: %d file(s)", it.Status, len(it.Changes))
		o.entry(toolResult(it.ID, text, it.Status != "completed"))
	case "mcpToolCall":
		var text string
		if it.Error != nil {
			text = it.Error.Message
		} else if it.Result != nil {
			text = contentText(it.Result.Content)
		}
		o.entry(toolResult(it.ID, text, it.Error != nil || it.Status == "failed"))
	case "webSearch":
		o.entry(toolResult(it.ID, "searched", false))
	case "contextCompaction":
		o.entry(model.TimelineEntry{Kind: model.EntrySystem, Text: "codex compacted the conversation"})
	}
}

func (p *parser) turnCompleted(o *out, t wireTurn) {
	meta := map[string]any{"subtype": t.Status}
	if t.DurationMS != nil {
		meta["duration_ms"] = *t.DurationMS
	}
	if p.tokens > 0 {
		meta["total_tokens"] = p.tokens
	}
	text := p.lastText
	failed := t.Status == "failed"
	if failed && t.Error != nil && t.Error.Message != "" {
		text = t.Error.Message
	}
	if t.Status == "interrupted" && text == "" {
		text = "interrupted"
	}
	o.entry(model.TimelineEntry{Kind: model.EntryResult, Text: text, IsError: failed, Meta: meta})
	o.emit(runner.Event{Kind: runner.EvUsage, Turns: 1})
	for key := range p.pending { // the turn ended: nothing waits for these answers anymore
		o.emit(runner.Event{Kind: runner.EvApprovalCancelled, Approval: &runner.ApprovalRequest{ID: key}})
	}
	clear(p.pending)
	p.busy, p.turnID, p.lastText = false, "", ""
	p.setState(o, model.StateIdle)
}

func (p *parser) serverRequest(o *out, m wireMsg) {
	key := string(m.ID)
	req := pendingReq{rawID: m.ID, method: m.Method}
	ap := runner.ApprovalRequest{ID: key}
	var literalExec []string
	switch m.Method {
	case mCommandApproval:
		var r wireCommandApproval
		_ = json.Unmarshal(m.Params, &r)
		ap.Tool, ap.Description = "Bash", r.Reason
		ap.Input = mustJSON(map[string]any{"command": r.Command, "cwd": r.Cwd})
	case mLegacyExec:
		var r wireLegacyExec
		_ = json.Unmarshal(m.Params, &r)
		literalExec = r.Command
		ap.Tool, ap.Description = "Bash", r.Reason
		ap.Input = mustJSON(map[string]any{"command": strings.Join(r.Command, " "), "cwd": r.Cwd})
	case mFileApproval:
		var r wireFileApproval
		_ = json.Unmarshal(m.Params, &r)
		ap.Tool, ap.Description = "apply_patch", r.Reason
		ap.Input = mustJSON(map[string]any{"changes": p.items[r.ItemID].Changes, "grant_root": r.GrantRoot})
	case mLegacyPatch:
		var r wireLegacyPatch
		_ = json.Unmarshal(m.Params, &r)
		ap.Tool, ap.Description = "apply_patch", r.Reason
		ap.Input = mustJSON(map[string]any{"file_changes": r.FileChanges})
	case mPermissions:
		var r wirePermissionsApproval
		_ = json.Unmarshal(m.Params, &r)
		ap.Tool, ap.Description = "permissions", r.Reason
		ap.Input = mustJSON(map[string]any{"permissions": r.Permissions, "cwd": r.Cwd})
		req.permissions = r.Permissions
	case mUserInput:
		var r wireUserInput
		_ = json.Unmarshal(m.Params, &r)
		ap.Kind = model.ApprovalQuestion
		var qs []string
		for i, q := range r.Questions {
			req.questionIDs = append(req.questionIDs, q.ID)
			qs = append(qs, strings.TrimSpace(strings.TrimSpace(q.Header+"\n")+q.Question))
			if i == 0 {
				for _, opt := range q.Options {
					ap.Options = append(ap.Options, opt.Label)
				}
			}
		}
		ap.Question = strings.Join(qs, "\n\n")
	case mElicitation:
		// An MCP server asks for input through codex; tmx's own tools never do.
		o.replies = append(o.replies, response(m.ID, map[string]any{"action": "decline", "content": nil, "_meta": nil}))
		return
	default:
		// codex waits for an answer to every request; never leave one hanging.
		p.log.Debug("codex: unsupported request", "method", m.Method)
		o.replies = append(o.replies, errorResponse(m.ID, -32601, "tmx does not support "+m.Method))
		return
	}
	if len(ap.Input) == 0 {
		ap.Input = json.RawMessage("{}")
	}
	if p.guardContext != nil && (m.Method == mCommandApproval || m.Method == mLegacyExec) {
		tool := "PowerShell"
		if p.guardContext.OS != "windows" {
			tool = "Bash"
		}
		var input struct {
			Cwd     string `json:"cwd"`
			Command string `json:"command"`
		}
		_ = json.Unmarshal(ap.Input, &input)
		ctx := *p.guardContext
		if input.Cwd != "" {
			ctx.Cwd = input.Cwd
		}
		if p.guardStates != nil {
			ctx.States = map[string]guard.Outcome{}
			for id, o := range p.guardStates() {
				ctx.States[id] = guard.Outcome(o)
			}
		}
		v := guard.Check(ctx, guard.Call{Tool: tool, Input: ap.Input, Cwd: input.Cwd})
		if len(literalExec) > 0 {
			v = guard.CheckExec(ctx, literalExec[0], literalExec[1:])
		}
		if v.Outcome == guard.Block || strings.TrimSpace(input.Command) == "" {
			p.pending[key] = req
			_ = p.answer(o, key, runner.Decision{Allow: false, Message: v.Message()})
			o.entry(model.TimelineEntry{Kind: model.EntryError, IsError: true, Text: "Codex command refused by the tmx guard"})
			return
		}
		if v.Outcome == guard.Hold {
			ap.Description = "tmx guard: " + v.Message()
		}
	}
	p.pending[key] = req
	o.emit(runner.Event{Kind: runner.EvApproval, Approval: &ap})
	p.setState(o, model.StateWaiting)
}

// answer builds the reply to a pending request.
func (p *parser) answer(o *out, key string, d runner.Decision) error {
	req, ok := p.pending[key]
	if !ok {
		return fmt.Errorf("no pending codex request %s", key)
	}
	delete(p.pending, key)
	var result any
	switch req.method {
	case mCommandApproval, mFileApproval:
		result = map[string]any{"decision": pick(d.Allow, "accept", "decline")}
	case mLegacyExec, mLegacyPatch:
		if d.Allow {
			result = map[string]any{"decision": "approved"}
		} else {
			result = map[string]any{"decision": map[string]any{"denied": map[string]any{"rejection": d.Message}}}
		}
	case mPermissions:
		granted := json.RawMessage("{}")
		if d.Allow && len(req.permissions) > 0 {
			granted = grantAsAsked(req.permissions)
		}
		result = map[string]any{"permissions": granted, "scope": "turn"}
	case mUserInput:
		answers := map[string]any{}
		if ans := strings.TrimSpace(d.Answer); ans != "" && d.Allow {
			for i, id := range req.questionIDs {
				if i == 0 {
					answers[id] = map[string]any{"answers": []string{ans}}
				}
			}
		}
		result = map[string]any{"answers": answers}
	}
	o.replies = append(o.replies, response(req.rawID, result))
	if p.busy {
		p.workingOrWaiting(o)
	}
	return nil
}

// grantAsAsked turns a RequestPermissionProfile ({network: X|null, fileSystem: Y|null})
// into the GrantedPermissionProfile that grants exactly that.
func grantAsAsked(asked json.RawMessage) json.RawMessage {
	var in map[string]json.RawMessage
	if json.Unmarshal(asked, &in) != nil {
		return json.RawMessage("{}")
	}
	outp := map[string]json.RawMessage{}
	for k, v := range in {
		if len(v) > 0 && string(v) != "null" {
			outp[k] = v
		}
	}
	return mustJSON(outp)
}

func pick(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

func toolResult(id, text string, isErr bool) model.TimelineEntry {
	e := model.TimelineEntry{Kind: model.EntryToolResult, ToolUseID: id, IsError: isErr}
	if len(text) > maxToolResult {
		e.Meta = map[string]any{"truncated_from": len(text)}
		text = clip(text, maxToolResult)
	}
	e.Text = text
	return e
}

// contentText flattens MCP result content ([{type:text,text}...]).
func contentText(parts []json.RawMessage) string {
	var out []string
	for _, raw := range parts {
		var c struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &c) == nil && c.Type == "text" {
			out = append(out, c.Text)
		} else if c.Type != "" {
			out = append(out, "["+c.Type+"]")
		}
	}
	return strings.Join(out, "\n")
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// clip cuts s to at most n bytes without splitting a UTF-8 sequence.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
