package config

import (
	"path/filepath"
	"slices"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// WhatsAppConfig is config.json "whatsapp" (DESIGN.md "WhatsApp"); zero fields take the defaults.
type WhatsAppConfig struct {
	// Enabled: false turns the service off (every WhatsApp route answers 503); nil = off. While no
	// account is linked the service makes no connection at all.
	Enabled *bool `json:"enabled,omitempty"`

	ReadReceipts    bool     `json:"read_receipts,omitempty"`
	AgentsSend      string   `json:"agents_send,omitempty"` // "ask" (default) or "allowed"
	AllowedChats    []string `json:"allowed_chats,omitempty"`
	NotifyHeadChats []string `json:"notify_head_chats,omitempty"`
	// AgentProfiles may read WhatsApp and ask to send; nil = only the head ("head").
	AgentProfiles []string `json:"agent_profiles,omitempty"`
	// MediaMaxMB caps one downloaded attachment; 0 = 64.
	MediaMaxMB int `json:"media_max_mb,omitempty"`
}

// On reports whether the WhatsApp service runs (default false).
func (w *WhatsAppConfig) On() bool { return w != nil && w.Enabled != nil && *w.Enabled }

// Settings returns the owner's settings with the defaults filled in (w may be nil).
func (w *WhatsAppConfig) Settings() model.WaSettings {
	s := model.WaSettings{AgentsSend: model.WaAgentsAsk, AllowedChats: []string{}, NotifyHeadChats: []string{},
		AgentProfiles: []string{"head"}}
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
	return s
}

// SetSettings writes the owner's settings into w (keeping enabled and the media cap).
func (w *WhatsAppConfig) SetSettings(s model.WaSettings) {
	w.ReadReceipts = s.ReadReceipts
	w.AgentsSend = s.AgentsSend
	w.AllowedChats = slices.Clone(s.AllowedChats)
	w.NotifyHeadChats = slices.Clone(s.NotifyHeadChats)
	w.AgentProfiles = slices.Clone(s.AgentProfiles)
}

// MediaMax is the largest attachment the daemon downloads, in bytes.
func (w *WhatsAppConfig) MediaMax() int64 {
	if w == nil || w.MediaMaxMB <= 0 {
		return 64 << 20
	}
	return int64(min(w.MediaMaxMB, 2048)) << 20
}

// WhatsAppDB is whatsmeow's own database: the linked device's keys and sessions. It is a
// secret (it can read and send as the owner), so it lives in secrets/, which the guard keeps
// every agent away from; never in git, logs or API output.
func (p Paths) WhatsAppDB() string { return filepath.Join(p.SecretsDir(), "whatsapp.db") }

// WhatsAppMediaDir holds attachments downloaded on demand: whatsapp/media/<chat>/<message id>.
func (p Paths) WhatsAppMediaDir() string { return filepath.Join(p.Home, "whatsapp", "media") }
