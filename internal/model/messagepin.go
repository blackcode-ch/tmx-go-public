package model

// MessagePin preserves the owner's selected message even after its timeline is trimmed.
// Text is a snapshot, not a new instruction or an attribution of authorship.
type MessagePin struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Label     string `json:"label"`
	Href      string `json:"href"`
	AgentID   string `json:"agent_id,omitempty"`
	Text      string `json:"text"`
	MessageAt int64  `json:"message_at"`
	PinnedAt  int64  `json:"pinned_at"`
	State     string `json:"state"`
}
