// Package harness knows each coding agent tool tmx runs: Claude Code, Codex, opencode, pi and
// a plain shell. Per harness: executables, modes, launch flags and env for terminal mode,
// screen patterns and the answers to its menus, known first-run gates, and where it keeps
// its conversations. Ported from v2's adapters.py; see DESIGN.md "Harnesses".
package harness

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner/term/screen"
)

// Info describes one harness.
type Info struct {
	Name    model.Harness `json:"name"`
	Label   string        `json:"label"`
	Install string        `json:"install,omitempty"`
	// Modes it runs in; the first is the default.
	Modes []model.Mode `json:"modes"`

	commands []string // executable names, tried in order
	// Patterns read its TUI's states off the screen; nil = no detection (a shell).
	Patterns *screen.Patterns `json:"-"`
	Synonyms screen.Synonyms  `json:"-"`
	Gates    []Gate           `json:"-"`
	// TypedMail: queued mail may be typed into its prompt when idle (never for a shell,
	// where the text would run as commands).
	TypedMail bool `json:"-"`
	// SubmitDelay is the pause between pasting a message and pressing Enter (Codex takes an
	// Enter right after a paste as part of it).
	SubmitDelay time.Duration `json:"-"`
	// SystemPrompt: the harness takes tmx's instructions at launch; otherwise they are put in
	// front of the first message of a new conversation.
	SystemPrompt bool `json:"-"`
}

// Gate is a known first-run or nag screen and the answer that changes nothing beyond
// getting past it. Label "" presses Enter (keeps the highlighted choice). Trust gates are
// answered only for a folder tmx may vouch for (TrustableFolder).
type Gate struct {
	Rx    *regexp.Regexp
	Label string
	Trust bool
}

// Default for a harness: the mode used when a launch names none.
func (i Info) DefaultMode() model.Mode { return i.Modes[0] }

// Supports reports whether the harness runs in mode m.
func (i Info) Supports(m model.Mode) bool {
	for _, x := range i.Modes {
		if x == m {
			return true
		}
	}
	return false
}

// "approve_always" deliberately never matches "switch to auto mode": that changes the
// session's whole permission mode, not just this one permission.
var menuNav = screen.Synonyms{
	"approve":        {"Yes"},
	"approve_always": {"Yes, and don't ask again", "Yes, and always allow", "always allow"},
	"deny":           {"No"},
}

