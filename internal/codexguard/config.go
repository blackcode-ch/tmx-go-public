package codexguard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ConfigLease pins the generated settings for a running agent. The directory
// must be agent-specific and outside the agent's workspace.
type ConfigLease struct {
	Home string
	Pins []*Pin
}

func (l *ConfigLease) Close() error {
	var first error
	for _, pin := range l.Pins {
		if err := pin.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (l *ConfigLease) Verify() error {
	for _, pin := range l.Pins {
		if err := pin.Verify(); err != nil {
			return err
		}
	}
	return nil
}

// NewConfig writes only tmx-owned configuration; it never imports the owner's
// config, MCP servers, hooks, plugins or notification commands. Authentication
// provisioning is a separate owner flow and is not copied by this function.
func NewConfig(home, cwd, instructions string) (*ConfigLease, error) {
	if !filepath.IsAbs(home) || !filepath.IsAbs(cwd) {
		return nil, fmt.Errorf("Codex home and workspace must be absolute")
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}
	quoted, _ := json.Marshal(cwd) // JSON strings are TOML basic strings.
	config := `sandbox_mode = "danger-full-access"
approval_policy = "on-request"
web_search = "disabled"
notify = []
check_for_update_on_startup = false
project_doc_max_bytes = 0

[mcp_servers]
[hooks]
[plugins]

[features]
apps = false
plugins = false
multi_agent = false
hooks = false

[projects.` + string(quoted) + `]
trust_level = "untrusted"
`
	l := &ConfigLease{Home: home}
	for _, entry := range []struct{ name, body string }{{"config.toml", config}, {"AGENTS.md", instructions}} {
		path := filepath.Join(home, entry.name)
		// Reuse unchanged generated settings on resume; never import or silently
		// overwrite modified config. Sessions/auth elsewhere in this home survive.
		pin, err := writePinned(path, []byte(entry.body))
		if err != nil {
			l.Close()
			return nil, err
		}
		l.Pins = append(l.Pins, pin)
	}
	return l, nil
}
