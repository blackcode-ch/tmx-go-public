// Package runner starts and drives agent processes. The fleet package talks only to this
// interface; implementations live in runner/claude (chat mode), runner/term (terminal mode)
// and runner/fake (tests). See DESIGN.md "Agent runtime".
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// ErrApprovalPending is returned by Process.Write when a non-owner write is refused because an
// owner-only approval is pending on the process (terminal mode only). The check and the write
// happen under the same lock the approval detector uses, so a transition to pending cannot land
// between the check and the write (see internal/runner/term's watcher.pending/process.mu).
var ErrApprovalPending = errors.New("a menu or an approval is waiting for the owner; only the owner can type now")

// ErrOwnerOnly is returned by Process.Write for a non-owner write only the owner may make:
// anything typed into a shell (it would run as a command, with no guard and no approval), and
// anything but an interrupt key (NonOwnerKey) typed into a harness's TUI, whose own keys and
// commands change its permissions and answer its menus (DESIGN.md "Fleet control").
var ErrOwnerOnly = errors.New("only the owner can type that into this terminal")

// NonOwnerKey reports whether b is raw input a non-owner (a head or lead driving its worker)
// may write into a harness's TUI: one Esc or one Ctrl-C, which interrupt and cancel. Every
// other key can answer a menu, cycle a permission mode or recall a command from history.
func NonOwnerKey(b []byte) bool {
	return len(b) == 1 && (b[0] == 0x1b || b[0] == 0x03)
}

