package codex

import (
	"encoding/json"
)

// JSON-RPC over stdio, one object per line, without the "jsonrpc" member (codex app-server
// 0.163; schema from `codex app-server generate-ts`). See DESIGN.md "Codex app-server".

type wireMsg struct {
	ID     json.RawMessage `json:"id,omitempty"` // string or number; absent for notifications
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *wireError      `json:"error,omitempty"`
}

type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func mustLine(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only maps, strings and RawMessages from valid JSON
	}
	return append(b, '\n')
}

func request(id, method string, params any) []byte {
	return mustLine(map[string]any{"id": id, "method": method, "params": params})
}

func notification(method string) []byte { return mustLine(map[string]any{"method": method}) }

func response(id json.RawMessage, result any) []byte {
	return mustLine(map[string]any{"id": id, "result": result})
}

func errorResponse(id json.RawMessage, code int, msg string) []byte {
	return mustLine(map[string]any{"id": id, "error": map[string]any{"code": code, "message": msg}})
}

func textInput(text string) []map[string]any {
	return []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}}
}

// Notification and request payloads (only the fields tmx reads).

type wireThread struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type wireThreadResult struct {
	Thread wireThread `json:"thread"`
	Model  string     `json:"model"`
}

type wireTurn struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"` // completed | interrupted | failed | inProgress
	Error      *wireTurnE `json:"error"`
	DurationMS *float64   `json:"durationMs"`
}

type wireTurnE struct {
	Message string `json:"message"`
}

type wireTurnNote struct {
	ThreadID string   `json:"threadId"`
	Turn     wireTurn `json:"turn"`
}

type wireItemNote struct {
	ThreadID string   `json:"threadId"`
	TurnID   string   `json:"turnId"`
	Item     wireItem `json:"item"`
}

// wireItem is the union of ThreadItem variants tmx shows.
type wireItem struct {
	Type string `json:"type"`
	ID   string `json:"id"`

	Text    string   `json:"text"`    // agentMessage, plan
	Summary []string `json:"summary"` // reasoning
	Content any      `json:"content"` // reasoning: []string

	Command          string `json:"command"` // commandExecution
	Cwd              string `json:"cwd"`
	Status           string `json:"status"`
	AggregatedOutput string `json:"aggregatedOutput"`
	ExitCode         *int   `json:"exitCode"`

	Changes []json.RawMessage `json:"changes"` // fileChange: {path, kind, diff}

	Server    string          `json:"server"` // mcpToolCall
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    *struct {
		Content []json.RawMessage `json:"content"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`

	Query string `json:"query"` // webSearch
}

type wireErrorNote struct {
	Error     wireTurnE `json:"error"`
	WillRetry bool      `json:"willRetry"`
}

type wireMCPStatus struct {
	Name   string  `json:"name"`
	Status string  `json:"status"`
	Error  *string `json:"error"`
}

type wireResolved struct {
	RequestID json.RawMessage `json:"requestId"`
}

type wireCommandApproval struct {
	ItemID  string `json:"itemId"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	Reason  string `json:"reason"`
}

type wireFileApproval struct {
	ItemID    string `json:"itemId"`
	Reason    string `json:"reason"`
	GrantRoot string `json:"grantRoot"`
}

type wirePermissionsApproval struct {
	Cwd         string          `json:"cwd"`
	Reason      string          `json:"reason"`
	Permissions json.RawMessage `json:"permissions"`
}

type wireUserInput struct {
	Questions []struct {
		ID       string `json:"id"`
		Header   string `json:"header"`
		Question string `json:"question"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
}

type wireLegacyExec struct {
	Command []string `json:"command"`
	Cwd     string   `json:"cwd"`
	Reason  string   `json:"reason"`
}

type wireLegacyPatch struct {
	FileChanges json.RawMessage `json:"fileChanges"`
	Reason      string          `json:"reason"`
}
