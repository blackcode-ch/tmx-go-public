// Package config resolves the state directory and reads/writes the small files in it:
// config.json, secrets/owner-token, daemon.json and the single-instance lock. See DESIGN.md
// "State on disk".
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// Version is the tmx version; release builds override it with -ldflags "-X ...config.Version=...".
var Version = "0.1.0-dev"

// EnvHome overrides the state directory (tests always set it).
const EnvHome = "TMXG_HOME"

// Paths names every file in one state directory.
type Paths struct{ Home string }

// DefaultHome returns $TMXG_HOME, else the per-OS default.
func DefaultHome() (string, error) {
	h, err := defaultHome()
	if err != nil {
		return "", err
	}
	if err := testModePath(h); err != nil {
		return "", err
	}
	return h, nil
}

func defaultHome() (string, error) {
	if h := os.Getenv(EnvHome); h != "" {
		return filepath.Abs(h)
	}
	return OSDefaultHome()
}

// OSDefaultHome is the per-OS state directory used when $TMXG_HOME is not set.
func OSDefaultHome() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "tmxg-trial"), nil
		}
		return "", errors.New("LOCALAPPDATA is not set")
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "tmxg-trial"), nil
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "state", "tmxg-trial"), nil
	}
}

// Resolve returns the Paths for DefaultHome and creates the directory.
func Resolve() (Paths, error) {
	h, err := DefaultHome()
	if err != nil {
		return Paths{}, err
	}
	p := Paths{Home: h}
	return p, p.Ensure()
}

// Ensure creates the state directory and its fixed subdirectories (private to the user; the
// secrets folder also on Windows, see MkdirSecret).
func (p Paths) Ensure() error {
	if err := testModePath(p.Home); err != nil {
		return err
	}
	// Validate the secrets ancestry before creating any other state beneath Home.
	if err := MkdirSecret(p.SecretsDir()); err != nil {
		return err
	}
	for _, d := range []string{p.Home, p.AgentsDir(), p.LogsDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (p Paths) DB() string                { return filepath.Join(p.Home, "tmx.db") }
func (p Paths) ConfigFile() string        { return filepath.Join(p.Home, "config.json") }
func (p Paths) SecretsDir() string        { return filepath.Join(p.Home, "secrets") }
func (p Paths) OwnerTokenFile() string    { return filepath.Join(p.SecretsDir(), "owner-token") }
func (p Paths) DaemonFile() string        { return filepath.Join(p.Home, "daemon.json") }
func (p Paths) LockFile() string          { return filepath.Join(p.Home, "daemon.lock") }
func (p Paths) AgentsDir() string         { return filepath.Join(p.Home, "agents") }
func (p Paths) AgentDir(id string) string { return filepath.Join(p.AgentsDir(), id) }
func (p Paths) P2PKeyFile() string        { return filepath.Join(p.SecretsDir(), "p2p-key") }
func (p Paths) P2PStateFile() string      { return filepath.Join(p.SecretsDir(), "p2p-state.json") }
func (p Paths) LogsDir() string           { return filepath.Join(p.Home, "logs") }

// ClaudeConfigDir is the CLAUDE_CONFIG_DIR of every claude this daemon starts (agents,
// summaries, account sign-in and usage): its own .claude.json, sign-in and transcripts, never
// the owner's shared ~/.claude.json (7 Oct: concurrent writers corrupted it). One per daemon,
// not per agent: the sign-in lives there, so the owner signs in once (Accounts), and agents
// started later are signed in too. It holds a credential, so it is a secrets-private folder.
func (p Paths) ClaudeConfigDir() string { return filepath.Join(p.Home, "claude-config") }

// EnsureClaudeConfigDir creates ClaudeConfigDir, private like the secrets folder (MkdirSecret),
// and returns it. A new folder gets a .claude.json that marks the first-run onboarding done (no
// sign-in, nothing secret): otherwise a terminal agent would stop at the theme picker. An
// existing .claude.json is never touched.
func (p Paths) EnsureClaudeConfigDir() (string, error) {
	return EnsureClaudeDir(p.ClaudeConfigDir())
}

// EnsureClaudeDir prepares dir as a CLAUDE_CONFIG_DIR the way EnsureClaudeConfigDir does (also
// a second Claude account's folder, internal/accounts).
func EnsureClaudeDir(dir string) (string, error) {
	if err := MkdirSecret(dir); err != nil {
		return "", fmt.Errorf("claude config folder %s: %w", dir, err)
	}
	cfg := filepath.Join(dir, ".claude.json")
	if _, err := os.Stat(cfg); errors.Is(err, fs.ErrNotExist) {
		if err := WriteFileAtomic(cfg, []byte(`{"hasCompletedOnboarding": true}`+"\n"), 0o600); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// UploadsDir holds files the owner attached to messages: uploads/<agent id>/<upload id>/<name>.
func (p Paths) UploadsDir() string { return filepath.Join(p.Home, "uploads") }

// ChatFilesDir holds files of chat messages: chatfiles/<chat id>/<file id>/<name>.
func (p Paths) ChatFilesDir() string { return filepath.Join(p.Home, "chatfiles") }

// GitHubTokenFile lets the updater read releases of a private repository (`tmx update token`).
func (p Paths) GitHubTokenFile() string { return filepath.Join(p.SecretsDir(), "github-token") }

// CompanionTokenFile is the token Companion's `tmx mcp --companion` uses to reach this daemon
// (Companion → tmx); CompanionAgentTokenFile is the agent key Companion issued to tmx
// (tmx → Companion). Both 0600.
func (p Paths) CompanionTokenFile() string { return filepath.Join(p.SecretsDir(), "companion-token") }
func (p Paths) CompanionAgentTokenFile() string {
	return filepath.Join(p.SecretsDir(), "companion-agent-token")
}
func (p Paths) DaemonLog() string { return filepath.Join(p.LogsDir(), "daemon.log") }

// HostTokenFile is the bearer token this daemon uses to read another local daemon's API for
// the fleet-wide Overview (config.json "hosts"; see Host). Callers must check ValidHostName
// first: name reaches the filesystem unescaped.
func (p Paths) HostTokenFile(name string) string {
	return filepath.Join(p.SecretsDir(), "host-"+name+"-token")
}

// VoiceDir holds local voice transcription (DESIGN.md "Voice"): voice/engine/<version>/whisper-cli
// and voice/models/ggml-*.bin, both downloaded on demand.
func (p Paths) VoiceDir() string { return filepath.Join(p.Home, "voice") }

// RecordingsDir holds the owner's screen and microphone recordings: recordings/<id>/seg-0001.mp4
// and seg-0001.wav (0600). Agents may read them (the owner's decision; DESIGN.md "Recorder").
func (p Paths) RecordingsDir() string { return filepath.Join(p.Home, "recordings") }