var registry = []Info{
	{
		Name: model.HarnessClaude, Label: "Claude Code", Install: "npm i -g @anthropic-ai/claude-code",
		Modes: []model.Mode{model.ModeChat, model.ModeTerminal}, commands: []string{"claude"},
		Patterns: &screen.Patterns{
			Trust:    screen.Rx(`Is\s+this\s+a\s+project\s+you\s+created\s+or\s+one\s+you\s+trust|Quick\s+safety\s+check|Yes, I trust this folder`),
			Approval: screen.Rx(`Do you want to (proceed|make this edit|create|allow)`),
			Question: screen.Rx(`Enter to select .*Esc to cancel|Chat about this`),
			Working:  screen.Rx(`esc to interrupt|Waiting for API response`),
			Idle:     screen.Rx(`^\s*[>❯](\s|$)|\? for shortcuts`),
			// With a background shell or a mode on, the hint is replaced by e.g. "⏵⏵ bypass
			// permissions on · 1 shell", so the mode line counts too.
			AtPrompt:   screen.Rx(`shift\+tab to cycle|\? for shortcuts|⏵⏵|⏸|(bypass permissions|accept edits|plan mode|auto mode|manual mode) on`),
			MenuCursor: "❯",
		},
		Synonyms: menuNav,
		Gates: []Gate{
			// \s+: the question wraps in a narrow terminal.
			{Rx: screen.Rx(`Is\s+this\s+a\s+project\s+you\s+created\s+or\s+one\s+you\s+trust|Quick\s+safety\s+check`), Label: "Yes, I trust this folder", Trust: true},
			{Rx: screen.Rx(`running in Bypass Permissions mode`), Label: "Yes, I accept"},
			{Rx: screen.Rx(`Make auto mode your default permission mode`), Label: "No"}, // never change user defaults
			{Rx: screen.Rx(`Syntax theme:|Choose the text style`)},                      // first run: keep the theme
		},
		TypedMail: true, SubmitDelay: 400 * time.Millisecond, SystemPrompt: true,
	},
	{
		Name: model.HarnessCodex, Label: "Codex", Install: "npm i -g @openai/codex",
		Modes: []model.Mode{model.ModeChat, model.ModeTerminal}, commands: []string{"codex"},
		Patterns: &screen.Patterns{
			Locked:   screen.Rx(`conversation is open in another app`),
			Trust:    screen.Rx(`Trust this folder\?`),
			Approval: screen.Rx(`Would you like to (run the following command|make the following edits|grant these permissions)`),
			Question: screen.Rx(`enter to submit answer|Select an option`),
			Working:  screen.Rx(`Working.*to interrupt|esc to interrupt|\btab to queue message\b`),
			Idle:     screen.Rx(`^\s*›(\s|$)|context left|% left`),
			// The weekly or rolling-window hard limit; Codex keeps its input box below it.
			UsageLimit: screen.Rx(`hit your usage limit`),
			// › is also Codex's menu cursor ("› 1. Trust"): a › line that is not an option, or
			// the status line under the input box ("gpt-… medium · ~/code/app").
			AtPrompt: screen.Rx(`^\s*›(?:\s*$|\s+[^\d\s]|\s+\d+(?:[^\d.]|$))|^\s+\S[^·\n]*\s·\s(?:[A-Za-z]:\\|~|/)`),
		},
		Synonyms: menuNav,
		// No gate for "Yes, continue anyway" (ported from v2): in Codex that option is only the
		// confirmation of a switch to Full Access in its /approvals and /permissions pickers
		// ("Enable full access?", permission_popups.rs), never a start-up screen, so answering
		// it would grant full access mid-session. It is a menu for the owner like any other.
		Gates: []Gate{
			{Rx: screen.Rx(`Trust this folder\?`), Label: "Trust and continue", Trust: true},
		},
		TypedMail: true, SubmitDelay: 1200 * time.Millisecond, SystemPrompt: true,
	},
	{
		Name: model.HarnessOpenCode, Label: "opencode", Install: "npm i -g opencode-ai",
		Modes: []model.Mode{model.ModeTerminal}, commands: []string{"opencode"},
		Patterns: &screen.Patterns{
			Approval: screen.Rx(`Permission required`),
			Question: screen.Rx(`Submit answer`),
			Working:  screen.Rx(`esc (again to )?interrupt`),
			Idle:     screen.Rx(`ctrl\+p commands|tab agents`),
			RowMenu:  screen.Rx(`⇆\s*select`),
		},
		Synonyms:  screen.Synonyms{"approve": {"Allow once"}, "approve_always": {"Always allow", "Allow always"}, "deny": {"Reject"}},
		TypedMail: true, SubmitDelay: 400 * time.Millisecond,
	},
	{
		Name: "agy", Label: "Gemini (Antigravity)", Install: "install agy CLI", Modes: []model.Mode{model.ModeTerminal},
		commands: []string{"agy"},
		Patterns: &screen.Patterns{
			// Verified Python agy adapter: only the live footer is idle; busy
			// announcements above the input box's last rule are conversation history.
			AtPrompt:     screen.Rx(`\? for shortcuts`),
			Working:      screen.Rx(`esc to interrupt|Thinking|Generating|Thought for|Using `),
			Approval:     screen.Rx(`Allow\s+[^?\n]{1,80}\?|Yes,\s*allow access|No,\s*deny access`),
			Trust:        screen.Rx(`Do you trust the contents of this project\?|Yes, I trust this folder`),
			NumericEnter: true, FooterAfterRule: true,
		},
		Synonyms:  menuNav,
		Gates:     []Gate{{Rx: screen.Rx(`Do you trust the contents of this project\?`), Label: "Yes, I trust this folder", Trust: true}},
		TypedMail: true, SubmitDelay: 400 * time.Millisecond,
	},
	{
		// Google's Gemini CLI (npm @google/gemini-cli). Patterns from its own UI
		// (packages/cli/src/ui: LoadingIndicator, InputPrompt, ToolConfirmationMessage,
		// FolderTrustDialog), not yet checked against a live screen.
		Name: model.HarnessGemini, Label: "Gemini CLI", Install: "npm i -g @google/gemini-cli",
		Modes: []model.Mode{model.ModeTerminal}, commands: []string{"gemini"},
		Patterns: &screen.Patterns{
			Working:  screen.Rx(`esc to cancel`),
			Idle:     screen.Rx(`Type your message`),
			Approval: screen.Rx(`Allow once`),
			Trust:    screen.Rx(`Do you trust`),
		},
		Synonyms:  menuNav,
		TypedMail: true, SubmitDelay: 400 * time.Millisecond,
	},
	{
		Name: model.HarnessPi, Label: "pi", Install: "npm i -g --ignore-scripts @earendil-works/pi-coding-agent",
		Modes: []model.Mode{model.ModeTerminal}, commands: []string{"pi"},
		Patterns: &screen.Patterns{
			Working: screen.Rx(`Working.*to interrupt`),
			Idle:    screen.Rx(`ctrl\+c twice to exit|ctrl\+d to exit`),
		},
		Synonyms:  menuNav,
		TypedMail: true, SubmitDelay: 400 * time.Millisecond,
	},
	{
		Name: model.HarnessShell, Label: "Shell", Modes: []model.Mode{model.ModeTerminal},
	},
}

