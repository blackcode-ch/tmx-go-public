package config

import (
	"path/filepath"
	"slices"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// WhatsAppConfig is config.json "whatsapp" (DESIGN.md "WhatsApp"); zero fields take the defaults.
type WhatsAppConfig struct {
	// Enabled: false turns the service off (every WhatsApp route answers 503); nil = off. While no
	// account is linked the service makes no connection at all.
	Enabled *bool `json:"enabled,omitempty"`

	ReadReceipts      bool                 `json:"read_receipts,omitempty"`
	AgentsSend        string               `json:"agents_send,omitempty"` // "ask" (default) or "allowed"
	AllowedChats      []string             `json:"allowed_chats,omitempty"`
	NotifyHeadChats   []string             `json:"notify_head_chats,omitempty"`
	WatchRoutes       []WhatsAppWatchRoute `json:"watch_routes,omitempty"`
	WatchAgentAuthors []string             `json:"watch_agent_authors,omitempty"`
	WatchConfig       string               `json:"watch_config,omitempty"`
	WatchSince        *time.Time           `json:"watch_since,omitempty"`
	WatchChatIDs      map[string]string    `json:"watch_chat_ids,omitempty"`
	WatchLocalAlias   string               `json:"watch_local_alias,omitempty"`

	// AgentProfiles may read WhatsApp and ask to send; nil = only the head ("head").
	AgentProfiles []string `json:"agent_profiles,omitempty"`
	// ChatAccess: per profile slug or "agent:<name>", the chats its agents may read and ask to
	// write to (model.WaSettings.ChatAccess).
	ChatAccess map[string]model.WaChatAccess `json:"chat_access,omitempty"`
	// PrivateChats: chats the head may not read (model.WaSettings.PrivateChats).
	PrivateChats []string `json:"private_chats,omitempty"`
	// MediaMaxMB caps one downloaded attachment; 0 = 64.
	MediaMaxMB int `json:"media_max_mb,omitempty"`
	// MediaRetentionDays expires cached files by write time; <=0 = 30 days.
	// Positive values are capped at 100 years to keep duration arithmetic bounded.
	MediaRetentionDays int `json:"media_retention_days,omitempty"`
	// MediaQuotaMB caps the entire media cache; <=0 = 2048 MB.
	MediaQuotaMB int `json:"media_quota_mb,omitempty"`
}

// WhatsAppWatchRoute forwards inbound text from one exact chat. Since sets the
// first eligible source timestamp, including messages synced after an outage.
type WhatsAppWatchRoute struct {
	Chat       string    `json:"chat"`
	Recipients []string  `json:"recipients"`
	Since      time.Time `json:"since"`
	Urgent     bool      `json:"urgent"`
}

// On reports whether the WhatsApp service runs (default false).
func (w *WhatsAppConfig) On() bool { return w != nil && w.Enabled != nil && *w.Enabled }

// Settings returns the owner's settings with the defaults filled in (w may be nil).
func (w *WhatsAppConfig) Settings() model.WaSettings {
	s := model.WaSettings{AgentsSend: model.WaAgentsAsk, AllowedChats: []string{}, NotifyHeadChats: []string{},
		AgentProfiles: []string{"head"}, ChatAccess: map[string]model.WaChatAccess{}, PrivateChats: []string{}}
	if w == nil {
		return s
	}
	s.ReadReceipts = w.ReadReceipts
	if w.AgentsSend == model.WaAgentsAllowed {
		s.AgentsSend = model.WaAgentsAllowed
	}
	if w.AllowedChats != nil {
		s.AllowedChats = slices.Clone(w.AllowedChats)
	}
	if w.NotifyHeadChats != nil {
		s.NotifyHeadChats = slices.Clone(w.NotifyHeadChats)
	}
	if w.AgentProfiles != nil {
		s.AgentProfiles = slices.Clone(w.AgentProfiles)
	}
	for k, a := range w.ChatAccess {
		s.ChatAccess[k] = model.WaChatAccess{Read: append([]string{}, a.Read...), Send: append([]string{}, a.Send...)}
	}
	if w.PrivateChats != nil {
		s.PrivateChats = slices.Clone(w.PrivateChats)
	}
	return s
}

// SetSettings writes the owner's settings into w (keeping enabled and the media cap).
func (w *WhatsAppConfig) SetSettings(s model.WaSettings) {
	w.ReadReceipts = s.ReadReceipts
	w.AgentsSend = s.AgentsSend
	w.AllowedChats = slices.Clone(s.AllowedChats)
	w.NotifyHeadChats = slices.Clone(s.NotifyHeadChats)
	w.AgentProfiles = slices.Clone(s.AgentProfiles)
	w.PrivateChats = slices.Clone(s.PrivateChats)
	w.ChatAccess = nil
	for k, a := range s.ChatAccess {
		if w.ChatAccess == nil {
			w.ChatAccess = map[string]model.WaChatAccess{}
		}
		w.ChatAccess[k] = model.WaChatAccess{Read: slices.Clone(a.Read), Send: slices.Clone(a.Send)}
	}
}

// MediaMax is the largest attachment the daemon downloads, in bytes.
func (w *WhatsAppConfig) MediaMax() int64 {
	if w == nil || w.MediaMaxMB <= 0 {
		return 64 << 20
	}
	return int64(min(w.MediaMaxMB, 2048)) << 20
}

func (w *WhatsAppConfig) MediaRetention() time.Duration {
	days := 30
	if w != nil && w.MediaRetentionDays > 0 {
		days = min(w.MediaRetentionDays, 36500)
	}
	return time.Duration(days) * 24 * time.Hour
}

func (w *WhatsAppConfig) MediaQuota() int64 {
	if w == nil || w.MediaQuotaMB <= 0 {
		return 2 << 30
	}
	return int64(min(w.MediaQuotaMB, 1<<20)) << 20
}

// WhatsAppDB is whatsmeow's own database: the linked device's keys and sessions. It is a
// secret (it can read and send as the owner), so it lives in secrets/, which the guard keeps
// every agent away from; never in git, logs or API output.
func (p Paths) WhatsAppDB() string { return filepath.Join(p.SecretsDir(), "whatsapp.db") }

// WhatsAppMediaDir holds attachments downloaded on demand: whatsapp/media/<chat>/<message id>.
func (p Paths) WhatsAppMediaDir() string { return filepath.Join(p.Home, "whatsapp", "media") }
