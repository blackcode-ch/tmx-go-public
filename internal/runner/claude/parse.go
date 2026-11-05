package claude

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// maxToolResult caps EntryToolResult.Text.
const maxToolResult = 64 << 10

// wireMsg is the union of the stdout events we read (see DESIGN.md "Claude stream-json protocol").
type wireMsg struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	ParentToolUseID string          `json:"parent_tool_use_id"`
	Error           string          `json:"error"`
	Message         *wireContent    `json:"message"`
	RequestID       string          `json:"request_id"`
	Request         json.RawMessage `json:"request"`
	Response        json.RawMessage `json:"response"`

	// result
	Result       string   `json:"result"`
	IsError      bool     `json:"is_error"`
	NumTurns     *int     `json:"num_turns"`
	TotalCostUSD *float64 `json:"total_cost_usd"`
	DurationMS   *float64 `json:"duration_ms"`

	RateLimitInfo *wireRateLimit `json:"rate_limit_info"` // rate_limit_event
}

// wireRateLimit is a rate_limit_event's rate_limit_info.
type wireRateLimit struct {
	Status        string   `json:"status"` // allowed | allowed_warning | rejected
	ResetsAt      *float64 `json:"resetsAt"`
	RateLimitType string   `json:"rateLimitType"`
}

type wireContent struct {
	Content json.RawMessage `json:"content"` // a string or []wireBlock
}

type wireBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type wireControlRequest struct {
	Subtype     string          `json:"subtype"`
	ToolName    string          `json:"tool_name"`
	Input       json.RawMessage `json:"input"`
	Description string          `json:"description"`
	Suggestions json.RawMessage `json:"permission_suggestions"`
}

type wireControlResponse struct {
	Subtype   string `json:"subtype"`
	RequestID string `json:"request_id"`
	Error     string `json:"error"`
}

// parser turns stdout lines into runner events and tracks the agent state. It is not safe for
// concurrent use; the process serialises access.
type parser struct {
	initID    string // request_id of our initialize request
	sessionID string
	state     model.AgentState
	busy      bool                       // a user turn is in flight (Send .. result)
	pending   map[string]json.RawMessage // can_use_tool request id -> original input
	log       *slog.Logger
}

func newParser(initID string, log *slog.Logger) *parser {
	return &parser{initID: initID, pending: map[string]json.RawMessage{}, log: log}
}

// out collects what one line produced: events for the consumer and lines to write back.
type out struct {
	events  []runner.Event
	replies [][]byte
}

func (o *out) emit(ev runner.Event) { o.events = append(o.events, ev) }

func (o *out) entry(e model.TimelineEntry) {
	o.emit(runner.Event{Kind: runner.EvTimeline, Entry: &e})
}

func (p *parser) setState(o *out, s model.AgentState) {
	if p.state == s {
		return
	}
	p.state = s
	o.emit(runner.Event{Kind: runner.EvState, State: s})
}

// activity: output while we thought it idle means a turn is running (e.g. queued mail).
func (p *parser) activity(o *out) {
	if p.state == model.StateIdle || p.state == model.StateStarting {
		p.setState(o, model.StateWorking)
	}
}

func (p *parser) setSession(o *out, sid string) {
	if sid == "" || sid == p.sessionID {
		return
	}
	p.sessionID = sid
	o.emit(runner.Event{Kind: runner.EvSession, SessionID: sid})
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
		p.log.Debug("claude: non-JSON stdout line", "line", truncate(string(b), 200))
		return o
	}
	switch m.Type {
	case "control_response":
		p.controlResponse(&o, m)
	case "control_request":
		p.controlRequest(&o, m)
	case "control_cancel_request":
		if _, ok := p.pending[m.RequestID]; ok {
			o.emit(runner.Event{Kind: runner.EvApprovalCancelled, Approval: &runner.ApprovalRequest{ID: m.RequestID}})
		}
		delete(p.pending, m.RequestID)
		if len(p.pending) == 0 && p.state == model.StateWaiting {
			p.setState(&o, model.StateWorking)
		}
	case "system":
		if m.Subtype == "init" {
			p.setSession(&o, m.SessionID)
			if !p.busy {
				p.setState(&o, model.StateIdle)
			}
		}
	case "assistant":
		p.assistant(&o, m)
	case "user":
		p.toolResults(&o, m)
	case "result":
		p.result(&o, m)
	case "rate_limit_event":
		// Only a limit that stopped the agent is reported (the accounts page marks it at once);
		// "allowed" comes with every turn.
		if rl := m.RateLimitInfo; rl != nil && rl.Status == "rejected" {
			ev := runner.RateLimit{Status: rl.Status, Type: rl.RateLimitType}
			if rl.ResetsAt != nil {
				ev.ResetsAt = int64(*rl.ResetsAt)
			}
			o.emit(runner.Event{Kind: runner.EvRateLimit, RateLimit: &ev})
		}
	default:
		p.log.Debug("claude: ignored event", "type", m.Type, "subtype", m.Subtype)
	}
	return o
}

