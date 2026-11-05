// Package companiontest is a fake of Companion's local agent server for tests: the A2A v1
// HTTP+JSON façade as Companion v0.17.1 implements it (api/a2a.py, core/auth_middleware.py):
// agent card, /local/agents/me, message:send (returnImmediately), tasks/{id}, :cancel, the
// task list, the agent-key header, one conversation per key that takes one message at a time
// (409 CONVERSATION_BUSY), INPUT_REQUIRED with an ask_caller interrupt, and the error bodies.
// Tests drive each task's outcome (Complete, Ask, Fail), or let a responder answer every
// message as it arrives (Respond: a reply, or a question).
package companiontest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const header = "X-Companion-Agent-Token"

// Fake is a running fake Companion.
type Fake struct {
	URL   string
	Token string

	mu       sync.Mutex
	srv      *httptest.Server
	tasks    map[string]*Task
	order    []string
	n        int
	Sent     []Sent // every message:send that reached it, accepted or not
	busy     int    // answer this many sends with 409 CONVERSATION_BUSY
	refuse   string // answer sends with 403 PERMISSION_DENIED and this message
	down     bool   // its list of agents is unreadable: every keyed request gets 503
	respond  func(text string) Response
	Requests []string
}

// Response is what the fake does with a message it took (one that starts a task, or an answer
// to its question), as Companion's run would end: Reply completes the task with that text,
// Question asks the caller (INPUT_REQUIRED, ask_caller), Fail fails it. All empty: it keeps
// working (the test drives it with Complete, Ask, Fail).
type Response struct {
	Reply, Question, Fail string
}

// Respond makes the fake answer every message it takes with fn's response (nil: tests drive the
// tasks). fn runs with the fake locked: it must not call the fake.
func (f *Fake) Respond(fn func(text string) Response) { f.mu.Lock(); f.respond = fn; f.mu.Unlock() }

// respondLocked applies the responder to t, which just took text.
func (f *Fake) respondLocked(t *Task, text string) {
	if f.respond == nil {
		return
	}
	switch r := f.respond(text); {
	case r.Question != "":
		t.State, t.Question = "TASK_STATE_INPUT_REQUIRED", r.Question
	case r.Fail != "":
		t.State, t.Error = "TASK_STATE_FAILED", r.Fail
	case r.Reply != "":
		t.State, t.Result = "TASK_STATE_COMPLETED", r.Reply
	}
}

// SetToken makes the fake accept only token (the owner rotated or replaced tmx's key).
func (f *Fake) SetToken(token string) { f.mu.Lock(); f.Token = token; f.mu.Unlock() }

// RegistryDown makes Companion unable to check any key (503 from its auth layer).
func (f *Fake) RegistryDown(down bool) { f.mu.Lock(); f.down = down; f.mu.Unlock() }

// Sent is one message:send.
type Sent struct {
	Text   string
	TaskID string
	Agent  string // X-Companion-Agent-Client
}

// Task is a fake task.
type Task struct {
	ID       string
	Context  string
	State    string
	Texts    []string // user messages (the first started it)
	Question string
	Result   string
	Error    string
}

// New starts a fake Companion that accepts token.
func New(t testing.TB, token string) *Fake {
	f := &Fake{Token: token, tasks: map[string]*Task{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	f.URL = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

// Close stops it (Companion quit).
func (f *Fake) Close() { f.srv.Close() }

// Busy makes the next n sends answer 409 CONVERSATION_BUSY.
func (f *Fake) Busy(n int) { f.mu.Lock(); f.busy = n; f.mu.Unlock() }

// Refuse makes every send answer 403 PERMISSION_DENIED with msg ("" = accept again).
func (f *Fake) Refuse(msg string) { f.mu.Lock(); f.refuse = msg; f.mu.Unlock() }

// Complete, Ask and Fail set a task's outcome (polled by tmx).
func (f *Fake) Complete(id, result string) {
	f.set(id, func(t *Task) { t.State, t.Result = "TASK_STATE_COMPLETED", result })
}
func (f *Fake) Ask(id, q string) {
	f.set(id, func(t *Task) { t.State, t.Question = "TASK_STATE_INPUT_REQUIRED", q })
}
func (f *Fake) Fail(id, msg string) {
	f.set(id, func(t *Task) { t.State, t.Error = "TASK_STATE_FAILED", msg })
}

func (f *Fake) set(id string, fn func(*Task)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t := f.tasks[id]; t != nil {
		fn(t)
	}
}

// Snapshot returns a copy of a task.
func (f *Fake) Snapshot(id string) Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t := f.tasks[id]; t != nil {
		c := *t
		c.Texts = append([]string(nil), t.Texts...)
		return c
	}
	return Task{}
}

// SentCount is how many sends reached it.
func (f *Fake) SentCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.Sent) }

