// Package companion links tmx with Companion, the owner's desktop agent app (DESIGN.md
// "Companion"): the A2A client tmx hands tasks to Companion with, discovery of Companion's
// local agent server, the effects classification and rendering of a task, the install into
// Companion's MCP/skill folders, and the service that sends, polls and reports tasks.
//
// Companion's interface (read from its code, v0.17.1): an A2A v1 "HTTP+JSON" façade on its
// local agent server, 127.0.0.1:<port> (default 8010), authenticated with an agent key Companion
// makes for tmx when the owner clicks Connect on tmx's card (People & Agents; open.go), sent as
// X-Companion-Agent-Token. A key has one conversation with Companion, which takes one message
// at a time.
package companion

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// TokenHeader carries the agent key Companion issued (Companion: core/auth_middleware.py).
const TokenHeader = "X-Companion-Agent-Token"

// A2A task states Companion emits (api/a2a.py). It never emits REJECTED or AUTH_REQUIRED.
const (
	StateSubmitted     = "TASK_STATE_SUBMITTED"
	StateWorking       = "TASK_STATE_WORKING"
	StateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	StateCompleted     = "TASK_STATE_COMPLETED"
	StateFailed        = "TASK_STATE_FAILED"
	StateCanceled      = "TASK_STATE_CANCELED"
)

// Error reasons of Companion's A2A errors (google.rpc.ErrorInfo.reason) that tmx acts on.
const (
	ReasonBusy             = "CONVERSATION_BUSY"
	ReasonPermissionDenied = "PERMISSION_DENIED"
	ReasonTaskNotFound     = "TASK_NOT_FOUND"
)

// Client talks to Companion's local agent server. BaseURL must be loopback.
type Client struct {
	BaseURL string // e.g. http://127.0.0.1:8010
	Token   string // the agent key; "" for the public routes (health, agent card)
	Agent   string // X-Companion-Agent-Client, e.g. "tmx/0.1.0"
	HTTP    *http.Client
}

// Error is a failed call. Unreachable: no connection was made, so nothing was sent.
// Unconfirmed: the request may have reached Companion (timeout, 5xx): never resend blindly.
// Otherwise Companion answered Status (a 4xx: nothing was sent or run) with Reason and Msg.
// KeyRefused: Companion's auth layer does not accept tmx's key (unknown, removed, rotated, or
// another account signed in): a connection problem, not a refusal of the task.
type Error struct {
	Status      int
	Reason      string
	Msg         string
	Unreachable bool
	Unconfirmed bool
	KeyRefused  bool
}

func (e *Error) Error() string { return e.Msg }

// AsError returns err as an *Error (nil if it is not one).
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// CheckLoopback refuses any address that is not http on 127.0.0.1, ::1 or localhost: tmx
// never sends a task off this machine.
func CheckLoopback(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return "", fmt.Errorf("%q is not an http://127.0.0.1:<port> address", raw)
	}
	h := u.Hostname()
	if h != "127.0.0.1" && h != "::1" && h != "localhost" {
		return "", fmt.Errorf("Companion's address must be on this machine (127.0.0.1), not %q", h)
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("%q: give only http://127.0.0.1:<port>", raw)
	}
	// Do not resolve even localhost through DNS/hosts configuration.
	if h == "localhost" {
		port := u.Port()
		u.Host = "127.0.0.1"
		if port != "" {
			u.Host = net.JoinHostPort("127.0.0.1", port)
		}
	}
	return u.Scheme + "://" + u.Host, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) (result error) {
	defer func() {
		if e := AsError(result); e != nil {
			e.Msg = KeySafeText(e.Msg, c.Token, "Companion's answer was withheld because it contained key material")
		}
	}()
	base, err := CheckLoopback(c.BaseURL)
	if err != nil {
		return &Error{Msg: err.Error(), Unreachable: true}
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return err
	}
	// Companion's local-access check wants a loopback Host and no (or a loopback) Origin; Go
	// sets Host from the URL and sends no Origin.
	req.Header.Set("Accept", "application/a2a+json, application/json")
	req.Header.Set("A2A-Version", "1.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set(TokenHeader, c.Token)
	}
	if c.Agent != "" {
		req.Header.Set("X-Companion-Agent-Client", c.Agent)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
		if c.Token != "" {
			hc = verifiedClient()
		}
	}
	resp, err := NoRedirectClient(hc).Do(req)
	if err != nil {
		return transportError(err, method)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return &Error{Msg: "Companion's answer was cut off: " + err.Error(), Unconfirmed: method == http.MethodPost}
	}
	if resp.StatusCode >= 300 {
		return answerError(resp.StatusCode, b, method)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return &Error{Msg: "Companion's answer is not the JSON tmx expects: " + err.Error(), Unconfirmed: method == http.MethodPost}
		}
	}
	return nil
}