// PlainText is text as it may be pasted into a TUI: CR LF and lone CRs become LF, and every
// other control character (C0 but tab and LF, DEL, C1) and invalid UTF-8 is dropped. An Esc
// inside a bracketed paste could end it early (ESC [201~) and turn the rest into keystrokes;
// mail and an agent's typed text are pasted, never typed as keys.
func PlainText(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\r\n", "\n"), "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

// Spec is everything needed to start one agent process.
type Spec struct {
	AgentID         string
	Name            string
	Harness         model.Harness
	Mode            model.Mode
	Command         string // executable path; "" = look up the harness on PATH
	Model           string // "" = harness default
	Cwd             string
	Env             []string // extra KEY=VALUE pairs (TMXG_URL, TMXG_AGENT_ID, TMXG_AGENT_TOKEN, ...)
	SessionID       string   // conversation to resume; "" = new conversation
	PermissionMode  string   // claude --permission-mode
	CodexFullAccess bool
	AgyFullAccess   bool
	// CodexWindowsSandbox: Codex's own sandbox on Windows. Nothing sets it today (there is no
	// config key or API field for it): Windows Codex runs unsandboxed, see harness.CodexPolicy.
	CodexWindowsSandbox bool
	// CodexGuardHome/Executable are set only by the daemon's Windows parity
	// adapter. Runners must prepare and pin the configuration before spawning.
	CodexGuardHome       string
	CodexGuardExecutable string
	// GuardStates returns the owner's current state per guard rule (block | hold | allow); the
	// Codex parser calls it on every check, so a flip applies to running agents at once.
	GuardStates     func() map[string]string
	MCPConfigPath   string   // claude --mcp-config (written by fleet)
	SettingsPath    string   // claude --settings: the guard's PreToolUse hook (written by fleet)
	AllowedTools    []string // claude --allowedTools
	DisallowedTools []string // claude --disallowedTools
	SystemPrompt    string   // claude --append-system-prompt
	InitialPrompt   string   // agy: first turn passed with -i, not typed a second time
	// AddDirs are extra folders the agent may read and edit (claude --add-dir), e.g. its uploads.
	AddDirs    []string
	ExtraArgs  []string
	Cols, Rows int // terminal mode size; 0 = 120x32

	// MCP is the tmx MCP server for harnesses configured on the command line or by env
	// (codex, opencode); its env values are in Env, only the names are passed (EnvKeys).
	// SystemPrompt goes to codex as developer instructions.
	MCP *MCPServer

	LowPriority bool // start the process tree at lower CPU and disk I/O priority (every harness)

	// OnStartWait, if set, hears why a claude start waits in the daemon's claude start queue
	// (claude.WaitReason; e.g. a Claude sign-in holds it) and "" once it may start. Called from
	// Start's goroutine, holding no lock of the runner's.
	OnStartWait func(reason string)

	// HostDir and HostExe: the agent's program runs in a session host of its own, `HostExe
	// host`, which keeps its state in HostDir and outlives the daemon (runner/host): terminal
	// mode, and claude's chat mode. Empty: the daemon's own child, ended with the daemon.
	// HostToken is the agent's tmx credential, kept by the host for the next daemon
	// (Reattacher).
	HostDir, HostExe, HostToken string
}

// ErrNoHost: there is no session host left to reattach to (Reattacher): it is proven gone, so
// the agent may be started again.
var ErrNoHost = errors.New("no session host")

// ErrHostUnknown: whether the agent's session host still runs could not be told (the process
// list could not be read, or it did not answer). The agent must NOT be started a second time
// on the same conversation: check again later.
var ErrHostUnknown = errors.New("the session host could not be checked")

// Reattacher is implemented by runners whose processes can outlive the daemon: Reattach
// connects to the one an earlier daemon left running for spec, and returns the credential it
// was started with. An error (ErrNoHost: there is none) means: start it instead.
type Reattacher interface {
	Reattach(ctx context.Context, spec Spec) (Process, string, error)
}

// Detacher is implemented by processes that can be left running when the daemon stops:
// Detach ends the daemon's view (Events closes without EvExit) and reports whether it did.
type Detacher interface {
	Detach() bool
}

type EventKind string

const (
	EvState    EventKind = "state"    // State changed
	EvTimeline EventKind = "timeline" // Entry appended (Seq/AgentID/At left 0: the fleet assigns them)
	EvApproval EventKind = "approval" // the harness asks permission; answer with Process.Answer
	// EvApprovalCancelled: the harness withdrew a request (Approval.ID set); the fleet expires it.
	EvApprovalCancelled EventKind = "approval_cancelled"
	EvSession           EventKind = "session" // SessionID known (new or confirmed)
	EvUsage             EventKind = "usage"   // CostUSD / Turns totals for the conversation so far
	EvOutput            EventKind = "output"  // terminal bytes
	EvExit              EventKind = "exit"    // process ended; the channel is closed right after
)

// EvRateLimit: the harness says a limit of the owner's plan stopped it (RateLimit set; Claude's
// rate_limit_event with status "rejected").
const EvRateLimit EventKind = "rate_limit"

type Event struct {
	MessageIDs []string // EvMailDelivery: local mail carried by this process
	Delivery   string   // submitted (paste+Enter written), or unconfirmed
	Kind       EventKind
	State      model.AgentState
	Entry      *model.TimelineEntry
	Approval   *ApprovalRequest
	SessionID  string
	CostUSD    float64
	Turns      int
	Output     []byte
	ExitCode   int
	Err        error
	RateLimit  *RateLimit
}

const EvMailDelivery EventKind = "mail_delivery"

// MailSender reports the terminal write effect asynchronously; accepting a
// runner queue entry is not evidence of a submitted turn or recipient pickup.
type MailSender interface {
	SendMail(text string, ids []string) error
}

// RateLimit is a harness's report that a limit of the owner's plan was hit.
type RateLimit struct {
	Status   string // the harness's word: rejected, …
	Type     string // which window: five_hour, seven_day, seven_day_opus, … ("" = unknown)
	ResetsAt int64  // Unix seconds; 0 = unknown
}

// MCPServer is a stdio MCP server command.
type MCPServer struct {
	Command string
	Args    []string
	EnvKeys []string // variables (set in Spec.Env) the server needs
}

// ApprovalRequest is a permission request from the harness (claude can_use_tool, a codex
// approval request, a menu on a terminal agent's screen) or a question (Kind question).
type ApprovalRequest struct {
	ID          string             // the harness's request id; pass it back to Answer
	Kind        model.ApprovalKind // "" = permission
	Tool        string
	Input       json.RawMessage
	Description string
	Suggestions json.RawMessage
	Question    string   // questions and terminal menus: the text asked
	Options     []string // choices, if any
}

type Decision struct {
	Allow        bool
	Message      string          // shown to the agent on deny
	UpdatedInput json.RawMessage // nil = the original input
	// Answer picks an option (its label or number) or answers a question; "" = Allow/deny.
	Answer string
}

// Process is one running agent.
type Process interface {
	// Events delivers everything the process does, in order. Closed after EvExit.
	Events() <-chan Event
	// Send gives the agent a user turn (chat) or types text followed by Enter (terminal).
	Send(text string) error
	// Write sends raw input bytes (terminal mode only). owner must be true for the human
	// owner's own input. A non-owner write (a head/lead pressing a key in its worker) may only
	// be a NonOwnerKey into a harness's TUI (else ErrOwnerOnly; always for a shell), and is
	// refused with ErrApprovalPending while a menu is on the screen or an approval is pending.
	// A non-owner's text goes through Send instead, like mail.
	Write(b []byte, owner bool) error
	// Resize changes the terminal size (terminal mode only; no-op in chat mode).
	Resize(cols, rows int) error
	// Answer resolves an ApprovalRequest.
	Answer(requestID string, d Decision) error
	// Interrupt stops the current turn without ending the process.
	Interrupt() error
	// Stop ends the process: graceful first, killed when ctx expires.
	Stop(ctx context.Context) error
	// Replay returns the recent terminal output (terminal mode; nil in chat mode).
	Replay() []byte
	PID() int
}

// Runner starts processes of one mode.
type Runner interface {
	Start(ctx context.Context, spec Spec) (Process, error)
}

// Supporter is implemented by runners that serve only some harnesses.
type Supporter interface {
	Supports(h model.Harness) bool
}

// ByHarness is a Runner that dispatches to one runner per harness (chat mode: claude's
// stream-json, codex's app-server). An empty Spec.Harness means claude.
type ByHarness map[model.Harness]Runner

func (b ByHarness) Supports(h model.Harness) bool {
	if h == "" {
		h = model.HarnessClaude
	}
	_, ok := b[h]
	return ok
}

// Reattach dispatches to the harness's runner, if it can reattach (ErrNoHost otherwise).
func (b ByHarness) Reattach(ctx context.Context, spec Spec) (Process, string, error) {
	h := spec.Harness
	if h == "" {
		h = model.HarnessClaude
	}
	if r, ok := b[h].(Reattacher); ok {
		return r.Reattach(ctx, spec)
	}
	return nil, "", ErrNoHost
}

func (b ByHarness) Start(ctx context.Context, spec Spec) (Process, error) {
	h := spec.Harness
	if h == "" {
		h = model.HarnessClaude
	}
	r, ok := b[h]
	if !ok {
		return nil, fmt.Errorf("no %s runner for harness %q", spec.Mode, h)
	}
	return r.Start(ctx, spec)
}
