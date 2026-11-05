package fleet

import (
	"github.com/blackcode-ch/tmx-go/internal/discover"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// AdoptRequest is POST /api/adopt.
type AdoptRequest struct {
	PID            int         `json:"pid"`
	Name           string      `json:"name,omitempty"`       // "" = derived from harness and folder
	ProjectID      string      `json:"project_id,omitempty"` // id or slug
	Level          model.Level `json:"level,omitempty"`      // worker (default) or lead
	ConversationID string      `json:"conversation_id,omitempty"`
	// Confirm: the owner agreed that the process is stopped (SIGTERM to that pid only).
	Confirm bool `json:"confirm"`
}

// Discover lists the agent processes on this machine that no tmx runs (owner only).
func (f *Fleet) Discover(c Caller) ([]discover.Agent, error) {
	return nil, errf(KindForbidden, "trial profile refuses discovery of outside agents")
}

// AdoptPlan is what an adopt would do: the process it stops and the agent it starts.
type AdoptPlan struct {
	Process      discover.Agent `json:"process"`
	Name         string         `json:"name"`
	Conversation string         `json:"conversation,omitempty"`

	lr LaunchRequest
}

// PlanAdopt validates an adopt without stopping anything (owner only): the pid is an
// adoptable agent outside tmx, its conversation is known, and the new agent may be launched.
func (f *Fleet) PlanAdopt(c Caller, req AdoptRequest) (AdoptPlan, error) {
	return AdoptPlan{}, errf(KindForbidden, "trial profile refuses adoption of outside agents")
}

// Adopt stops an outside agent process and resumes its conversation under tmx in chat
// mode (owner only). The process is signalled only once the owner confirmed, the agent was
// validated, and the pid is still the same process.
func (f *Fleet) Adopt(c Caller, req AdoptRequest) (model.Agent, error) {
	return model.Agent{}, errf(KindForbidden, "trial profile refuses adoption of outside agents")
}
