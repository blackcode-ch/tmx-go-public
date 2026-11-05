package config

const TrialServicesWarning = "TRIAL PROFILE: WhatsApp or peers are configured but disabled by this profile. Select the production profile to use these services."

// ProfileWarnings examines the requested configuration before trial defaults
// disable services. It contains no account, endpoint or credential details.
func (c Config) ProfileWarnings() []string {
	if c.Production() {
		return nil
	}
	w := c.WhatsApp
	configured := w != nil && (w.On() || w.ReadReceipts || w.AgentsSend != "" || len(w.AllowedChats) > 0 || len(w.NotifyHeadChats) > 0 || len(w.AgentProfiles) > 0)
	if configured || len(c.Peers) > 0 {
		return []string{TrialServicesWarning}
	}
	return nil
}
