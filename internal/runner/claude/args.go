package claude

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/agentpolicy"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// LookPath returns the claude executable for spec: spec.Command, else `claude` on PATH, else
// the usual install locations (GUI apps on macOS get a thin PATH that misses ~/.local/bin).
func LookPath(spec runner.Spec) (string, error) {
	if spec.Command != "" {
		return spec.Command, nil
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, p := range installPaths(runtime.GOOS, home, os.Getenv("APPDATA")) {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
		}
	}
	return "", errors.New("claude not found on PATH, in ~/.local/bin or in ~/.claude/local")
}

// installPaths are where claude is usually installed when it is not on PATH: the native
// installer's ~/.local/bin, the old ~/.claude/local, and on Windows npm's global folder
// (%APPDATA%\npm, where `npm i -g` puts claude.cmd).
func installPaths(goos, home, appdata string) []string {
	names := []string{"claude"}
	dirs := []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, ".claude", "local")}
	if goos == "windows" {
		names = []string{"claude.exe", "claude.cmd"}
		if appdata != "" {
			dirs = append(dirs, filepath.Join(appdata, "npm"))
		}
	}
	var out []string
	for _, d := range dirs {
		for _, n := range names {
			out = append(out, filepath.Join(d, n))
		}
	}
	return out
}

// ChatArgs is the argv (without the executable) for chat mode.
func ChatArgs(spec runner.Spec) []string {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-prompt-tool", "stdio",
	}
	return append(args, commonArgs(spec)...)
}

// TerminalArgs is the argv (without the executable) for claude's interactive TUI.
func TerminalArgs(spec runner.Spec) []string { return commonArgs(spec) }

func commonArgs(spec runner.Spec) []string {
	args := []string{"--setting-sources", ""}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.SessionID != "" {
		args = append(args, "--resume", spec.SessionID)
	}
	if spec.PermissionMode != "" {
		args = append(args, "--permission-mode", spec.PermissionMode)
	}
	// --mcp-config and --allowedTools are variadic: the "=" form keeps them from swallowing
	// whatever argument follows.
	if spec.MCPConfigPath != "" {
		args = append(args, "--mcp-config="+spec.MCPConfigPath, "--strict-mcp-config")
	}
	for _, d := range spec.AddDirs { // variadic flag: the "=" form again
		args = append(args, "--add-dir="+d)
	}
	if spec.SettingsPath != "" {
		args = append(args, "--settings", spec.SettingsPath)
	}
	if len(spec.AllowedTools) > 0 {
		args = append(args, "--allowedTools="+strings.Join(spec.AllowedTools, ","))
	}
	if len(spec.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools="+strings.Join(spec.DisallowedTools, ","))
	}
	if spec.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", spec.SystemPrompt)
	}
	return append(args, spec.ExtraArgs...)
}

// sessionVars are set by a running Claude Code for its own children. A daemon started from
// inside a Claude session would otherwise hand them to every agent, which then believes it is
// that session's child.
var sessionVars = map[string]bool{
	"CLAUDECODE": true, "CLAUDE_CODE_ENTRYPOINT": true, "CLAUDE_CODE_SESSION_ID": true,
	"CLAUDE_CODE_CHILD_SESSION": true, "CLAUDE_CODE_SESSION_ATTENDED": true,
	"CLAUDE_CODE_MESSAGING_SOCKET": true, "CLAUDE_CODE_MESSAGING_TOKEN": true,
	"CLAUDE_CODE_EXECPATH": true, "CLAUDE_CODE_SSE_PORT": true, "CLAUDE_PID": true,
	"CLAUDE_AGENT_SDK_VERSION": true,
}

// rawEnv removes parent session variables and the daemon's own TMXG_AGENT_TOKEN; every BaseEnv
// variant starts here.
func rawEnv() []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !sessionVars[strings.ToUpper(k)] && !strings.EqualFold(k, "TMXG_AGENT_TOKEN") && !agentpolicy.InheritedFleetIdentity(k) {
			out = append(out, kv)
		}
	}
	return out
}

// BaseEnv removes parent session variables and inherited owner/agent credentials.
func BaseEnv() []string { return agentpolicy.FilterEnv(rawEnv()) }

// BaseEnvUnfiltered is BaseEnv without the owner-credential denylist: only for the owner's own
// shell (DESIGN.md "Safety"), which is the owner, not an agent, and needs their usual
// gh/ssh/account environment.
func BaseEnvUnfiltered() []string { return rawEnv() }

func mergeDedup(base []string, extra ...string) []string {
	key := func(kv string) string {
		k, _, _ := strings.Cut(kv, "=")
		if runtime.GOOS == "windows" {
			k = strings.ToUpper(k)
		}
		return k
	}
	all := append(append([]string{}, base...), extra...)
	last := make(map[string]int, len(all))
	for i, kv := range all {
		last[key(kv)] = i
	}
	out := make([]string, 0, len(last))
	for i, kv := range all {
		if last[key(kv)] == i {
			out = append(out, kv)
		}
	}
	return out
}

// MergeEnv returns base with extra applied on top; a later KEY=VALUE replaces an earlier one
// (keys compare case-insensitively on Windows). Owner credentials are filtered
// after merging so extra environment entries cannot reintroduce them.
func MergeEnv(base []string, extra ...string) []string {
	return agentpolicy.FilterEnv(mergeDedup(base, extra...))
}

// MergeEnvUnfiltered is MergeEnv without the owner-credential denylist: only for the owner's
// own shell (DESIGN.md "Safety").
func MergeEnvUnfiltered(base []string, extra ...string) []string {
	return mergeDedup(base, extra...)
}
