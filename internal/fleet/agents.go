package fleet

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// LaunchRequest is POST /api/agents. ProjectID and ParentID also accept a slug / a name.
type LaunchRequest struct {
	Name      string        `json:"name"`
	ProjectID string        `json:"project_id,omitempty"`
	ParentID  string        `json:"parent_id,omitempty"`
	Level     model.Level   `json:"level"`
	Profile   string        `json:"profile,omitempty"` // "" = the built-in profile of the level
	Harness   model.Harness `json:"harness,omitempty"`
	Account   string        `json:"account,omitempty"` // the harness account; "" = the harness's default
	Mode      model.Mode    `json:"mode,omitempty"`
	Model     string        `json:"model,omitempty"`
	Cwd       string        `json:"cwd,omitempty"`
	Role      string        `json:"role,omitempty"`
	Brief     string        `json:"brief,omitempty"`
	Prompt    string        `json:"prompt,omitempty"`    // first user turn; "" = the brief
	ThreadID  string        `json:"thread_id,omitempty"` // the Matrix thread it works on; "" = the launcher's topic or its project's

	CodexFullAccess bool `json:"codex_full_access,omitempty"`
	AgyFullAccess   bool `json:"agy_full_access,omitempty"`

	// Resource control: over the machine's limits a launch is queued, unless
	NoWait bool `json:"no_wait,omitempty"` // refuse instead of queueing
	Force  bool `json:"force,omitempty"`   // owner only: start anyway
}

// HeadName is the name of the machine's single head agent.
const HeadName = "head"

// LaunchAgent creates an agent (policy-checked for the caller) and starts it.
func (f *Fleet) LaunchAgent(c Caller, req LaunchRequest) (model.Agent, error) {
	f.mu.Lock()
	a, err := f.prepareLaunchLocked(c, req)
	if err == nil && req.NoWait && !(req.Force && c.Owner) {
		err = f.capacityLocked(req.Name)
	}
	var thread string
	if err == nil {
		thread, err = f.launchThreadLocked(c, req, a)
	}
	if err != nil {
		f.mu.Unlock()
		return model.Agent{}, err
	}
	r := &agentRec{a: a}
	f.agents[a.ID] = r
	f.saveAgentLocked(r)
	if a.AgyFullAccess {
		f.log.Warn("owner opted agy agent into full access", "agent", a.ID, "name", a.Name)
		f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Text: "Owner opted this agy agent into skipping permission prompts. This opt-in resets when the daemon restarts."})
	}
	launcher, _ := f.callerRecLocked(c)
	f.linkLaunchLocked(linkerName(launcher), thread, a)
	if a.CodexFullAccess {
		f.log.Warn("owner opted Codex agent into full access", "agent", a.ID, "name", a.Name, "cwd", a.Cwd)
	}
	f.mu.Unlock()

	prompt := req.Prompt
	if prompt == "" {
		prompt = req.Brief
	}
	o := startOpts{StartOptions: StartOptions{Force: req.Force && c.Owner}}
	_ = f.startAgentOpts(a.ID, prompt, o) // a failed start leaves the agent in state error
	return f.Agent(a.ID)
}

