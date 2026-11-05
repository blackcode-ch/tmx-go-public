// Package discover finds coding agents (claude, codex, opencode, pi) running on this machine
// that tmx did not start, and the conversation each is on, so the owner can adopt them.
// Read-only: the process table, command lines, working folders and transcript files.
// Ported from v2's discover.py; see DESIGN.md "Discover and adopt".
package discover

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Proc is one process of the table.
type Proc struct {
	PID, PPID int
	Started   time.Time // zero = unknown
	Args      []string  // best effort; nil = unknown
	Cwd       string    // "" = unknown
}

// Agent is an agent process tmx did not start (GET /api/discover).
type Agent struct {
	PID       int           `json:"pid"`
	Harness   model.Harness `json:"harness"`
	Cwd       string        `json:"cwd"`
	Command   string        `json:"command"`
	StartedAt int64         `json:"started_at"` // Unix ms; 0 = unknown
	// Interactive: a session a person works in (not -p / exec / an editor's or app's server).
	Interactive bool   `json:"interactive"`
	Adoptable   bool   `json:"adoptable"`
	Reason      string `json:"reason,omitempty"` // why it cannot be adopted
	// The conversation it is on, how that was found, and the folder's recent conversations
	// to pick from when it could not be told.
	ConversationID     string                 `json:"conversation_id,omitempty"`
	ConversationSource string                 `json:"conversation_source"`
	Candidates         []harness.Conversation `json:"candidates"`
}

// Scanner reads the machine. Zero fields use the real OS; tests replace them.
type Scanner struct {
	Home  string                          // the user's home (transcripts); "" = os.UserHomeDir
	List  func() ([]Proc, error)          // the process table
	Cwds  func(pids []int) map[int]string // working folders of these processes (when List has none)
	Self  int                             // this daemon's pid (its descendants are tmx's); 0 = os.Getpid
	Now   func() time.Time
	Alive func(pid int) bool
	// Terminate asks pid (only that process) to end: SIGTERM, or taskkill without /F.
	Terminate func(pid int) error
}

// New returns a Scanner on the real machine.
func New() *Scanner { return &Scanner{} }

func (s *Scanner) home() string {
	if s.Home != "" {
		return s.Home
	}
	h, _ := os.UserHomeDir()
	return h
}

func (s *Scanner) self() int {
	if s.Self != 0 {
		return s.Self
	}
	return os.Getpid()
}

func (s *Scanner) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scanner) list() ([]Proc, error) {
	if s.List != nil {
		return s.List()
	}
	return listProcs()
}

func (s *Scanner) cwds(pids []int) map[int]string {
	if s.Cwds != nil {
		return s.Cwds(pids)
	}
	return procCwds(pids)
}

// IsAlive reports whether pid still runs.
func (s *Scanner) IsAlive(pid int) bool {
	if s.Alive != nil {
		return s.Alive(pid)
	}
	return alive(pid)
}

// Stop asks pid to end (that process only).
func (s *Scanner) Stop(pid int) error {
	if s.Terminate != nil {
		return s.Terminate(pid)
	}
	return terminate(pid)
}

var scriptHints = map[string]model.Harness{
	"@openai/codex": model.HarnessCodex, "@anthropic-ai/claude-code": model.HarnessClaude,
	"opencode-ai": model.HarnessOpenCode, "pi-coding-agent": model.HarnessPi,
}