// LastTask is the newest task's id (waits up to 5 s for one).
func (f *Fake) LastTask(t testing.TB) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if len(f.order) > 0 {
			id := f.order[len(f.order)-1]
			f.mu.Unlock()
			return id
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no task reached the fake Companion")
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/a2a+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func a2aErr(w http.ResponseWriter, status int, reason, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "details": []any{
		map[string]any{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": reason, "domain": "a2a-protocol.org"}}}})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.Requests = append(f.Requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	if r.Header.Get("Origin") != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"detail": "origin not allowed"})
		return
	}
	if r.URL.Path == "/.well-known/agent-card.json" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"name": "Companion", "version": "0.17.1-fake", "protocolVersion": "1.0",
			"supportedInterfaces": []any{map[string]any{"url": f.URL + "/a2a/v1", "protocolBinding": "HTTP+JSON", "protocolVersion": "1.0"}},
			"capabilities":        map[string]any{"streaming": true, "pushNotifications": false}, "securitySchemes": map[string]any{}})
		return
	}
	f.mu.Lock()
	token, down := f.Token, f.down
	f.mu.Unlock()
	if down {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"detail": "Companion cannot open its list of connected agents right now, so it cannot check your key; retry shortly."})
		return
	}
	if r.Header.Get(header) != token {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "Companion does not recognise this key, or it has been stopped — ask the person to connect you again"})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/local/agents/me":
		writeJSON(w, http.StatusOK, map[string]any{"agent_id": "ag-1", "label": "tmx", "slug": "tmx", "uid": "u1"})
	case r.Method == http.MethodPost && r.URL.Path == "/a2a/v1/message:send":
		f.send(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/a2a/v1/tasks":
		f.mu.Lock()
		var list []any
		for i := len(f.order) - 1; i >= 0; i-- {
			list = append(list, f.taskJSON(f.tasks[f.order[i]]))
		}
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"tasks": list, "totalSize": len(list), "pageSize": 10})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/a2a/v1/tasks/"):
		id := strings.TrimPrefix(r.URL.Path, "/a2a/v1/tasks/")
		f.mu.Lock()
		t := f.tasks[id]
		var body any
		if t != nil {
			body = f.taskJSON(t)
		}
		f.mu.Unlock()
		if t == nil {
			a2aErr(w, http.StatusNotFound, "TASK_NOT_FOUND", fmt.Sprintf("Task '%s' not found", id))
			return
		}
		writeJSON(w, http.StatusOK, body) // not wrapped
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/a2a/v1/tasks/") && strings.HasSuffix(r.URL.Path, ":cancel"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/a2a/v1/tasks/"), ":cancel")
		f.mu.Lock()
		t := f.tasks[id]
		if t == nil {
			f.mu.Unlock()
			a2aErr(w, http.StatusNotFound, "TASK_NOT_FOUND", fmt.Sprintf("Task '%s' not found", id))
			return
		}
		if terminal(t.State) {
			f.mu.Unlock()
			a2aErr(w, http.StatusBadRequest, "TASK_NOT_CANCELABLE", "Task is "+t.State)
			return
		}
		t.State = "TASK_STATE_CANCELED"
		body := f.taskJSON(t)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, body)
	default:
		writeJSON(w, http.StatusForbidden, map[string]string{"detail": "this key is for a program talking to Companion; it cannot manage Companion — the person does that in People & Agents"})
	}
}

func terminal(s string) bool {
	return s == "TASK_STATE_COMPLETED" || s == "TASK_STATE_FAILED" || s == "TASK_STATE_CANCELED"
}