// Get returns a harness by name ("" = claude).
func Get(h model.Harness) (Info, bool) {
	if h == "antigravity" {
		h = "agy"
	}
	if h == "" {
		h = model.HarnessClaude
	}
	for _, i := range registry {
		if i.Name == h {
			return i, true
		}
	}
	return Info{}, false
}

// All returns every harness in display order.
func All() []Info { return append([]Info(nil), registry...) }

// ErrNotInstalled: the harness's executable was not found.
var ErrNotInstalled = errors.New("not installed")

// LookPath finds a harness's executable: override if set, else PATH, else the usual install
// folders (a GUI app's PATH misses ~/.local/bin, npm's and Homebrew's folders).
func LookPath(h model.Harness, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	info, ok := Get(h)
	if !ok || len(info.commands) == 0 {
		return "", fmt.Errorf("harness %q has no executable", h)
	}
	for _, c := range info.commands {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	for _, d := range installDirs(h) {
		for _, c := range info.commands {
			for _, n := range exeNames(c) {
				p := filepath.Join(d, n)
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					return p, nil
				}
			}
		}
	}
	return "", fmt.Errorf("%s is %w (%s)", info.Label, ErrNotInstalled, info.Install)
}

// installDirs are the usual install folders of h, searched after PATH.
func installDirs(h model.Harness) []string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		dirs := []string{filepath.Join(os.Getenv("APPDATA"), "npm"), filepath.Join(home, ".local", "bin")}
		if h == "agy" || h == "antigravity" {
			dirs = append(dirs, filepath.Join(os.Getenv("LOCALAPPDATA"), "agy", "bin"))
		}
		return dirs
	}
	dirs := []string{
		filepath.Join(home, ".local", "bin"), filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".bun", "bin"), filepath.Join(home, ".opencode", "bin"),
		"/opt/homebrew/bin", "/usr/local/bin",
	}
	if h == model.HarnessClaude {
		dirs = append(dirs, filepath.Join(home, ".claude", "local"))
	}
	return dirs
}

func exeNames(c string) []string {
	if runtime.GOOS == "windows" {
		return []string{c + ".exe", c + ".cmd"}
	}
	return []string{c}
}

// Status is GET /api/harnesses: what each harness is and whether it is installed here.
type Status struct {
	Info
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	// Structured: it has a chat mode (machine-readable protocol), not only a terminal.
	Structured bool `json:"structured"`
}

// Statuses lists every harness with its install state.
func Statuses() []Status {
	var out []Status
	for _, i := range registry {
		s := Status{Info: i, Installed: true, Structured: i.Supports(model.ModeChat)}
		if i.Name != model.HarnessShell {
			p, err := LookPath(i.Name, "")
			s.Installed, s.Path = err == nil, p
		}
		out = append(out, s)
	}
	return out
}
