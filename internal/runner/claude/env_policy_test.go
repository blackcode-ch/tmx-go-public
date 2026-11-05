package claude

import (
	"strings"
	"testing"
)

func TestAgentEnvDropsInheritedCredentials(t *testing.T) {
	t.Setenv("GH_TOKEN", "owner-fixture")
	t.Setenv("TMXG_AGENT_TOKEN", "parent-fixture")
	for _, kv := range BaseEnv() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(key, "GH_TOKEN") || strings.EqualFold(key, "TMXG_AGENT_TOKEN") {
			t.Fatalf("inherited %s", key)
		}
	}
}

func TestAgentEnvFiltersExtrasAndPreservesOwnToken(t *testing.T) {
	got := MergeEnv([]string{"PATH=bin"}, "TMXG_AGENT_TOKEN=child-fixture", "GH_TOKEN=owner-fixture", "SERVICE_API_KEY=fixture")
	if len(got) != 2 || got[1] != "TMXG_AGENT_TOKEN=child-fixture" {
		t.Fatal("agent environment did not filter credentials")
	}
}