// classify: which harness a process is, if any.
func classify(p Proc) model.Harness {
	if len(p.Args) == 0 {
		return ""
	}
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(p.Args[0], `\`, "/")))
	base = strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".cmd")
	switch model.Harness(base) {
	case model.HarnessClaude, model.HarnessCodex, model.HarnessOpenCode, model.HarnessPi:
		return model.Harness(base)
	}
	if base == "node" || base == "bun" {
		joined := strings.ReplaceAll(strings.Join(p.Args, " "), `\`, "/")
		for hint, h := range scriptHints {
			if strings.Contains(joined, hint) {
				return h
			}
		}
	}
	return ""
}

var (
	// a tmx of any generation running agents: the Go daemon, v2's daemon and session hosts
	tmxManager    = regexp.MustCompile(`(^|[/\\])tmx(\.exe)?\s+(daemon|mcp)\b|-m\s+tmx(\.[a-z]+)?(\s|$)|(^|[/\\])tmxd(\.exe)?(\s|$)`)
	claudeConvArg = regexp.MustCompile(`(?:--resume|-r|--session-id)[\s=]+([0-9a-fA-F-]{16,})`)
	codexConvArg  = regexp.MustCompile(`\bresume\s+([0-9a-fA-F-]{16,})`)
	editorParent  = regexp.MustCompile(`(?i)Code Helper|Visual Studio Code|Cursor|Windsurf|Zed|JetBrains|idea|Claude\.app`)
)

// Scan lists the agent processes not started by this daemon or by any other tmx. managed
// are the PIDs of this daemon's agents (excluded with their descendants, like everything
// below the daemon itself).
func (s *Scanner) Scan(managed []int) ([]Agent, error) {
	procs, err := s.list()
	if err != nil {
		return nil, err
	}
	byPID := make(map[int]Proc, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	skip := map[int]bool{s.self(): true}
	for _, pid := range managed {
		if pid > 0 {
			skip[pid] = true
		}
	}
	// ancestors walks up the parent chain (a parent must have started before its child:
	// a reused pid is not a parent).
	ancestors := func(p Proc) []Proc {
		var out []Proc
		seen := map[int]bool{p.PID: true}
		for cur := p; len(out) < 40; {
			par, ok := byPID[cur.PPID]
			if !ok || seen[par.PID] || !bornBefore(par, cur) {
				break
			}
			seen[par.PID] = true
			out = append(out, par)
			cur = par
		}
		return out
	}
	found := map[int]*Agent{}
	var order []int
	for _, p := range procs {
		h := classify(p)
		if h == "" || skip[p.PID] {
			continue
		}
		chain := ancestors(p)
		ours := false
		for _, a := range chain {
			ours = ours || skip[a.PID]
		}
		if ours {
			continue
		}
		// Report the outermost process of the same agent (npm's node wrapper around codex).
		top := p
		for _, a := range chain {
			if classify(a) != h {
				break
			}
			top = a
		}
		if _, dup := found[top.PID]; dup {
			continue
		}
		joined := strings.Join(p.Args, " ")
		ag := &Agent{PID: top.PID, Harness: h, Cwd: firstNonEmpty(p.Cwd, top.Cwd), Command: clip(joined, 400),
			Candidates: []harness.Conversation{}}
		if !top.Started.IsZero() {
			ag.StartedAt = top.Started.UnixMilli()
		}
		ag.Interactive = interactive(p.Args)
		switch {
		case managedBy(chain) != "":
			ag.Reason = "run by " + managedBy(chain)
		case !ag.Interactive:
			ag.Reason = "not an interactive session (a script, an editor or an app runs it)"
		case editorChild(chain):
			ag.Reason = "run by an editor or app"
		}
		found[top.PID] = ag
		order = append(order, top.PID)
	}
	var missing []int
	for _, pid := range order {
		if found[pid].Cwd == "" {
			missing = append(missing, pid)
		}
	}
	if len(missing) > 0 {
		for pid, cwd := range s.cwds(missing) {
			if a, ok := found[pid]; ok && a.Cwd == "" {
				a.Cwd = cwd
			}
		}
	}
	sameFolder := map[string]int{}
	for _, pid := range order {
		a := found[pid]
		sameFolder[string(a.Harness)+"\x00"+folderKey(a.Cwd)]++
	}
	out := make([]Agent, 0, len(order))
	for _, pid := range order {
		a := found[pid]
		s.conversation(a, sameFolder[string(a.Harness)+"\x00"+folderKey(a.Cwd)])
		if a.Reason == "" && a.Cwd == "" {
			a.Reason = "its working folder cannot be read"
		}
		if a.Reason == "" && a.Harness != model.HarnessClaude && a.Harness != model.HarnessCodex {
			a.Reason = "tmx adopts Claude Code and Codex sessions"
		}
		a.Adoptable = a.Reason == ""
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Adoptable != out[j].Adoptable {
			return out[i].Adoptable
		}
		return out[i].StartedAt > out[j].StartedAt
	})
	return out, nil
}

// ErrNotFound: no such agent process (or tmx runs it).
var ErrNotFound = errors.New("no agent process with that pid outside tmx")

// Find returns one discovered agent.
func (s *Scanner) Find(pid int, managed []int) (Agent, error) {
	all, err := s.Scan(managed)
	if err != nil {
		return Agent{}, err
	}
	for _, a := range all {
		if a.PID == pid {
			return a, nil
		}
	}
	return Agent{}, ErrNotFound
}

// Owns reports whether conversation id belongs to the agent's folder (so an explicit pick
// from the owner is a real conversation of that harness there).
func (s *Scanner) Owns(a Agent, id string) bool {
	for _, c := range a.Candidates {
		if c.ID == id {
			return true
		}
	}
	switch a.Harness {
	case model.HarnessClaude:
		return harness.ClaudeTranscriptExists(s.home(), a.Cwd, id)
	case model.HarnessCodex:
		return harness.CodexRolloutExists(harness.CodexHome(s.home()), id)
	}
	return false
}

// matchWindow: a fresh session's transcript starts within this of its process.
const matchWindow = 2 * time.Minute

func (s *Scanner) conversation(a *Agent, sameFolder int) {
	rx := claudeConvArg
	if a.Harness == model.HarnessCodex {
		rx = codexConvArg
	}
	if a.Harness == model.HarnessClaude || a.Harness == model.HarnessCodex {
		if m := rx.FindStringSubmatch(a.Command); m != nil {
			a.ConversationID, a.ConversationSource = m[1], "command line"
			return
		}
	} else {
		a.ConversationSource = "not read for this harness"
		return
	}
	if a.Cwd == "" {
		a.ConversationSource = "working folder unreadable"
		return
	}
	var cands []harness.Conversation
	started := time.UnixMilli(a.StartedAt)
	if a.Harness == model.HarnessClaude {
		cands = harness.ClaudeConversations(s.home(), a.Cwd)
	} else {
		since := s.now().Add(-14 * 24 * time.Hour)
		cands = harness.CodexConversations(harness.CodexHome(s.home()), a.Cwd, since)
	}
	if len(cands) > 8 {
		cands = cands[:8]
	}
	a.Candidates = cands
	if a.StartedAt != 0 {
		var close []harness.Conversation
		for _, c := range cands {
			if c.Started != 0 && absDur(time.UnixMilli(c.Started).Sub(started)) < matchWindow {
				close = append(close, c)
			}
		}
		if len(close) == 1 {
			a.ConversationID, a.ConversationSource = close[0].ID, "transcript started with this process"
			return
		}
	}
	if sameFolder > 1 {
		a.ConversationSource = fmt.Sprintf("%d %s sessions share this folder; pick the conversation", sameFolder, a.Harness)
		return
	}
	// The one session in the folder: its conversation is the newest written since it started.
	var newest *harness.Conversation
	for i, c := range cands {
		if a.StartedAt == 0 || c.Updated >= a.StartedAt-5000 {
			if newest == nil || c.Updated > newest.Updated {
				newest = &cands[i]
			}
		}
	}
	if newest != nil {
		a.ConversationID, a.ConversationSource = newest.ID, "newest transcript in this folder"
		return
	}
	a.ConversationSource = "no transcript yet (a new conversation)"
}

// interactive: not a one-shot or server mode (-p, exec, app-server, mcp, stream-json I/O).
func interactive(args []string) bool {
	sub := ""
	for i, a := range args {
		if i == 0 {
			continue
		}
		switch {
		case a == "-p" || a == "--print" || a == "--sdk-url":
			return false
		case strings.HasPrefix(a, "--input-format") || strings.HasPrefix(a, "--output-format"):
			if strings.Contains(a, "stream-json") || i+1 < len(args) && args[i+1] == "stream-json" {
				return false
			}
		case sub == "" && !strings.HasPrefix(a, "-") && (i == 1 || !strings.HasPrefix(args[i-1], "-")):
			sub = a
		}
	}
	switch sub {
	case "exec", "e", "app-server", "mcp-server", "mcp", "serve", "run", "review", "cloud":
		return false
	}
	return true
}

func managedBy(chain []Proc) string {
	for _, p := range chain {
		if j := strings.Join(p.Args, " "); tmxManager.MatchString(j) {
			return fmt.Sprintf("a tmx (pid %d)", p.PID)
		}
	}
	return ""
}

func editorChild(chain []Proc) bool {
	for _, p := range chain {
		if len(p.Args) > 0 && editorParent.MatchString(p.Args[0]) {
			return true
		}
	}
	return false
}

func bornBefore(parent, child Proc) bool {
	if parent.Started.IsZero() || child.Started.IsZero() {
		return runtime.GOOS != "windows" // Unix re-parents orphans, so its ppid is never stale
	}
	return !child.Started.Add(time.Second).Before(parent.Started)
}

func folderKey(dir string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(dir)
	}
	return dir
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
