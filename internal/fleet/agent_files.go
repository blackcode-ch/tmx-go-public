package fleet

import "github.com/blackcode-ch/tmx-go/internal/model"

// AgentForFiles checks access before exposing folder contents or conversation
// items. Peer identity does not grant filesystem access.
func (f *Fleet) AgentForFiles(c Caller, id string) (model.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.resolveLocked(id)
	if err != nil {
		return model.Agent{}, err
	}
	if c.Peer != "" || c.Companion {
		return model.Agent{}, errf(KindForbidden, "local agent hierarchy or owner required")
	}
	if !c.Owner && c.AgentID != r.a.ID {
		if err := f.authorizeActLocked(c, r); err != nil {
			return model.Agent{}, err
		}
	}
	return r.a, nil
}
