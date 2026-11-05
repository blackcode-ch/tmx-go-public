package claude

import (
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestTranscriptLine(t *testing.T) {
	cases := []struct {
		line   string
		kinds  []model.EntryKind
		prompt string
	}{
		{`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"hi"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`,
			[]model.EntryKind{model.EntryThinking, model.EntryAssistant, model.EntryToolUse}, ""},
		{`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a\nb"}]}}`,
			[]model.EntryKind{model.EntryToolResult}, ""},
		{`{"type":"user","message":{"role":"user","content":"what was the word?"}}`, nil, "what was the word?"},
		{`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"look"},{"type":"image"}]}}`, nil, "look"},
		{`{"type":"user","isMeta":true,"message":{"role":"user","content":"Caveat: injected"}}`, nil, ""},
		{`{"type":"user","message":{"role":"user","content":"<command-name>/model</command-name>"}}`, nil, ""},
		{`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`,
			[]model.EntryKind{model.EntrySystem}, ""},
		{`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"sub-agent"}]}}`, nil, ""},
		{`{"type":"system","subtype":"compact_boundary"}`, []model.EntryKind{model.EntrySystem}, ""},
		{`{"type":"queue-operation","operation":"enqueue","content":"x"}`, nil, ""},
		{`{"type":"cost-state","totalCostUSD":1.2}`, nil, ""},
		{`not json`, nil, ""},
	}
	for _, c := range cases {
		entries, prompt := TranscriptLine([]byte(c.line))
		if prompt != c.prompt || len(entries) != len(c.kinds) {
			t.Errorf("%s: prompt %q entries %+v", c.line, prompt, entries)
			continue
		}
		for i, e := range entries {
			if e.Kind != c.kinds[i] {
				t.Errorf("%s: entry %d is %s, want %s", c.line, i, e.Kind, c.kinds[i])
			}
		}
	}
	e, _ := TranscriptLine([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t9","name":"Read","input":{"file_path":"/x"}}]}}`))
	if e[0].Tool != "Read" || e[0].ToolUseID != "t9" || string(e[0].Input) != `{"file_path":"/x"}` {
		t.Fatalf("%+v", e[0])
	}
}
