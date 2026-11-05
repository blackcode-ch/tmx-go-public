package codexguard

import (
	"encoding/json"
	"testing"
)

func TestEffectiveConfigRejectsOtherChannels(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"sandbox_mode": "danger-full-access", "approval_policy": "on-request", "web_search": "disabled", "notify": []string{}, "features": map[string]bool{"hooks": false, "plugins": false, "apps": false, "multi_agent": false}, "mcp_servers": map[string]any{}}
	}
	for _, test := range []struct {
		name, key string
		value     any
	}{
		{"notification", "notify", []string{"evil"}},
		{"mcp", "mcp_servers", map[string]any{"other": map[string]any{"command": "evil"}}},
		{"hooks", "hooks", map[string]any{"PreToolUse": []string{"evil"}}},
		{"sandbox", "sandbox_mode", "workspace-write"},
		{"web", "web_search", "live"},
		{"feature", "features", map[string]bool{"hooks": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := base()
			cfg[test.key] = test.value
			b, _ := json.Marshal(map[string]any{"config": cfg})
			if ValidateEffective(b, "tmx.exe") == nil {
				t.Fatal("unmanaged config accepted")
			}
		})
	}
	b, _ := json.Marshal(map[string]any{"config": base()})
	if err := ValidateEffective(b, "tmx.exe"); err != nil {
		t.Fatal(err)
	}
}