func (f *Fake) send(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message struct {
			MessageID string `json:"messageId"`
			Role      string `json:"role"`
			Parts     []struct {
				Text string `json:"text"`
			} `json:"parts"`
			TaskID    string `json:"taskId"`
			ContextID string `json:"contextId"`
		} `json:"message"`
		Configuration struct {
			ReturnImmediately bool `json:"returnImmediately"`
		} `json:"configuration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		a2aErr(w, http.StatusBadRequest, "INVALID_PARAMS", "request body is not valid JSON")
		return
	}
	var texts []string
	for _, p := range body.Message.Parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	text := strings.Join(texts, "\n")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Sent = append(f.Sent, Sent{Text: text, TaskID: body.Message.TaskID, Agent: r.Header.Get("X-Companion-Agent-Client")})
	if f.refuse != "" {
		a2aErr(w, http.StatusForbidden, "PERMISSION_DENIED", f.refuse)
		return
	}
	if body.Message.ContextID != "" && body.Message.ContextID != "ctx-tmx" {
		a2aErr(w, http.StatusBadRequest, "INVALID_PARAMS", "That is not your conversation with this Companion")
		return
	}
	if body.Message.TaskID != "" { // an answer to a question
		t := f.tasks[body.Message.TaskID]
		if t == nil {
			a2aErr(w, http.StatusNotFound, "TASK_NOT_FOUND", fmt.Sprintf("Task '%s' not found", body.Message.TaskID))
			return
		}
		if t.State != "TASK_STATE_INPUT_REQUIRED" {
			a2aErr(w, http.StatusBadRequest, "INVALID_PARAMS", "Task is "+t.State+"; only INPUT_REQUIRED tasks accept a continuation message")
			return
		}
		t.Texts = append(t.Texts, text)
		t.State, t.Question = "TASK_STATE_WORKING", ""
		writeJSON(w, http.StatusOK, map[string]any{"task": f.taskJSON(t)})
		f.respondLocked(t, text)
		return
	}
	if f.busy > 0 {
		f.busy--
		a2aErr(w, http.StatusConflict, "CONVERSATION_BUSY", "Companion is still busy in this conversation (job x); nothing was sent — try again when it finishes")
		return
	}
	for _, id := range f.order { // one message at a time per conversation
		if !terminal(f.tasks[id].State) {
			a2aErr(w, http.StatusConflict, "CONVERSATION_BUSY", "Companion is still busy in this conversation (job "+id+"); nothing was sent")
			return
		}
	}
	f.n++
	t := &Task{ID: fmt.Sprintf("task-%d", f.n), Context: "ctx-tmx", State: "TASK_STATE_SUBMITTED", Texts: []string{text}}
	f.tasks[t.ID] = t
	f.order = append(f.order, t.ID)
	writeJSON(w, http.StatusOK, map[string]any{"task": f.taskJSON(t)})
	t.State = "TASK_STATE_WORKING"
	f.respondLocked(t, text)
}

// taskJSON is Companion's _task_json shape.
func (f *Fake) taskJSON(t *Task) map[string]any {
	status := map[string]any{"state": t.State, "timestamp": time.Now().UTC().Format(time.RFC3339)}
	msg := func(parts ...any) map[string]any {
		return map[string]any{"messageId": "msg-1", "contextId": t.Context, "taskId": t.ID, "role": "ROLE_AGENT", "parts": parts}
	}
	switch t.State {
	case "TASK_STATE_INPUT_REQUIRED":
		status["message"] = msg(map[string]any{"text": "The agent needs your input to continue."},
			map[string]any{"data": map[string]any{"kind": "ask_caller", "question": t.Question}})
	case "TASK_STATE_FAILED":
		status["message"] = msg(map[string]any{"text": t.Error})
	}
	var history []any
	for i, tx := range t.Texts {
		history = append(history, map[string]any{"messageId": fmt.Sprintf("r%d-user", i), "contextId": t.Context, "taskId": t.ID,
			"role": "ROLE_USER", "parts": []any{map[string]any{"text": tx}}})
	}
	out := map[string]any{"id": t.ID, "contextId": t.Context, "status": status}
	if terminal(t.State) && t.Result != "" {
		history = append(history, map[string]any{"messageId": "r-agent", "contextId": t.Context, "taskId": t.ID, "role": "ROLE_AGENT",
			"parts": []any{map[string]any{"text": t.Result}}})
	}
	out["history"] = history
	if t.State == "TASK_STATE_COMPLETED" && t.Result != "" {
		out["artifacts"] = []any{map[string]any{"artifactId": "run-answer", "parts": []any{map[string]any{"text": t.Result}}}}
	}
	return out
}
