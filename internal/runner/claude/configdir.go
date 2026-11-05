package claude

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvConfigDir moves Claude Code's whole user state (.claude.json, .credentials.json,
// settings, projects/ transcripts) to a folder of its own.
//
// Every claude this daemon starts gets the daemon's own folder (config.Paths.ClaudeConfigDir),
// never the owner's ~/.claude and ~/.claude.json: several claudes writing the shared
// ~/.claude.json at once (the Python tmx daemon's agents, the owner's own sessions and ours)
// corrupted it on 7 Oct and crash-looped an agent.
const EnvConfigDir = "CLAUDE_CONFIG_DIR"

// ConfigDir is the CLAUDE_CONFIG_DIR in env (the last one wins, as in MergeEnv), or "".
func ConfigDir(env []string) string {
	dir := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && sameKey(k, EnvConfigDir) {
			dir = v
		}
	}
	return dir
}

// ConfigRoot is where the claude started with env keeps its state: its CLAUDE_CONFIG_DIR,
// else ~/.claude ("" when there is no home).
func ConfigRoot(env []string) string {
	if d := ConfigDir(env); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".claude")
	}
	return ""
}

// CheckConfigDir is the launch guard: a claude may start only with an isolated
// CLAUDE_CONFIG_DIR, an absolute folder that is neither the home folder nor the owner's
// shared ~/.claude (whose claude keeps ~/.claude.json beside it in the home).
func CheckConfigDir(env []string) error {
	home, _ := os.UserHomeDir()
	return checkConfigDir(ConfigDir(env), home)
}

func checkConfigDir(dir, home string) error {
	const why = "; refusing to start claude on the owner's shared ~/.claude.json (7 Oct: concurrent writers corrupted it)"
	if dir == "" {
		return errors.New("claude has no isolated " + EnvConfigDir + why)
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("claude's %s %q is not an absolute path%s", EnvConfigDir, dir, why)
	}
	if home != "" {
		d := realPath(dir)
		for _, shared := range []string{home, filepath.Join(home, ".claude")} {
			if samePath(d, realPath(shared)) {
				return fmt.Errorf("claude's %s %q is the owner's default%s", EnvConfigDir, dir, why)
			}
		}
	}
	return nil
}

// realPath is p cleaned, with symlinks resolved when it exists (a link to ~/.claude is ~/.claude).
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}

func sameKey(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