// prepareLaunchLocked validates req against the hierarchy and the policy and returns the
// new agent (not yet registered).
func (f *Fleet) prepareLaunchLocked(c Caller, req LaunchRequest) (model.Agent, error) {
	me, err := f.callerRecLocked(c)
	if err != nil {
		return model.Agent{}, err
	}
	if req.CodexFullAccess && (!c.Owner || req.Harness != model.HarnessCodex) {
		return model.Agent{}, errf(KindForbidden, "Codex full access requires an explicit owner opt-in for a Codex agent")
	}
	if req.AgyFullAccess && (!c.Owner || c.Companion || c.AgentID != "" || (req.Harness != "agy" && req.Harness != "antigravity")) {
		return model.Agent{}, errf(KindForbidden, "agy full access requires an explicit owner opt-in for an agy agent")
	}
	if !model.ValidName(req.Name) || ReservedAgentName(req.Name) {
		return model.Agent{}, errf(KindInvalid, "%q is not a valid agent name (lowercase letters, digits and dashes, up to 40)", req.Name)
	}
	if _, err := f.resolveLocked(req.Name); err == nil {
		return model.Agent{}, errf(KindConflict, "an agent named %q already exists", req.Name)
	}
	prof, err := f.launchProfileLocked(req.Profile, req.Level)
	if err != nil {
		return model.Agent{}, err
	}
	level := prof.Level

	var proj *model.Project
	if req.ProjectID != "" {
		if proj, err = f.projectLocked(req.ProjectID); err != nil {
			return model.Agent{}, errf(KindInvalid, "%s", err.Error())
		}
	}
	var parent *agentRec
	if req.ParentID != "" {
		if parent, err = f.resolveLocked(req.ParentID); err != nil {
			return model.Agent{}, errf(KindInvalid, "parent: %s", err.Error())
		}
	}

	if me != nil {
		if mp := f.profileForLocked(me.a); !slices.Contains(mp.CanLaunch, prof.Slug) {
			return model.Agent{}, errf(KindForbidden, "%s (profile %s) cannot launch %s agents", me.a.Name, mp.Slug, prof.Slug)
		}
		if me.a.Level == model.LevelLead {
			// An explicitly assigned lead can serve several projects without moving.
			if proj == nil {
				if me.a.ProjectID == "" {
					return model.Agent{}, errf(KindForbidden, "a lead without a project cannot launch agents: ask the owner, the head or a project's lead to add you to a project (project_member_add), then launch there")
				}
				proj = f.projects[me.a.ProjectID]
			}
			if proj == nil || (proj.ID != me.a.ProjectID && proj.LeadID != me.a.ID) {
				return model.Agent{}, errf(KindForbidden, "a lead can launch agents only in its own project or one it leads")
			}
		}
		if parent == nil {
			parent = me
		} else if parent != me && !f.isDescendantLocked(parent, me.a.ID) {
			return model.Agent{}, errf(KindForbidden, "%s is not in %s's part of the tree", parent.a.Name, me.a.Name)
		}
	}
	if parent == nil {
		if level == model.LevelWorker && proj != nil && proj.LeadID != "" {
			parent = f.agents[proj.LeadID]
		}
		if parent == nil {
			parent = f.headLocked()
		}
	}
	if level == model.LevelLead && proj != nil && proj.LeadID != "" {
		if _, ok := f.agents[proj.LeadID]; ok || IsPeer(proj.LeadID) {
			return model.Agent{}, errf(KindConflict, "project %s already has a lead", proj.Name)
		}
	}
	if proj == nil && parent != nil && parent.a.ProjectID != "" {
		proj = f.projects[parent.a.ProjectID]
	}

	harness, mode, err := f.harnessModeLocked(req.Harness, req.Mode)
	if err != nil {
		return model.Agent{}, err
	}
	// A shell runs whatever is typed into it, with no model, no guard and no approvals: it is
	// the owner's own terminal. An agent has no use for one (mail is never typed into a shell)
	// and must not get one to type commands into (DESIGN.md "Fleet control").
	if harness == model.HarnessShell && !c.Owner {
		return model.Agent{}, errf(KindForbidden, "only the owner can launch a shell")
	}
	mdl, err := f.pickHarnessModelLocked(harness, prof, req.Model, me == nil)
	if err != nil {
		return model.Agent{}, err
	}
	if err := harnessProfileOK(harness, prof); err != nil {
		return model.Agent{}, err
	}

	base := f.opts.Workspace
	if proj != nil {
		base = proj.Folder
	}
	cwd := req.Cwd
	switch {
	case cwd == "":
		cwd = base
	case !filepath.IsAbs(cwd):
		cwd = filepath.Join(base, cwd)
	}

	now := model.Now()
	account, err := f.launchAccountLocked(harness, req.Account)
	if err != nil {
		return model.Agent{}, err
	}
	a := model.Agent{
		ID: model.NewID(), Name: req.Name, Level: level, Profile: prof.Slug, Harness: harness, Account: account, Mode: mode, Model: mdl,
		Cwd: filepath.Clean(cwd), Role: req.Role, Brief: req.Brief,
		CodexFullAccess: req.CodexFullAccess,
		AgyFullAccess:   req.AgyFullAccess,
		State:           model.StateStopped, Desired: "running", CreatedAt: now, UpdatedAt: now,
	}
	if proj != nil {
		a.ProjectID = proj.ID
	}
	if parent != nil {
		a.ParentID = parent.a.ID
	}
	if err := f.launchAdmissionLocked(a); err != nil {
		return model.Agent{}, err
	}
	if level == model.LevelWorker && harness != model.HarnessShell {
		if proj == nil && parent != nil && parent.a.Level == model.LevelHead {
			proj, err = f.generalProjectLocked(parent)
			if err != nil {
				return model.Agent{}, err
			}
			a.ProjectID = proj.ID
		}
		var lead *agentRec
		if proj != nil {
			lead = f.agents[proj.LeadID]
		}
		headLead := lead != nil && lead.a.Level == model.LevelHead
		if lead == nil || (!headLead && lead.a.Level != model.LevelLead) {
			return model.Agent{}, errf(KindInvalid, "a worker needs a project with a lead; select or create a project, or launch from its lead")
		}
		if parent == nil || parent.a.Level == model.LevelHead {
			parent = lead
			a.ParentID = parent.a.ID
		}
		if parent.a.ProjectID != proj.ID && parent != lead {
			return model.Agent{}, errf(KindInvalid, "a worker and its parent must belong to the same project")
		}
	}

	return a, nil
}