// transportError: a refused or unresolvable connection sent nothing; anything later may have.
func transportError(err error, method string) error {
	if errors.Is(err, ErrListenerIdentity) {
		return &Error{Status: http.StatusForbidden, Msg: ErrListenerIdentity.Error()}
	}
	var op *net.OpError
	if errors.Is(err, syscall.ECONNREFUSED) || errors.As(err, &op) && op.Op == "dial" {
		return &Error{Msg: "Companion is not running (nothing answers at its address)", Unreachable: true}
	}
	return &Error{Msg: "Companion did not answer in time: " + err.Error(), Unconfirmed: method == http.MethodPost,
		Unreachable: method != http.MethodPost}
}

// answerError reads Companion's error bodies: the A2A façade's {"error": {message, details:
// [{reason}]}} and the auth layer's {"detail": "..."}.
func answerError(status int, b []byte, method string) error {
	var a struct {
		Error *struct {
			Message string `json:"message"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
		Detail any `json:"detail"`
	}
	_ = json.Unmarshal(b, &a)
	e := &Error{Status: status}
	switch {
	case a.Error != nil:
		e.Msg = a.Error.Message
		for _, d := range a.Error.Details {
			if d.Reason != "" {
				e.Reason = d.Reason
				break
			}
		}
	case a.Detail != nil:
		if s, ok := a.Detail.(string); ok {
			e.Msg = s
		} else {
			d, _ := json.Marshal(a.Detail)
			e.Msg = string(d)
		}
	}
	if e.Msg == "" {
		e.Msg = fmt.Sprintf("Companion answered %d %s", status, http.StatusText(status))
	}
	if len(e.Msg) > 600 {
		e.Msg = e.Msg[:600] + "…"
	}
	// Companion's own rule: a 4xx means nothing was sent or run; a 5xx is unconfirmed.
	e.Unconfirmed = status >= 500 && method == http.MethodPost
	// Its auth layer answers before any route runs, as {"detail": ...} (the A2A façade's own
	// errors are {"error": ...}): 401 = the key is not accepted; 503 = it could not check the
	// key (its list of agents is unreadable). Either way nothing was sent or run.
	if a.Error == nil && a.Detail != nil {
		switch status {
		case http.StatusUnauthorized:
			e.KeyRefused = true
		case http.StatusServiceUnavailable:
			e.Unconfirmed, e.Unreachable = false, true
		}
	}
	return e
}

// Card is the part of Companion's agent card tmx reads.
type Card struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocolVersion"`
	Interfaces      []struct {
		URL             string `json:"url"`
		ProtocolBinding string `json:"protocolBinding"`
	} `json:"supportedInterfaces"`
}

// Card reads GET /.well-known/agent-card.json (no key needed).
func (c *Client) Card(ctx context.Context) (Card, error) {
	var card Card
	err := c.do(ctx, http.MethodGet, "/.well-known/agent-card.json", nil, &card)
	card.Version = KeySafeText(card.Version, c.Token, "withheld")
	card.Name = KeySafeText(card.Name, c.Token, "Companion")
	return card, err
}

// Me is GET /local/agents/me: who Companion takes this key for.
type Me struct {
	AgentID string `json:"agent_id"`
	Label   string `json:"label"`
	Slug    string `json:"slug"`
}

// Me checks the key (401 when Companion does not know it).
func (c *Client) Me(ctx context.Context) (Me, error) {
	var m Me
	err := c.do(ctx, http.MethodGet, "/local/agents/me", nil, &m)
	m.Label = KeySafeText(m.Label, c.Token, Name)
	return m, err
}

// Part is one A2A message part (text or data).
type Part struct {
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Message is an A2A message.
type Message struct {
	MessageID string `json:"messageId,omitempty"`
	Role      string `json:"role,omitempty"`
	Parts     []Part `json:"parts"`
	TaskID    string `json:"taskId,omitempty"`
	ContextID string `json:"contextId,omitempty"`
}

// Task is an A2A task as Companion returns it.
type Task struct {
	ID        string `json:"id"`
	ContextID string `json:"contextId"`
	Status    struct {
		State     string   `json:"state"`
		Timestamp string   `json:"timestamp"`
		Message   *Message `json:"message"`
	} `json:"status"`
	History   []Message `json:"history"`
	Artifacts []struct {
		ArtifactID string `json:"artifactId"`
		Parts      []Part `json:"parts"`
	} `json:"artifacts"`
}

// Send posts a new message (taskID "" = a new task; else the answer to that task's
// question) with returnImmediately: Companion answers at once and tmx polls.
func (c *Client) Send(ctx context.Context, text, taskID, contextID string) (Task, error) {
	body := map[string]any{
		"message": Message{MessageID: newMessageID(), Role: "ROLE_USER", Parts: []Part{{Text: text}},
			TaskID: taskID, ContextID: contextID},
		"configuration": map[string]any{"returnImmediately": true, "historyLength": 2},
	}
	var out struct {
		Task *Task `json:"task"`
	}
	if err := c.do(ctx, http.MethodPost, "/a2a/v1/message:send", body, &out); err != nil {
		return Task{}, err
	}
	if out.Task == nil || out.Task.ID == "" {
		return Task{}, &Error{Msg: "Companion's answer carried no task", Unconfirmed: true}
	}
	return *out.Task, nil
}

// Get reads a task (GET /a2a/v1/tasks/{id}: the task itself, not wrapped).
func (c *Client) Get(ctx context.Context, id string) (Task, error) {
	var t Task
	err := c.do(ctx, http.MethodGet, "/a2a/v1/tasks/"+url.PathEscape(id)+"?historyLength=4", nil, &t)
	return t, err
}

// Cancel asks Companion to cancel a task.
func (c *Client) Cancel(ctx context.Context, id string) (Task, error) {
	var t Task
	err := c.do(ctx, http.MethodPost, "/a2a/v1/tasks/"+url.PathEscape(id)+":cancel", nil, &t)
	return t, err
}

// Recent lists this key's newest tasks (to find a task whose send was unconfirmed).
func (c *Client) Recent(ctx context.Context, n int) ([]Task, error) {
	var out struct {
		Tasks []Task `json:"tasks"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/a2a/v1/tasks?pageSize=%d&historyLength=8&includeArtifacts=true", n), nil, &out)
	return out.Tasks, err
}

func newMessageID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "tmx-" + hex.EncodeToString(b)
}

// Question is what Companion asks when a task is INPUT_REQUIRED: the question of its
// aios__ask_caller interrupt ({"kind":"ask_caller","question":...}), else the message text.
func (t Task) Question() string {
	m := t.Status.Message
	if m == nil {
		return ""
	}
	var texts []string
	for _, p := range m.Parts {
		if len(p.Data) > 0 {
			var d struct {
				Question string `json:"question"`
			}
			if json.Unmarshal(p.Data, &d) == nil && strings.TrimSpace(d.Question) != "" {
				return strings.TrimSpace(d.Question)
			}
			texts = append(texts, string(p.Data))
		}
	}
	for _, p := range m.Parts {
		if p.Text != "" {
			texts = append([]string{p.Text}, texts...)
		}
	}
	return strings.TrimSpace(strings.Join(texts, "\n"))
}

// Result is the final answer: the artifacts' text, else the last agent message in history.
func (t Task) Result() string {
	var b []string
	for _, a := range t.Artifacts {
		for _, p := range a.Parts {
			if p.Text != "" {
				b = append(b, p.Text)
			}
		}
	}
	if len(b) > 0 {
		return strings.TrimSpace(strings.Join(b, "\n"))
	}
	for i := len(t.History) - 1; i >= 0; i-- {
		if t.History[i].Role == "ROLE_AGENT" {
			return strings.TrimSpace(partsText(t.History[i].Parts))
		}
	}
	return ""
}

// StatusText is the status message's text (a failure's error).
func (t Task) StatusText() string {
	if t.Status.Message == nil {
		return ""
	}
	return strings.TrimSpace(partsText(t.Status.Message.Parts))
}

// FirstUserText is the text of the message that started the task.
func (t Task) FirstUserText() string {
	for _, m := range t.History {
		if m.Role == "ROLE_USER" {
			return partsText(m.Parts)
		}
	}
	return ""
}

func partsText(ps []Part) string {
	var b []string
	for _, p := range ps {
		if p.Text != "" {
			b = append(b, p.Text)
		}
	}
	return strings.Join(b, "\n")
}