func (p *parser) controlResponse(o *out, m wireMsg) {
	var r wireControlResponse
	_ = json.Unmarshal(m.Response, &r)
	if r.RequestID != p.initID {
		return
	}
	if r.Subtype == "error" {
		o.entry(model.TimelineEntry{Kind: model.EntryError, Text: "claude initialize failed: " + r.Error, IsError: true})
		return
	}
	// claude sends system/init only with the first turn; the initialize reply is "ready".
	if !p.busy {
		p.setState(o, model.StateIdle)
	}
}

func (p *parser) controlRequest(o *out, m wireMsg) {
	var r wireControlRequest
	_ = json.Unmarshal(m.Request, &r)
	if r.Subtype != "can_use_tool" {
		// claude waits for an answer to every control request; never leave one hanging.
		p.log.Debug("claude: unsupported control request", "subtype", r.Subtype)
		o.replies = append(o.replies, controlError(m.RequestID, "unsupported control request: "+r.Subtype))
		return
	}
	input := r.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	p.pending[m.RequestID] = input
	o.emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{
		ID: m.RequestID, Tool: r.ToolName, Input: input, Description: r.Description, Suggestions: r.Suggestions,
	}})
	p.setState(o, model.StateWaiting)
}

func (p *parser) assistant(o *out, m wireMsg) {
	blocks := contentBlocks(m.Message)
	if len(blocks) == 0 {
		return
	}
	p.activity(o)
	meta := parentMeta(m.ParentToolUseID)
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				o.entry(model.TimelineEntry{Kind: model.EntryAssistant, Text: b.Text, IsError: m.Error != "", Meta: meta})
			}
		case "thinking":
			if b.Thinking != "" {
				o.entry(model.TimelineEntry{Kind: model.EntryThinking, Text: b.Thinking, Meta: meta})
			}
		case "tool_use":
			o.entry(model.TimelineEntry{Kind: model.EntryToolUse, Tool: b.Name, ToolUseID: b.ID, Input: b.Input, Meta: meta})
		}
	}
}

func (p *parser) toolResults(o *out, m wireMsg) {
	meta := parentMeta(m.ParentToolUseID)
	for _, b := range contentBlocks(m.Message) {
		if b.Type != "tool_result" {
			continue // our own user text echoed back, or images
		}
		p.activity(o)
		text := flatten(b.Content)
		e := model.TimelineEntry{Kind: model.EntryToolResult, ToolUseID: b.ToolUseID, IsError: b.IsError, Meta: meta}
		if len(text) > maxToolResult {
			e.Meta = withKey(meta, "truncated_from", len(text))
			text = truncate(text, maxToolResult)
		}
		e.Text = text
		o.entry(e)
	}
}

func (p *parser) result(o *out, m wireMsg) {
	p.setSession(o, m.SessionID)
	text := m.Result
	if text == "" && m.IsError {
		text = m.Subtype
	}
	meta := map[string]any{"subtype": m.Subtype}
	ev := runner.Event{Kind: runner.EvUsage}
	if m.NumTurns != nil {
		meta["num_turns"] = *m.NumTurns
		ev.Turns = *m.NumTurns
	}
	if m.TotalCostUSD != nil {
		meta["total_cost_usd"] = *m.TotalCostUSD
		ev.CostUSD = *m.TotalCostUSD
	}
	if m.DurationMS != nil {
		meta["duration_ms"] = *m.DurationMS
	}
	o.entry(model.TimelineEntry{Kind: model.EntryResult, Text: text, IsError: m.IsError, Meta: meta})
	o.emit(ev)
	p.busy = false
	clear(p.pending)
	p.setState(o, model.StateIdle)
}

func contentBlocks(c *wireContent) []wireBlock {
	if c == nil || len(c.Content) == 0 || c.Content[0] != '[' {
		return nil
	}
	var blocks []wireBlock
	_ = json.Unmarshal(c.Content, &blocks)
	return blocks
}

// flatten turns tool_result content (a string, or an array of text/image blocks) into text.
func flatten(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []wireBlock
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "image":
				parts = append(parts, "[image]")
			default:
				parts = append(parts, "["+b.Type+"]")
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

func parentMeta(parent string) map[string]any {
	if parent == "" {
		return nil
	}
	return map[string]any{"parent_tool_use_id": parent}
}

func withKey(m map[string]any, k string, v any) map[string]any {
	out := map[string]any{k: v}
	for mk, mv := range m {
		out[mk] = mv
	}
	return out
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