// generalProjectLocked gives the head's one-off workers a durable home without
// creating another agent or changing the head's machine-wide scope.
func (f *Fleet) generalProjectLocked(head *agentRec) (*model.Project, error) {
	for _, p := range f.projects {
		if p.LeadID == head.a.ID {
			return p, nil
		}
	}
	slug := "general"
	for n := 0; ; n++ {
		used := false
		for _, p := range f.projects {
			used = used || p.Slug == slug
		}
		if !used {
			break
		}
		slug += "-head"
	}
	now := model.Now()
	p := &model.Project{ID: model.NewID(), Name: "General", Slug: slug,
		Folder: f.opts.Workspace, LeadID: head.a.ID, Status: model.ProjectActive,
		CreatedAt: now, UpdatedAt: now}
	if err := f.saveProjectLocked(p); err != nil {
		return nil, err
	}
	f.projects[p.ID] = p
	f.projectThreadLocked(p.ID)
	return p, nil
}

// ensureHeadLocked creates the head if missing and reports whether it was created.
func (f *Fleet) ensureHeadLocked() (*agentRec, bool, error) {
	if h := f.headLocked(); h != nil {
		if err := f.migrateHeadHarnessLocked(h); err != nil {
			return nil, false, err
		}
		return h, false, nil
	}
	if r, err := f.resolveLocked(HeadName); err == nil {
		return nil, false, errf(KindConflict, "an agent named %q exists but is a %s, not the head", HeadName, r.a.Level)
	}
	prof := f.profileForLocked(model.Agent{Level: model.LevelHead, Profile: HeadName})
	headHarness, configured := f.configuredHeadHarness()
	if !configured {
		headHarness = model.HarnessClaude
	}
	mdl := prof.Model
	if headHarness != model.HarnessClaude {
		mdl = "" // let the harness pick its own default model
	}
	now := model.Now()
	r := &agentRec{a: model.Agent{
		ID: model.NewID(), Name: HeadName, Level: model.LevelHead, Profile: prof.Slug, Harness: headHarness,
		Mode: model.ModeChat, Model: mdl, Cwd: f.opts.Workspace, State: model.StateStopped,
		Desired: "running", CreatedAt: now, UpdatedAt: now,
	}}
	// Register the head even when launch policy refuses it, so the daemon and
	// owner controls remain available. Start quarantines refused agents.
	f.agents[r.a.ID] = r
	f.saveAgentLocked(r)
	return r, true, nil
}

// migrateHeadHarnessLocked applies an explicitly configured head harness (TMX_HEAD_HARNESS, or
// config.json "head_harness") to an existing head during startup. The head's database identity and timeline stay in place; model and
// session IDs belong to the old harness and must not be passed to the new one.
func (f *Fleet) migrateHeadHarnessLocked(r *agentRec) error {
	desired, configured := f.configuredHeadHarness()
	if !configured || desired == r.a.Harness {
		return nil
	}
	next := r.a
	next.Harness = desired
	if desired == model.HarnessClaude {
		next.Model = f.profileForLocked(r.a).Model
	} else {
		next.Model = ""
	}
	next.SessionID = ""
	next.UpdatedAt = model.Now()
	if err := f.st.PutAgent(next); err != nil {
		return err
	}
	r.a = next
	r.sessionConfirmed = false
	f.bus.Publish(model.BusEvent{Type: model.EvAgentUpdated, AgentID: next.ID, ProjectID: next.ProjectID, Data: next})
	return nil
}

// configuredHeadHarness is the head's harness when one is set explicitly: TMX_HEAD_HARNESS, else
// config.json "head_harness". Unset or not a chat harness: none, and an existing head keeps its own.
func (f *Fleet) configuredHeadHarness() (model.Harness, bool) {
	for _, h := range []string{os.Getenv("TMX_HEAD_HARNESS"), f.opts.HeadHarness} {
		if h == "" {
			continue
		}
		if info, ok := harness.Get(model.Harness(h)); ok && info.Supports(model.ModeChat) {
			return model.Harness(h), true
		}
		return "", false
	}
	return "", false
}
