package claude

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// transcriptRecord is the part of a line of Claude Code's transcript
// (~/.claude/projects/<folder>/<session>.jsonl) that tmx reads. Claude's TUI writes it as the
// conversation goes; besides user and assistant messages it holds many bookkeeping records
// (queue operations, attachments, request shapes, cost, titles, …), all ignored.
type transcriptRecord struct {
	Type            string       `json:"type"`
	Subtype         string       `json:"subtype"`
	IsMeta          bool         `json:"isMeta"`
	IsSidechain     bool         `json:"isSidechain"`
	ParentToolUseID string       `json:"parentToolUseId"`
	Message         *wireContent `json:"message"`
}

// TranscriptLine reads one transcript line (DESIGN.md "Switching live"). The assistant's text,
// thinking and tool calls and the tool results become timeline entries exactly as in chat mode;
// prompt is the text of a message someone sent ("" otherwise: tool results, text Claude adds
// itself, side chains of sub-agents, bookkeeping). A compaction gives a system entry.
func TranscriptLine(b []byte) (entries []model.TimelineEntry, prompt string) {
	var r transcriptRecord
	if json.Unmarshal(b, &r) != nil || r.IsSidechain {
		return nil, ""
	}
	p := newParser("", slog.New(slog.DiscardHandler))
	var o out
	m := wireMsg{Type: r.Type, Message: r.Message}
	switch r.Type {
	case "assistant":
		p.assistant(&o, m)
	case "user":
		if hasToolResult(r.Message) {
			p.toolResults(&o, m)
		} else if !r.IsMeta {
			prompt = userText(r.Message)
			if strings.HasPrefix(prompt, "[Request interrupted by user") {
				return []model.TimelineEntry{{Kind: model.EntrySystem, Text: "Interrupted in the screen"}}, ""
			}
		}
	case "system":
		if r.Subtype == "compact_boundary" {
			return []model.TimelineEntry{{Kind: model.EntrySystem, Text: "Claude compacted the conversation"}}, ""
		}
	}
	for _, ev := range o.events {
		if ev.Kind == runner.EvTimeline && ev.Entry != nil {
			entries = append(entries, *ev.Entry)
		}
	}
	return entries, prompt
}

func hasToolResult(c *wireContent) bool {
	for _, b := range contentBlocks(c) {
		if b.Type == "tool_result" {
			return true
		}
	}
	return false
}

// userText is a user message's text: a string, or its text blocks joined. Wrappers Claude
// puts around slash commands, shell escapes and reminders are not something a person wrote.
func userText(c *wireContent) string {
	if c == nil || len(c.Content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(c.Content, &s) != nil {
		var parts []string
		for _, b := range contentBlocks(c) {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		s = strings.Join(parts, "\n")
	}
	s = strings.TrimSpace(s)
	for _, w := range []string{"<command-", "<local-command-", "<bash-", "<system-reminder>", "<user-memory-input>", "Caveat: "} {
		if strings.HasPrefix(s, w) {
			return ""
		}
	}
	return s
}
