// Package agentpolicy contains side-effect-free launch and command policies.
// Callers must apply them before spawning a process or dispatching a browser tool.
// Command matching is defense in depth, not a sandbox for arbitrary code.
package agentpolicy

import (
	"errors"
	"regexp"
	"strings"
)

// LaunchPolicy combines the selected harness with trusted host capabilities.
// Sandboxed means the runner actually enforces isolation, not that the request
// asked for it. Neither capability boolean may come from agent request fields.
// Windows Codex danger-full-access does NOT qualify as Sandboxed.
type LaunchPolicy struct {
	GOOS                   string
	Harness                string
	Sandboxed              bool
	CodexParityGuard       bool // daemon-installed Python-parity shims; NOT a sandbox
	BrowserAutomation      bool
	AllowBrowserAutomation bool // explicit host permission; defaults to false
}

var (
	ErrUnsandboxedHarness = errors.New("Windows agents require Claude command guards or an enforced sandbox")
	ErrBrowserAutomation  = errors.New("agent browser automation is disabled by host policy")
)

// CheckLaunch refuses unguarded Windows harnesses and browser-capable launches.
// Harness is the canonical registry name (claude covers chat and terminal mode).
func CheckLaunch(p LaunchPolicy) error {
	if p.GOOS != "windows" {
		return nil
	}
	if p.Harness != "claude" && !p.Sandboxed && !(p.Harness == "codex" && p.CodexParityGuard) {
		return ErrUnsandboxedHarness
	}
	if p.BrowserAutomation && !p.AllowBrowserAutomation {
		return ErrBrowserAutomation
	}
	return nil
}

// BrowserOutcome classifies how a browser-automation keyword relates to a command or tool name.
type BrowserOutcome int

const (
	// BrowserOK: no match, or the host allows browser automation.
	BrowserOK BrowserOutcome = iota
	// BrowserMention: the keyword is only plain text (an argument, a file name, a commit
	// message, a chat message, ...), not the thing being run. The owner may still approve it.
	BrowserMention
	// BrowserCommand: the keyword names the thing actually invoked: the executable basename
	// (even inside a quoted path or a bare tool name), the target of npx, python -m, node or
	// Start-Process/start, or a --remote-debugging* flag.
	BrowserCommand
)

// browserMention matches any mention of a browser, an automation library or a CDP interface,
// anywhere in the text: the fallback that makes a plain mention a Hold, not a Block.
var browserMention = regexp.MustCompile(`(?i)(chrome|chromium|msedge|firefox|playwright|puppeteer|remote-debugging|cdp|browser)`)

// browserTool names browsers and automation libraries specifically, not the generic word
// "browser" (which also names unrelated tools such as browserslist or browser-sync). Used to
// decide whether something is actually being launched or targeted, not merely mentioned.
var browserTool = regexp.MustCompile(`(?i)(chrome|chromium|msedge|firefox|playwright|puppeteer|cdp)`)

var remoteDebuggingFlag = regexp.MustCompile(`(?i)^--remote-debugging`)

// ClassifyBrowserCommand reports how a command line or tool name relates to the host's browser
// automation policy. Command matching is defense in depth, not a sandbox for arbitrary code: an
// obfuscated script still requires the harness guard or sandbox.
func ClassifyBrowserCommand(goos string, allowBrowserAutomation bool, command string) BrowserOutcome {
	if goos != "windows" || allowBrowserAutomation {
		return BrowserOK
	}
	// A launch may be chained after another command (;, &&, ||), piped, backgrounded, or
	// wrapped (cmd /c, powershell/pwsh -c, the PowerShell call operator &): check every segment
	// in command position, not just the first word of the whole line.
	for _, seg := range commandSegments(command) {
		if segmentLaunchesBrowser(commandWords(seg)) {
			return BrowserCommand
		}
	}
	for _, w := range commandWords(command) {
		if remoteDebuggingFlag.MatchString(stripQuotes(w)) {
			return BrowserCommand
		}
	}
	if browserMention.MatchString(command) {
		return BrowserMention
	}
	return BrowserOK
}

