package fleet

import (
	"slices"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Caller is who makes a request: the owner, or one agent (by its token).
type Caller struct {
	Owner   bool
	AgentID string
	// Companion is the owner's desktop agent app through `tmx mcp --companion`: it may read the
	// fleet, Needs you and chats, and message the head (DESIGN.md "Companion"); the API lets it
	// reach nothing else. Never combined with Owner or AgentID.
	Companion bool
	// Peer is another fleet machine, authenticated by its pair token (Python's "peer" role):
	// a trusted architect/lead box operating this machine's agents (launch/screen/type/keys/
	// restart), same standing as the owner for those routes. Never combined with Owner,
	// AgentID or Companion. Empty for everyone else.
	Peer string
}

// CompanionCaller is Companion (the link token).
var CompanionCaller = Caller{Companion: true}

// OwnerCaller is the human.
var OwnerCaller = Caller{Owner: true}

// AgentCaller is an agent acting through its token.
func AgentCaller(id string) Caller { return Caller{AgentID: id} }

// PeerCaller is another fleet machine, authenticated by its pair token (a "full" role peer,
// never "cloud" or "guest": those stay boxed to their existing narrow routes).
func PeerCaller(name string) Caller { return Caller{Peer: name} }

// ID is the caller as a message party: "owner" or the agent ID.
func (c Caller) ID() string {
	if c.Owner {
		return model.Owner
	}
	if c.Companion {
		return model.CompanionParty
	}
	if c.Peer != "" {
		return model.Owner // a trusted peer acts with the owner's standing on this machine
	}
	return c.AgentID
}

// CallerForToken maps an agent token (from its env) to its Caller.
func (f *Fleet) CallerForToken(token string) (Caller, bool) {
	if token == "" {
		return Caller{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.tokens[token]
	if !ok {
		return Caller{}, false
	}
	return AgentCaller(id), true
}

// callerRecLocked returns the calling agent's record (nil for the owner, and nil for a
// trusted peer machine: it has no agent record on this machine, standing like the owner here).
func (f *Fleet) callerRecLocked(c Caller) (*agentRec, error) {
	if c.Owner || c.Peer != "" {
		return nil, nil
	}
	if c.Companion {
		return nil, errf(KindForbidden, "Companion cannot do this in tmx")
	}
	r, ok := f.agents[c.AgentID]
	if !ok {
		return nil, errf(KindForbidden, "the calling agent no longer exists")
	}
	return r, nil
}

// Policy returns the per-level policy, derived from the built-in profiles.
func (f *Fleet) Policy() model.Policy {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.policyLocked()
}

// SetPolicy writes a per-level policy into the built-in profiles (owner only): model, allowed
// models, permission mode, project creation and launch rights (a level in can_launch means
// its built-in profile; custom profiles already allowed for a level that stays are kept).
func (f *Fleet) SetPolicy(c Caller, p model.Policy) (model.Policy, error) {
	if !c.Owner {
		return model.Policy{}, errf(KindForbidden, "only the owner can change the policy")
	}
	if err := validatePolicy(p); err != nil {
		return model.Policy{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.profileListLocked()
	ps := make([]model.Profile, 0, len(levelOrder))
	for _, lv := range levelOrder {
		cur, ok := f.profiles[string(lv)]
		if !ok {
			continue
		}
		ps = append(ps, cur.Clone())
	}
	applyPolicy(ps, p, all)
	for _, prof := range ps {
		if err := f.validateProfileLocked(&prof); err != nil {
			return model.Policy{}, err
		}
	}
	for _, prof := range ps {
		prof.UpdatedAt = model.Now()
		if _, err := f.putProfileLocked(prof); err != nil {
			return model.Policy{}, err
		}
	}
	return f.policyLocked(), nil
}

func validatePolicy(p model.Policy) error {
	for _, lv := range []model.Level{model.LevelHead, model.LevelLead, model.LevelWorker} {
		lp, ok := p.Levels[lv]
		if !ok {
			return errf(KindInvalid, "the policy has no entry for level %s", lv)
		}
		if lp.Model == "" {
			return errf(KindInvalid, "the policy for %s has no default model", lv)
		}
		if !slices.Contains(lp.AllowedModels, lp.Model) {
			return errf(KindInvalid, "the default model of %s (%s) is not in its allowed models", lv, lp.Model)
		}
		for _, l := range lp.CanLaunch {
			if l != model.LevelLead && l != model.LevelWorker {
				return errf(KindInvalid, "%s cannot be allowed to launch %q agents", lv, l)
			}
		}
	}
	for lv := range p.Levels {
		if lv != model.LevelHead && lv != model.LevelLead && lv != model.LevelWorker {
			return errf(KindInvalid, "unknown level %q in policy", lv)
		}
	}
	return nil
}

// isDescendantLocked reports whether target sits below ancestor in the parent chain.
func (f *Fleet) isDescendantLocked(target *agentRec, ancestorID string) bool {
	seen := map[string]bool{}
	for id := target.a.ParentID; id != "" && !seen[id]; {
		if id == ancestorID {
			return true
		}
		seen[id] = true
		p, ok := f.agents[id]
		if !ok {
			return false
		}
		id = p.a.ParentID
	}
	return false
}

// authorizeActLocked: start/stop/restart/interrupt. The owner may act on anyone; a head or
// lead on its own subtree; nobody on itself.
func (f *Fleet) authorizeActLocked(c Caller, target *agentRec) error {
	if c.Owner || c.Peer != "" {
		return nil
	}
	if c.AgentID == target.a.ID {
		return errf(KindForbidden, "an agent cannot do this to itself")
	}
	me, err := f.callerRecLocked(c)
	if err != nil {
		return err
	}
	if me.a.Level == model.LevelWorker {
		return errf(KindForbidden, "workers cannot start or stop other agents")
	}
	if !f.isDescendantLocked(target, me.a.ID) {
		return errf(KindForbidden, "%s is not in %s's part of the tree", target.a.Name, me.a.Name)
	}
	return nil
}
