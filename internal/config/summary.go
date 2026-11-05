package config

// SummaryConfig is config.json "summaries": Claude's summaries of finished recordings (DESIGN.md
// "Summaries"). Every field is optional; internal/summarize fills the defaults.
type SummaryConfig struct {
	// Auto summarizes every finished recording by itself; nil = on.
	Auto *bool `json:"auto,omitempty"`
	// Model is the Claude model that writes them ("" = the cheapest Haiku tmx knows).
	Model string `json:"model,omitempty"`
	// Backend: "" or "claude" = the cheap cloud model (Model); "local" = the local model first (LocalURL, or ViaPeer),
	// the cloud model when there is no local route. A recording tagged "clinic" is written by local models only.
	Backend string `json:"backend,omitempty"`
	// LocalModel is the Ollama model ("" = gemma4:e4b); LocalURL its loopback address ("" = http://127.0.0.1:11434).
	LocalModel string `json:"local_model,omitempty"`
	LocalURL   string `json:"local_url,omitempty"`
	// ViaPeer names a fleet peer (config "peers") whose daemon runs the local model for this one (a machine without
	// room for Ollama), over its pair-token route. "" = this machine's Ollama. Clinic recordings go there too (a
	// machine of our own fleet counts as local): on a machine that records clinic sessions whose patient data must
	// stay on that very machine, leave via_peer empty.
	ViaPeer string `json:"via_peer,omitempty"`
	// ServePeers are the peers this daemon runs its local model for (POST /api/peer/summarize-local); nil = none.
	ServePeers []string `json:"serve_peers,omitempty"`
}

// AutoOn reports config summaries.auto (default on).
func (c SummaryConfig) AutoOn() bool { return c.Auto == nil || *c.Auto }