// segmentLaunchesBrowser reports whether one chain segment's own command (after unwrapping
// cmd/powershell/env/timeout wrappers) is a browser binary, or targets one via npx, python -m,
// node or Start-Process/start.
func segmentLaunchesBrowser(words []string) bool {
	words, blockOutright := unwrapSegment(words)
	if blockOutright {
		return true
	}
	if len(words) == 0 {
		return false
	}
	// The first word is the thing being run, whether it parses as a real executable name (a
	// shell command) or not (a bare tool name such as mcp__browser__navigate): matched against
	// the generic word "browser" too, unlike a launcher's target below.
	if browserMention.MatchString(stripQuotes(words[0])) {
		return true
	}
	switch verbBasename(words[0]) {
	case "npx":
		return len(words) > 1 && browserTool.MatchString(stripQuotes(words[1]))
	case "python", "python3":
		for i := 1; i < len(words)-1; i++ {
			if stripQuotes(words[i]) == "-m" && browserTool.MatchString(stripQuotes(words[i+1])) {
				return true
			}
		}
	case "node":
		for _, w := range words[1:] {
			if browserTool.MatchString(stripQuotes(w)) {
				return true
			}
		}
	case "start", "start-process":
		// start/Start-Process takes an optional window title before the program (start "" chrome,
		// start "x" msedge): the first non-flag word is not necessarily the program, so every
		// later word is checked, not just the first.
		for _, w := range words[1:] {
			s := stripQuotes(w)
			if strings.HasPrefix(s, "-") {
				continue
			}
			if browserTool.MatchString(s) {
				return true
			}
		}
	}
	return false
}

// unwrapSegment strips wrappers that hand their remaining words to another command: cmd (after
// skipping other switches) /c|/k, powershell/pwsh (after skipping other switches) -c|-command,
// env (with leading NAME=value words), and timeout <n>. It repeats so a nested wrap (cmd /c
// powershell -c chrome) unwraps fully. blockOutright is true for an encoded or obfuscated
// PowerShell command (-EncodedCommand/-enc/-e) this checker cannot read into words at all.
func unwrapSegment(words []string) (rest []string, blockOutright bool) {
	for len(words) > 0 {
		switch verbBasename(words[0]) {
		case "cmd":
			if next, ok := unwrapCmd(words[1:]); ok {
				words = next
				continue
			}
		case "powershell", "pwsh":
			next, enc, ok := unwrapPowerShell(words[1:])
			if enc {
				return nil, true
			}
			if ok {
				words = next
				continue
			}
		case "env":
			words = words[1:]
			for len(words) > 0 {
				s := stripQuotes(words[0])
				if strings.HasPrefix(s, "-") || !strings.Contains(s, "=") {
					break
				}
				words = words[1:]
			}
			continue
		case "timeout", "gtimeout":
			words = words[1:]
			if len(words) > 0 {
				words = words[1:] // the duration
			}
			continue
		}
		break
	}
	return words, false
}

// cmdRunSwitches are cmd.exe switches that run what follows; cmdSkipSwitches are benign
// switches that may come before one (/d disables AutoRun, /s strips quotes, the rest are
// on/off toggles): only these are skipped, so an unrecognized switch stops the unwrap rather
// than silently skipping past something that matters.
var cmdRunSwitches = map[string]bool{"/c": true, "/k": true}
var cmdSkipSwitches = map[string]bool{"/d": true, "/s": true, "/a": true, "/u": true, "/q": true,
	"/e:on": true, "/e:off": true, "/f:on": true, "/f:off": true, "/v:on": true, "/v:off": true}

func unwrapCmd(words []string) (rest []string, ok bool) {
	for i, w := range words {
		f := strings.ToLower(stripQuotes(w))
		if cmdRunSwitches[f] {
			return words[i+1:], true
		}
		if !cmdSkipSwitches[f] {
			return nil, false
		}
	}
	return nil, false
}

// psValueSwitches are powershell/pwsh switches that take a separate value word (skipped along
// with the switch itself: -ExecutionPolicy Bypass, -WindowStyle Hidden, ...), so they do not
// stop the scan for the run switch the way an unrecognized switch does.
var psValueSwitches = map[string]bool{
	"-executionpolicy": true, "-ep": true, "-ex": true,
	"-windowstyle": true, "-w": true, "-win": true,
	"-version": true, "-v": true,
	"-configurationname": true, "-config": true,
	"-outputformat": true, "-o": true, "-of": true,
	"-inputformat": true, "-i": true, "-if": true,
	"-workingdirectory": true, "-wd": true,
	"-settingsfile": true, "-psconsolefile": true, "-custompipename": true,
}

// isAbbrev reports whether f is an unambiguous abbreviation of full: at least min characters
// (the leading - included), and a genuine prefix of it.
func isAbbrev(f, full string, min int) bool {
	return len(f) >= min && len(f) <= len(full) && strings.HasPrefix(full, f)
}

