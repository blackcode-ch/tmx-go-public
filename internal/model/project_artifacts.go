package model

// ProjectOutput is a reported deliverable, not permission to open or serve a file.
type ProjectOutput struct {
	Path string `json:"path"`
	What string `json:"what"`
}

// ProjectContribution belongs to a stable agent ID, even after renaming/removal.
type ProjectContribution struct {
	ProjectID   string          `json:"project_id"`
	AgentID     string          `json:"agent_id"`
	AgentName   string          `json:"agent_name"`
	Status      string          `json:"status"`
	FinalOutput []ProjectOutput `json:"final_output"`
	UpdatedAt   int64           `json:"updated_at"`
}

type ProjectArtifact struct {
	ProjectOutput
	Kind    string `json:"kind"`               // link or path; never an existence claim
	AgentID string `json:"agent_id,omitempty"` // empty for the shared project output
}

type ProjectArtifacts struct {
	ProjectID     string                `json:"project_id"`
	FinalOutput   []ProjectArtifact     `json:"final_output"`
	Contributions []ProjectContribution `json:"contributions"`
}
