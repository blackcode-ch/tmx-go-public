package model

type ProcessCategory string

const (
	CategoryAgent      ProcessCategory = "agent"
	CategoryAgentTool  ProcessCategory = "agent_tool"
	CategorySystemTool ProcessCategory = "system_tool"
)

// ProcessInfo contains no arguments or environment: either may contain secrets.
type ProcessInfo struct {
	ID        string          `json:"id"`
	Category  ProcessCategory `json:"category"`
	OwnerID   string          `json:"owner_id"`
	CreatorID string          `json:"creator_id,omitempty"`
	Promoted  bool            `json:"promoted,omitempty"`
	ProjectID string          `json:"project_id,omitempty"`
	Purpose   string          `json:"purpose"`
	StartedAt int64           `json:"started_at"`
	PID       int             `json:"pid"`
	Command   string          `json:"command"`
	State     string          `json:"state"`
	Restarts  int             `json:"restarts"`
}
