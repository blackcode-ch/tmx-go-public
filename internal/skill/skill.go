// Package skill holds SKILL.md: how AI agents (Claude Code, Codex, ...) set up tmx on a
// server, join chats and use the CLI. `tmx skill` prints it, `tmx skill install` writes it
// as a Claude Code skill, and the daemon serves it at GET /skill.md.
package skill

import (
	"bytes"
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed SKILL.md
var content []byte

// Content returns SKILL.md (a copy: callers may not change the embedded bytes).
// The file is written with LF line endings: a Windows checkout may have turned them into
// CRLF, and the skill's YAML front matter must stay exactly as published.
func Content() []byte {
	return bytes.ReplaceAll(append([]byte(nil), content...), []byte("\r\n"), []byte("\n"))
}

// DefaultDir is where Claude Code finds user skills: ~/.claude/skills/tmxg.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "skills", "tmxg"), nil
}
