package whatsapp

import (
	"encoding/json"
	"os"
	"slices"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

type legacyWatchConfig struct {
	Chats map[string]struct {
		Enabled   *bool    `json:"enabled"`
		Immediate bool     `json:"immediate"`
		Owners    []string `json:"owners"`
		UrgentTo  []string `json:"urgentTo"`
	} `json:"chats"`
	ExcludeChats          []string `json:"excludeChats"`
	AgentAuthors          []string `json:"agentAuthors"`
	Triggers              []string `json:"triggers"`
	BlindAlertTo          []string `json:"blindAlertTo"`
	DigestMinutes         int      `json:"digestMinutes"`
	UrgentThrottleMinutes int      `json:"urgentThrottleMinutes"`
	DryRun                bool     `json:"dryRun"`
}

// readLegacyWatchConfig reads routing only. Browser, token and attachment
// settings from the old watcher have no effect on the linked Go account.
func readLegacyWatchConfig(path string) (legacyWatchConfig, error) {
	var routing legacyWatchConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return routing, err
	}
	err = json.Unmarshal(data, &routing)
	return routing, err
}

func watchLocalAddress(address, alias string) string {
	name, host, ok := strings.Cut(strings.TrimSpace(address), "@")
	if ok && alias != "" && strings.EqualFold(alias, host) {
		return name
	}
	return strings.TrimSpace(address)
}

func excludedWatchChat(excluded []string, title, id string) bool {
	return slices.ContainsFunc(excluded, func(name string) bool {
		return strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(title)) || name == id
	})
}

func (s *Service) watchChatID(cfg *config.WhatsAppConfig, title string) (string, error) {
	if id := cfg.WatchChatIDs[title]; id != "" {
		return id, nil
	}
	if strings.HasSuffix(title, "@g.us") || strings.HasSuffix(title, "@s.whatsapp.net") || strings.HasSuffix(title, "@lid") {
		return title, nil
	}
	return s.o.Store.WaWatchChatID(title)
}