// unwrapPowerShell scans powershell/pwsh's switches (normalizing a leading / to -, which
// Windows PowerShell 5.1 also accepts) for its run switch: any unambiguous abbreviation of
// -command (-c, -co, -com, ...), skipping any other switch ahead of it, including ones that
// take a separate value (-ExecutionPolicy Bypass, -WindowStyle Hidden, ...). -File/-f runs a
// script file directly, so its path is returned as the remaining command (Block if it names a
// browser; otherwise the unwrap stops there, since what follows is the script's own
// arguments). enc is true for -e, -ec or an abbreviation of -encodedcommand of length >= 3: an
// obfuscated command this checker cannot read. In Windows PowerShell 5.1 a first non-switch
// word is itself the command, not a value, so it becomes the remaining command too.
func unwrapPowerShell(words []string) (rest []string, enc, ok bool) {
	for i := 0; i < len(words); i++ {
		f := strings.ToLower(stripQuotes(words[i]))
		if strings.HasPrefix(f, "/") {
			f = "-" + f[1:]
		}
		switch {
		case isAbbrev(f, "-command", 2):
			return words[i+1:], false, true
		case f == "-e" || f == "-ec" || isAbbrev(f, "-encodedcommand", 3):
			return nil, true, false
		case f == "-file" || f == "-f":
			if i+1 < len(words) {
				return words[i+1 : i+2], false, true
			}
			return nil, false, false
		case psValueSwitches[f]:
			i++ // also skip its value
		case strings.HasPrefix(f, "-"):
			// a boolean switch (-NoProfile, -NoLogo, -NonInteractive, -Sta, -Mta, -NoExit, ...)
		default:
			return words[i:], false, true
		}
	}
	return nil, false, false
}

// commandSegments splits a command line into chained, piped or backgrounded segments on ;, &&,
// ||, |, & and newline, respecting quotes: a launch after a chain operator (git status;
// chrome.exe), behind a pipe (echo | node -e ...) or after the PowerShell call operator
// (& "...\chrome.exe") is still checked.
func commandSegments(s string) []string {
	var segs []string
	var b strings.Builder
	var quote byte
	flush := func() { segs = append(segs, b.String()); b.Reset() }
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			b.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			b.WriteByte(c)
		case c == ';' || c == '\n':
			flush()
		case c == '&' && i+1 < len(s) && s[i+1] == '&':
			flush()
			i++
		case c == '|' && i+1 < len(s) && s[i+1] == '|':
			flush()
			i++
		case c == '|' || c == '&':
			flush()
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return segs
}

// commandWords splits a command line into words on unquoted whitespace: a minimal, forgiving
// tokenizer (it never fails; unbalanced quotes simply run to the end) good enough to tell a
// command's own name and its first target from an argument buried later in the line.
func commandWords(s string) []string {
	var words []string
	var b strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			b.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			b.WriteByte(c)
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if b.Len() > 0 {
				words = append(words, b.String())
				b.Reset()
			}
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		words = append(words, b.String())
	}
	return words
}

// stripQuotes removes one matching pair of surrounding quotes, if any.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// verbBasename is a command word's name: basename, lower case, without its extension.
func verbBasename(s string) string {
	s = stripQuotes(s)
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	s = strings.ToLower(s)
	for _, ext := range []string{".exe", ".com", ".cmd", ".bat"} {
		s = strings.TrimSuffix(s, ext)
	}
	return s
}

// CheckCommand applies the host browser policy to a command or tool name, refusing only an
// actual launch or target (ClassifyBrowserCommand); a mere mention is not reported as an error
// here and is left for the guard's Hold, which the owner can approve.
func CheckCommand(goos string, allowBrowserAutomation bool, command string) error {
	if ClassifyBrowserCommand(goos, allowBrowserAutomation, command) == BrowserCommand {
		return ErrBrowserAutomation
	}
	return nil
}

// FilterEnv removes owner credentials on every platform. Only the launched
// agent's own TMXG_AGENT_TOKEN may survive: callers must remove any inherited
// value before adding the freshly minted child credential. Input is unchanged.
// This deny-list does not isolate credential files; that requires a sandbox.
func FilterEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" || strings.ContainsAny(kv, "\x00\r\n") {
			continue
		}
		key = strings.ToUpper(key)
		if key != "TMXG_AGENT_TOKEN" && deniedEnv(key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// InheritedFleetIdentity reports a variable that names the launching process's own tmx
// fleet identity (the Python fleet's TMX_URL, TMX_AGENT_TOKEN, any TMX_*TOKEN*). A daemon started
// from an agent's shell inherits them; no child, not even the owner's own shell, may act with them.
// TMXG_* (this daemon's own, minted per child) is not matched.
func InheritedFleetIdentity(key string) bool {
	key = strings.ToUpper(key)
	if !strings.HasPrefix(key, "TMX_") {
		return false
	}
	return key == "TMX_URL" || strings.Contains(key, "TOKEN")
}

func deniedEnv(key string) bool {
	if strings.HasPrefix(key, "GH_") || strings.HasPrefix(key, "GITHUB_") {
		return true
	}
	for _, part := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "ACCESS_KEY", "PRIVATE_KEY", "CREDENTIAL"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	switch key {
	case "SSH_AUTH_SOCK", "SSH_AGENT_PID", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "GOOGLE_APPLICATION_CREDENTIALS":
		return true
	}
	return false
}
