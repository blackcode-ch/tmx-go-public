package codexguard

import (
	"encoding/json"
	"fmt"
)

// ValidateEffective checks app-server config/read before any thread/model turn.
// Only tmx's own MCP command is accepted. This validates merged effective values,
// not just the generated file, because Codex also has project/system layers.
func ValidateEffective(result json.RawMessage, tmx string) error {
	var response struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if json.Unmarshal(result, &response) != nil || response.Config == nil {
		return fmt.Errorf("Codex did not report effective configuration")
	}
	c := response.Config
	for key, want := range map[string]string{"sandbox_mode": "danger-full-access", "approval_policy": "on-request", "web_search": "disabled"} {
		var got string
		if json.Unmarshal(c[key], &got) != nil || got != want {
			return fmt.Errorf("Codex effective %s is not the managed value", key)
		}
	}
	var notify []string
	if json.Unmarshal(c["notify"], &notify) != nil || len(notify) != 0 {
		return fmt.Errorf("Codex notification commands are not allowed")
	}
	for _, key := range []string{"hooks", "plugins"} {
		var entries map[string]json.RawMessage
		if len(c[key]) > 0 && json.Unmarshal(c[key], &entries) != nil {
			return fmt.Errorf("invalid Codex %s", key)
		}
		for _, entry := range entries {
			if configuredValue(entry) {
				return fmt.Errorf("unmanaged Codex %s are not allowed", key)
			}
		}
	}
	var features map[string]bool
	if json.Unmarshal(c["features"], &features) != nil {
		return fmt.Errorf("Codex did not report feature policy")
	}
	for _, key := range []string{"hooks", "plugins", "apps", "multi_agent"} {
		if value, ok := features[key]; !ok || value {
			return fmt.Errorf("Codex feature %s is not disabled", key)
		}
	}
	var servers map[string]struct {
		Enabled *bool             `json:"enabled"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		URL     string            `json:"url"`
		Env     map[string]string `json:"env"`
	}
	if len(c["mcp_servers"]) > 0 && json.Unmarshal(c["mcp_servers"], &servers) != nil {
		return fmt.Errorf("invalid Codex MCP configuration")
	}
	for name, server := range servers {
		if server.Enabled != nil && !*server.Enabled {
			continue
		}
		if name != "tmxg" || server.Command != tmx || server.URL != "" || len(server.Env) != 0 || len(server.Args) != 1 || server.Args[0] != "mcp" {
			return fmt.Errorf("unmanaged Codex MCP server is not allowed")
		}
	}
	return nil
}

func configuredValue(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	var active func(any) bool
	active = func(value any) bool {
		switch v := value.(type) {
		case nil:
			return false
		case bool:
			return v
		case string:
			return v != ""
		case float64:
			return v != 0
		case []any:
			for _, x := range v {
				if active(x) {
					return true
				}
			}
			return false
		case map[string]any:
			for _, x := range v {
				if active(x) {
					return true
				}
			}
			return false
		default:
			return true
		}
	}
	return active(value)
}
