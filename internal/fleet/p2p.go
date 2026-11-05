package fleet

import (
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

const ToolP2PInvite = "p2p_invite_create"
const ToolP2PJoin = "p2p_join"

// CheckP2PAgent checks the authenticated local agent, never a supplied name or tier.
func (f *Fleet) CheckP2PAgent(c Caller) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.callerRecLocked(c)
	if err != nil {
		return err
	}
	if c.Owner || c.Peer != "" || r == nil || (r.a.Level != model.LevelHead && r.a.Level != model.LevelLead) {
		return errf(KindForbidden, "only head and lead agents may use pairing tools")
	}
	if r.proc == nil || r.stopping || r.a.Desired == "stopped" || f.profileForLocked(r.a).ReadOnly {
		return errf(KindForbidden, "pairing requires a running, writable head or lead")
	}
	return nil
}

// HoldP2P shares phone approvals and Safety's revocable standing grants. Command
// is the tool name deliberately: Allow always covers this tool, not a past link.
// These fixed rules are NOT Rememberable: Allow once never becomes Allow always.
func (f *Fleet) HoldP2P(c Caller, tool, description string) (model.Approval, error) {
	if err := f.CheckP2PAgent(c); err != nil {
		return model.Approval{}, err
	}
	rule := guard.RuleP2PInvite
	if tool == ToolP2PJoin {
		rule = guard.RuleP2PJoin
	} else if tool != ToolP2PInvite {
		return model.Approval{}, errf(KindInvalid, "unknown pairing tool")
	}
	return f.hold(c, HoldRequest{Tool: tool, Command: tool, Rule: rule, Detail: description, Match: description})
}
