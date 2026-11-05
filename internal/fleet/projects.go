package fleet

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/peertext"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// maxSlug keeps "<slug>-lead" within the 40-character name limit.
const maxSlug = 34

// ProjectRequest is POST /api/projects.
type ProjectRequest struct {
	// Lead omitted launches a local lead; empty or "none" leaves it unassigned.
	// Otherwise this selects an existing local lead/head or a name@host peer.
	Lead   *string `json:"lead,omitempty"`
	Name   string  `json:"name"`
	Folder string  `json:"folder,omitempty"`
	Brief  string  `json:"brief,omitempty"`
	// Profile of the project's lead; "" = the built-in lead profile.
	Profile string `json:"profile,omitempty"`
	Model   string `json:"model,omitempty"`
	// Harness for the project's lead; "" preserves the Claude default.
	Harness model.Harness `json:"harness,omitempty"`
	// Members: existing agents (names or IDs) in no project yet that join it at once, e.g. a lead
	// that must launch workers here (AddProjectMember).
	Members []string `json:"members,omitempty"`
}

// ProjectPatch is PATCH /api/projects/{id}; nil fields stay.
type ProjectPatch struct {
	Name   *string              `json:"name,omitempty"`
	Brief  *string              `json:"brief,omitempty"`
	Status *model.ProjectStatus `json:"status,omitempty"`
}

// Slugify turns a project name into its slug: lowercase letters, digits and dashes.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > maxSlug {
		s = strings.Trim(s[:maxSlug], "-")
	}
	return s
}

// CreateProject creates a project and either launches a lead or records an explicit
// assignment. Only the owner or a profile allowed to create projects may call it.
func (f *Fleet) CreateProject(c Caller, req ProjectRequest) (model.Project, error) {
	// Resolve before touching the project folder or store. In particular, do not silently
	// start the lead with Claude when the caller selected an unknown harness.
	f.mu.Lock()
	leadHarness := model.HarnessClaude
	var err error
	if req.Lead == nil {
		leadHarness, _, err = f.harnessModeLocked(req.Harness, model.ModeChat)
	} else if req.Harness != "" || req.Profile != "" || req.Model != "" {
		err = errf(KindInvalid, "harness, profile and model apply only when launching a new lead")
	}
	if err != nil {
		f.mu.Unlock()
		return model.Project{}, err
	}
	slug := Slugify(req.Name)
	if slug == "" {
		f.mu.Unlock()
		return model.Project{}, errf(KindInvalid, "the project needs a name with letters or digits")
	}
	leadName := slug + "-lead"

	me, err := f.callerRecLocked(c)
	if err != nil {
		f.mu.Unlock()
		return model.Project{}, err
	}
	if me != nil {
		mp := f.profileForLocked(me.a)
		if !mp.CanCreateProjects {
			f.mu.Unlock()
			return model.Project{}, errf(KindForbidden, "%s (profile %s) cannot create projects", me.a.Name, mp.Slug)
		}
		if req.Profile != "" && !slices.Contains(mp.CanLaunch, req.Profile) {
			f.mu.Unlock()
			return model.Project{}, errf(KindForbidden, "%s (profile %s) cannot launch %s agents", me.a.Name, mp.Slug, req.Profile)
		}
	}
	if req.Lead == nil {
		if _, err := f.launchProfileLocked(req.Profile, model.LevelLead); err != nil {
			f.mu.Unlock()
			return model.Project{}, err
		}
	}
	selectedLead := ""
	if req.Lead != nil {
		selectedLead, err = f.projectLeadLocked(*req.Lead)
		if err != nil {
			f.mu.Unlock()
			return model.Project{}, err
		}
	}
	for _, p := range f.projects {
		if p.Slug == slug {
			f.mu.Unlock()
			return model.Project{}, errf(KindConflict, "a project %q already exists", slug)
		}
	}
	if _, err := f.resolveLocked(leadName); req.Lead == nil && err == nil {
		f.mu.Unlock()
		return model.Project{}, errf(KindConflict, "an agent named %q already exists", leadName)
	}
	// Members are checked before anything is created, so a bad name creates nothing.
	var members []*agentRec
	for _, m := range req.Members {
		r, err := f.memberLocked(m, "")
		if err != nil {
			f.mu.Unlock()
			return model.Project{}, err
		}
		if r != nil && !slices.Contains(members, r) {
			members = append(members, r)
		}
	}
	folder := req.Folder
	switch {
	case folder == "":
		folder = filepath.Join(f.opts.Workspace, slug)
	case strings.HasPrefix(folder, "~"):
		if home, err := os.UserHomeDir(); err == nil {
			folder = filepath.Join(home, folder[1:])
		}
	case !filepath.IsAbs(folder):
		folder = filepath.Join(f.opts.Workspace, folder)
	}
	folder = filepath.Clean(folder)
	validateFolder := func() error {
		if req.Lead == nil {
			return f.launchAdmissionLocked(model.Agent{Harness: leadHarness, Level: model.LevelLead, Profile: req.Profile, Model: req.Model, Cwd: folder})
		}
		if err := harness.ValidateLaunchCwd(folder); err != nil {
			return errf(KindForbidden, "%s", err)
		}
		return nil
	}
	if err := validateFolder(); err != nil {
		f.mu.Unlock()
		return model.Project{}, err
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		f.mu.Unlock()
		return model.Project{}, errf(KindInvalid, "cannot create the folder %s: %v", folder, err)
	}
	if err := validateFolder(); err != nil {
		f.mu.Unlock()
		return model.Project{}, err
	}
	now := model.Now()
	p := &model.Project{ID: model.NewID(), Name: strings.TrimSpace(req.Name), Slug: slug, Folder: folder,
		Brief: req.Brief, LeadID: selectedLead, Status: model.ProjectActive, CreatedAt: now, UpdatedAt: now}
	if err := f.saveProjectLocked(p); err != nil {
		f.mu.Unlock()
		return model.Project{}, err
	}
	f.projects[p.ID] = p
	f.projectThreadLocked(p.ID)

	if req.Lead != nil {
		if r := f.agents[selectedLead]; r != nil {
			_, linkErr := f.addLinkLocked(model.ThreadLink{ThreadID: f.projectThreadLocked(p.ID), Axis: model.AxisAgent, Ref: r.a.ID, Role: "lead", Label: r.a.Name, Why: "assigned to this project", By: model.Owner, How: model.LinkAuto})
			if linkErr != nil {
				f.log.Error("matrix: link assigned lead", "err", linkErr)
			}
		}
		by := model.Owner
		if me != nil {
			by = me.a.Name
		}
		for _, r := range members {
			f.joinProjectLocked(by, p, r)
		}
		out := *p
		f.mu.Unlock()
		return out, nil
	}

	// The lead is launched by the system on behalf of the head, using the requested harness.
	lreq := LaunchRequest{Name: leadName, ProjectID: p.ID, Level: model.LevelLead, Profile: req.Profile, Model: req.Model, Harness: leadHarness, Brief: req.Brief}
	if h := f.headLocked(); h != nil {
		lreq.ParentID = h.a.ID
	}
	a, err := f.prepareLaunchLocked(OwnerCaller, lreq)
	if err != nil {
		delete(f.projects, p.ID)
		_ = f.st.DeleteProject(p.ID)
		f.bus.Publish(model.BusEvent{Type: model.EvProjectRemoved, ProjectID: p.ID, Data: map[string]string{"id": p.ID}})
		f.mu.Unlock()
		return model.Project{}, err
	}
	lead := &agentRec{a: a}
	f.agents[a.ID] = lead
	f.saveAgentLocked(lead)
	f.linkLaunchLocked(model.Owner, f.projectThreadLocked(p.ID), a)
	p.LeadID = a.ID
	if err := f.saveProjectLocked(p); err != nil {
		f.log.Error("store project", "err", err)
	}
	by := model.Owner
	if me != nil {
		by = me.a.Name
	}
	for _, r := range members {
		f.joinProjectLocked(by, p, r)
	}
	out := *p
	f.mu.Unlock()

	_ = f.startAgent(a.ID, leadKickoff(out))
	return out, nil
}

// AddProjectMember puts an existing agent into a project (DESIGN.md "Projects", members), so a
// lead in it can launch workers there. Only the owner, the head or the project's own lead may add
// one: never an agent on its own behalf, nor a peer machine. An agent belongs to one project: one
// already in another is refused; adding it again is a no-op. Takes effect on its next launch.
func (f *Fleet) AddProjectMember(c Caller, project, agent string) (model.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.projectLocked(project)
	if err != nil {
		return model.Agent{}, err
	}
	by := model.Owner
	if !c.Owner {
		if c.Peer != "" {
			return model.Agent{}, errf(KindForbidden, "only the owner, the head or the project's lead can add members to %s", p.Name)
		}
		me, err := f.callerRecLocked(c)
		if err != nil {
			return model.Agent{}, err
		}
		if !isHead(me) && me.a.ID != p.LeadID {
			return model.Agent{}, errf(KindForbidden, "only the owner, the head or the project's lead can add members to %s", p.Name)
		}
		by = me.a.Name
	}
	r, err := f.memberLocked(agent, p.ID)
	if err != nil {
		return model.Agent{}, err
	}
	if r == nil { // already a member
		got, _ := f.resolveLocked(strings.TrimSpace(agent))
		return got.a, nil
	}
	f.joinProjectLocked(by, p, r)
	return r.a, nil
}

// memberLocked resolves an agent that may join project (""= a project about to be created):
// nil when it is in that project already.
func (f *Fleet) memberLocked(agent, project string) (*agentRec, error) {
	r, err := f.resolveLocked(strings.TrimSpace(agent))
	if err != nil {
		return nil, errf(KindInvalid, "member: %s", err.Error())
	}
	switch {
	case r.a.Level == model.LevelHead:
		return nil, errf(KindInvalid, "%s is the head; the head belongs to no project", r.a.Name)
	case project != "" && r.a.ProjectID == project:
		return nil, nil
	case f.leadsAnotherLocked(r.a.ID, project) != "":
		return nil, errf(KindConflict, "%s is the lead of project %s; a project's lead never joins another", r.a.Name, f.leadsAnotherLocked(r.a.ID, project))
	case r.a.ProjectID != "":
		name := r.a.ProjectID
		if q := f.projects[r.a.ProjectID]; q != nil {
			name = q.Name
		}
		return nil, errf(KindConflict, "%s is already in project %s; an agent belongs to one project", r.a.Name, name)
	}
	return r, nil
}

// leadsAnotherLocked is the name of a project other than project that agent leads ("" if none),
// whatever the agent's own ProjectID says.
func (f *Fleet) leadsAnotherLocked(agent, project string) string {
	for _, q := range f.projects {
		if q.LeadID == agent && q.ID != project {
			return q.Name
		}
	}
	return ""
}

// joinProjectLocked files the agent under the project and links it to the project's thread.
func (f *Fleet) joinProjectLocked(by string, p *model.Project, r *agentRec) {
	r.a.ProjectID = p.ID
	f.saveAgentLocked(r)
	role := "worker"
	if r.a.Level == model.LevelLead {
		role = "member"
	}
	if _, err := f.addLinkLocked(model.ThreadLink{ThreadID: f.projectThreadLocked(p.ID), Axis: model.AxisAgent, Ref: r.a.ID, Role: role,
		Label: r.a.Name, Why: "added to the project", By: by, How: model.LinkHand}); err != nil {
		f.log.Error("matrix: link project member", "agent", r.a.ID, "project", p.ID, "err", err)
	}
}

// UpdateProject changes a project's name, brief or status (owner only).
func (f *Fleet) UpdateProject(c Caller, idOrSlug string, patch ProjectPatch) (model.Project, error) {
	if !c.Owner {
		return model.Project{}, errf(KindForbidden, "only the owner can change a project")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.projectLocked(idOrSlug)
	if err != nil {
		return model.Project{}, err
	}
	if patch.Name != nil {
		if strings.TrimSpace(*patch.Name) == "" {
			return model.Project{}, errf(KindInvalid, "the project name is empty")
		}
		p.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Brief != nil {
		p.Brief = *patch.Brief
	}
	if patch.Status != nil {
		switch *patch.Status {
		case model.ProjectActive, model.ProjectPaused, model.ProjectArchived:
			p.Status = *patch.Status
		default:
			return model.Project{}, errf(KindInvalid, "unknown project status %q", *patch.Status)
		}
	}
	if err := f.saveProjectLocked(p); err != nil {
		return model.Project{}, err
	}
	return *p, nil
}

// DeleteProject stops and removes the project's agents and the project; the folder stays.
func (f *Fleet) DeleteProject(c Caller, idOrSlug string) error {
	if !c.Owner {
		return errf(KindForbidden, "only the owner can delete a project")
	}
	f.mu.Lock()
	p, err := f.projectLocked(idOrSlug)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	pid := p.ID
	var ids []string
	for _, a := range f.agentsLocked() {
		if a.ProjectID == pid {
			ids = append(ids, a.ID)
		}
	}
	f.mu.Unlock()
	for i := len(ids) - 1; i >= 0; i-- {
		f.removeOne(ids[i])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.projects, pid)
	f.processes.StopProject(pid)
	if err := f.st.DeleteProject(pid); err != nil {
		return err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvProjectRemoved, ProjectID: pid, Data: map[string]string{"id": pid}})
	return nil
}

// ProjectSectionPatch replaces supplied fields; nil preserves them, empty clears them.
type ProjectSectionPatch struct {
	FinalOutput *[]model.ProjectOutput `json:"final_output,omitempty"`
	Status      *string                `json:"status,omitempty"`
	Needs       *[]string              `json:"needs,omitempty"`
	AddNeeds    []string               `json:"add_needs,omitempty"`
	Links       *[]string              `json:"links,omitempty"`
	// ReplaceLinks: Links replaces the whole list (an empty one clears it). Without it, Links is
	// merged into the list, deduplicated by URL, a newer title winning (bc #244, 4 Nov).
	ReplaceLinks bool    `json:"replace_links,omitempty"`
	Lead         *string `json:"lead,omitempty"`
}

func sectionText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	r := []rune(strings.TrimSpace(s))
	if len(r) > max {
		r = r[:max]
	}
	return string(r)
}

func (f *Fleet) sectionLocked(p *model.Project) (model.ProjectSection, error) {
	sec, err := f.st.ProjectSection(p.ID)
	if errors.Is(err, store.ErrNotFound) {
		sec = model.ProjectSection{Needs: []model.ProjectNeed{}, Links: []model.ProjectLink{}}
	} else if err != nil {
		return sec, err
	}
	sec.ProjectID, sec.Slug, sec.Name = p.ID, p.Slug, p.Name
	if sec.Lead == "" && IsPeer(p.LeadID) {
		sec.Lead = strings.TrimPrefix(p.LeadID, PeerPrefix)
	}
	return sec, nil
}

func (f *Fleet) ProjectSection(c Caller, project string) (model.ProjectSection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.callerRecLocked(c); err != nil {
		return model.ProjectSection{}, err
	}
	p, err := f.projectLocked(project)
	if err != nil {
		return model.ProjectSection{}, err
	}
	return f.sectionLocked(p)
}

// SetProjectSection accepts reports from authorized project writers.
// It never creates or closes approvals, cards or tasks.
func (f *Fleet) SetProjectSection(c Caller, project string, patch ProjectSectionPatch) (model.ProjectSection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.Peer != "" {
		return model.ProjectSection{}, errf(KindForbidden, "only project members or the owner can write its section")
	}
	p, err := f.projectLocked(project)
	if err != nil {
		return model.ProjectSection{}, err
	}
	me, err := f.projectWriterLocked(c, p)
	if err != nil {
		return model.ProjectSection{}, err
	}
	if patch.Lead != nil && me != nil && me.a.ID != p.LeadID {
		return model.ProjectSection{}, errf(KindForbidden, "only the project lead or owner can change feedback routing")
	}
	if patch.FinalOutput != nil && me != nil && me.a.ID != p.LeadID {
		return model.ProjectSection{}, errf(KindForbidden, "only the project lead or owner can change final output")
	}
	sec, err := f.sectionLocked(p)
	if err != nil {
		return sec, err
	}
	if patch.FinalOutput != nil {
		sec.FinalOutput, err = validateProjectOutputs(*patch.FinalOutput)
		if err != nil {
			return sec, err
		}
	}
	if patch.Status != nil {
		normalized := strings.NewReplacer("\r\n", "\n", "\r", "\n", "\v", "\n", "\f", "\n", "\u0085", "\n", "\u2028", "\n", "\u2029", "\n").Replace(strings.TrimSpace(*patch.Status))
		lines := strings.Split(normalized, "\n")
		if len(lines) > 3 {
			return sec, errf(KindInvalid, "status must be at most 3 lines")
		}
		for i := range lines {
			lines[i] = strings.TrimRight(lines[i], " \t\r")
		}
		sec.Status = sectionText(strings.Join(lines, "\n"), 600)
	}
	if patch.Needs != nil {
		old := map[string]bool{}
		for _, n := range sec.Needs {
			old[n.Text] = n.Done
		}
		sec.Needs = []model.ProjectNeed{}
		for i, value := range *patch.Needs {
			if i >= 20 {
				break
			}
			if text := sectionText(value, 300); text != "" {
				sec.Needs = append(sec.Needs, model.ProjectNeed{Text: text, Done: old[text]})
			}
		}
	}
	for _, value := range patch.AddNeeds {
		if len(sec.Needs) >= 20 {
			break
		}
		text := sectionText(value, 300)
		if text != "" && !slices.ContainsFunc(sec.Needs, func(n model.ProjectNeed) bool { return n.Text == text }) {
			sec.Needs = append(sec.Needs, model.ProjectNeed{Text: text})
		}
	}
	if patch.Links != nil {
		var links []model.ProjectLink
		if !patch.ReplaceLinks {
			links = slices.Clone(sec.Links)
		}
		for _, value := range *patch.Links {
			if strings.TrimSpace(value) == "" {
				continue
			}
			title, url := "", value
			if at := strings.LastIndex(value, "|"); at >= 0 {
				title, url = value[:at], value[at+1:]
			}
			url, title = sectionText(url, 300), sectionText(title, 300)
			if url == "" {
				return sec, errf(KindInvalid, "a link needs a URL or path")
			}
			if title == "" {
				parts := strings.FieldsFunc(strings.TrimRight(url, "/\\"), func(r rune) bool { return r == '/' || r == '\\' })
				title = url
				if len(parts) > 0 {
					title = parts[len(parts)-1]
				}
			}
			if i := slices.IndexFunc(links, func(l model.ProjectLink) bool { return l.URL == url }); i >= 0 {
				links[i].Title = title // the same URL again: one entry, the newer title
				continue
			}
			links = append(links, model.ProjectLink{Title: title, URL: url})
		}
		if len(links) > 20 {
			return sec, errf(KindInvalid, "a section holds at most 20 links; replace the list (replace_links) to drop some")
		}
		if links == nil {
			links = []model.ProjectLink{}
		}
		sec.Links = links
	}
	if patch.Lead != nil {
		lead := strings.ToLower(strings.TrimSpace(*patch.Lead))
		if utf8.RuneCountInString(lead) > 80 {
			return sec, errf(KindInvalid, "lead is too long")
		}
		if name, host, ok := strings.Cut(lead, "@"); ok {
			if name == "" || !strings.EqualFold(host, f.opts.Machine) {
				return sec, errf(KindInvalid, "feedback lead must be on this machine")
			}
			lead = name
		}
		if lead != "" {
			target, err := f.resolveLocked(lead)
			if err != nil || !feedbackLive(target) || target.a.ProjectID != p.ID {
				return sec, errf(KindInvalid, "feedback lead must be a live non-shell member of this project")
			}
			lead = target.a.Name
		}
		sec.Lead = lead
	}
	sec.UpdatedAt, sec.UpdatedBy = model.Now(), model.Owner
	if me != nil {
		sec.UpdatedBy = me.a.Name
	}
	if err := f.st.PutProjectSection(sec); err != nil {
		return sec, err
	}
	f.bus.Publish(model.BusEvent{Type: "project.section.updated", ProjectID: p.ID, Data: sec})
	return sec, nil
}

func feedbackLive(r *agentRec) bool {
	if r == nil || r.a.Harness == model.HarnessShell || r.a.Desired == "stopped" {
		return false
	}
	switch r.a.State {
	case model.StateStarting, model.StateWorking, model.StateIdle, model.StateWaiting:
		return true
	}
	return false
}

type ProjectContributionPatch struct {
	Status      *string                `json:"status,omitempty"`
	FinalOutput *[]model.ProjectOutput `json:"final_output,omitempty"`
}

func outputKind(path string) string {
	if u, err := url.Parse(path); err == nil && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) && u.Host != "" {
		return "link"
	}
	return "path"
}

func validateProjectOutputs(in []model.ProjectOutput) ([]model.ProjectOutput, error) {
	if len(in) > 20 {
		return nil, errf(KindInvalid, "at most 20 final outputs")
	}
	out := make([]model.ProjectOutput, 0, len(in))
	for _, v := range in {
		v.Path = strings.TrimSpace(v.Path)
		if v.Path == "" || len(v.Path) > 4096 || strings.ContainsAny(v.Path, "\x00\r\n") {
			return nil, errf(KindInvalid, "final output needs a path or HTTP(S) URL of at most 4096 bytes")
		}
		lower := strings.ToLower(v.Path)
		if (strings.HasPrefix(lower, "http:") || strings.HasPrefix(lower, "https:")) && outputKind(v.Path) != "link" {
			return nil, errf(KindInvalid, "invalid HTTP(S) final output URL")
		}
		v.What = sectionText(v.What, 600)
		out = append(out, v)
	}
	return out, nil
}

// SetMyProjectContribution has no caller-supplied ownership field. Even the owner
// cannot impersonate an agent; only an authenticated project writer writes its own row.
func (f *Fleet) SetMyProjectContribution(c Caller, project string, patch ProjectContributionPatch) (model.ProjectContribution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.projectLocked(project)
	if err != nil {
		return model.ProjectContribution{}, err
	}
	me, err := f.projectWriterLocked(c, p)
	if err != nil {
		return model.ProjectContribution{}, err
	}
	if me == nil {
		return model.ProjectContribution{}, errf(KindForbidden, "only an authenticated project writer can write its own contribution")
	}
	all, err := f.st.ProjectContributions(p.ID)
	if err != nil {
		return model.ProjectContribution{}, err
	}
	part := model.ProjectContribution{ProjectID: p.ID, AgentID: me.a.ID, FinalOutput: []model.ProjectOutput{}}
	for _, prev := range all {
		if prev.AgentID == me.a.ID {
			part = prev
			break
		}
	}
	part.AgentName, part.UpdatedAt = me.a.Name, model.Now()
	if patch.Status != nil {
		part.Status = sectionText(*patch.Status, 600)
	}
	if patch.FinalOutput != nil {
		part.FinalOutput, err = validateProjectOutputs(*patch.FinalOutput)
		if err != nil {
			return model.ProjectContribution{}, err
		}
	}
	if err := f.st.PutProjectContribution(part); err != nil {
		return model.ProjectContribution{}, err
	}
	f.bus.Publish(model.BusEvent{Type: "project.contribution.updated", ProjectID: p.ID, AgentID: me.a.ID, Data: part})
	return part, nil
}

// ProjectArtifacts is metadata only. It never stats, fetches, resolves or serves
// reported paths. HTTP(S) values are preserved verbatim as links.
func (f *Fleet) ProjectArtifacts(c Caller, project string) (model.ProjectArtifacts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.callerRecLocked(c); err != nil {
		return model.ProjectArtifacts{}, err
	}
	p, err := f.projectLocked(project)
	if err != nil {
		return model.ProjectArtifacts{}, err
	}
	sec, err := f.sectionLocked(p)
	if err != nil {
		return model.ProjectArtifacts{}, err
	}
	parts, err := f.st.ProjectContributions(p.ID)
	if err != nil {
		return model.ProjectArtifacts{}, err
	}
	out := model.ProjectArtifacts{ProjectID: p.ID, Contributions: parts, FinalOutput: []model.ProjectArtifact{}}
	for _, v := range sec.FinalOutput {
		out.FinalOutput = append(out.FinalOutput, model.ProjectArtifact{ProjectOutput: v, Kind: outputKind(v.Path)})
	}
	for _, part := range parts {
		for _, v := range part.FinalOutput {
			out.FinalOutput = append(out.FinalOutput, model.ProjectArtifact{ProjectOutput: v, Kind: outputKind(v.Path), AgentID: part.AgentID})
		}
	}
	return out, nil
}

// feedbackRouteLocked deliberately makes no arbitrary choice among several leads.
// This local slice uses the head as Python's supervisor copy.
func (f *Fleet) feedbackRouteLocked(p *model.Project, sec model.ProjectSection) model.FeedbackRoute {
	named := sec.Lead
	if named == "" {
		if r := f.agents[p.LeadID]; r != nil {
			named = r.a.Name
		}
	}
	route := model.FeedbackRoute{Named: named, How: "none", Why: "project names no live lead"}
	rejected := ""
	if named != "" {
		r, _ := f.resolveLocked(named)
		if feedbackLive(r) {
			route.To, route.How, route.Why = r.a.ID, "named", ""
			return route
		}
		if r != nil {
			rejected = r.a.ID
		}
		route.Why = fmt.Sprintf("named lead %s is unavailable or is a shell session", named)
	}
	var leads []*agentRec
	for _, r := range f.agents {
		if r.a.ProjectID == p.ID && r.a.Level == model.LevelLead && r.a.ID != rejected && feedbackLive(r) {
			leads = append(leads, r)
		}
	}
	if len(leads) == 1 {
		route.To, route.How = leads[0].a.ID, "live-lead"
		return route
	}
	if len(leads) > 1 {
		route.Why += "; several live project leads, choose one"
	}
	if r, _ := f.resolveLocked("tmx-lead"); r != nil && r.a.ID != rejected && feedbackLive(r) {
		route.To, route.How = r.a.ID, "tmx-lead"
	}
	if route.To == "" {
		route.Why += "; no live lead or tmx-lead; supervisor copy only"
	}
	return route
}

type ProjectFeedbackRequest struct {
	ID   string   `json:"id"`
	Text string   `json:"text"`
	Done []string `json:"done,omitempty"`
}

var feedbackID = regexp.MustCompile(`^[0-9a-f]{16,32}$`)

// SendProjectFeedback persists one owner feedback operation and its mailbox entries
// atomically. Retries retain the original recipients and message identities.
func (f *Fleet) SendProjectFeedback(c Caller, project string, req ProjectFeedbackRequest) (model.ProjectFeedback, error) {
	if !c.Owner {
		return model.ProjectFeedback{}, errf(KindForbidden, "project feedback comes from the owner only")
	}
	if !feedbackID.MatchString(req.ID) {
		return model.ProjectFeedback{}, errf(KindInvalid, "id must be 16-32 lowercase hex characters")
	}
	req.Text = strings.TrimSpace(req.Text)
	if utf8.RuneCountInString(req.Text) > 20000 || len(req.Text) > MaxBodyBytes-8192 {
		return model.ProjectFeedback{}, errf(KindInvalid, "feedback is too long")
	}
	done := []string{}
	for _, value := range req.Done {
		if len(done) >= 20 {
			break
		}
		if text := sectionText(value, 300); text != "" {
			done = append(done, text)
		}
	}
	if req.Text == "" && len(done) == 0 {
		return model.ProjectFeedback{}, errf(KindInvalid, "feedback is empty")
	}
	f.mu.Lock()
	rec, fresh, err := f.prepareProjectFeedbackLocked(project, req, done)
	f.mu.Unlock()
	if err != nil {
		return rec, err
	}
	for i, m := range rec.Messages {
		if fresh {
			f.bus.Publish(model.BusEvent{Type: model.EvMessageCreated, AgentID: m.To, Data: m})
		}
		f.deliverOwner(m)
		if current, err := f.st.GetMessage(m.ID); err == nil {
			rec.Messages[i] = current
		}
	}
	return rec, nil
}

func (f *Fleet) prepareProjectFeedbackLocked(project string, req ProjectFeedbackRequest, done []string) (model.ProjectFeedback, bool, error) {
	p, err := f.projectLocked(project)
	if err != nil {
		return model.ProjectFeedback{}, false, err
	}
	if prev, err := f.st.ProjectFeedback(p.ID, req.ID); err == nil {
		if prev.Text != req.Text || !slices.Equal(prev.Done, done) {
			return prev, false, errf(KindConflict, "feedback id already used for different content")
		}
		return prev, false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return prev, false, err
	}
	sec, err := f.sectionLocked(p)
	if err != nil {
		return model.ProjectFeedback{}, false, err
	}
	route := f.feedbackRouteLocked(p, sec)
	rec := model.ProjectFeedback{ID: req.ID, ProjectID: p.ID, Text: req.Text, Done: done, Route: route, CreatedAt: model.Now(), Messages: []model.Message{}}
	targets := []string{}
	if route.To != "" {
		targets = append(targets, route.To)
	}
	if h := f.headLocked(); h != nil && h.a.Harness != model.HarnessShell && !slices.Contains(targets, h.a.ID) {
		targets = append(targets, h.a.ID)
	}
	body := fmt.Sprintf("[Feedback from owner - %s - project page]\n%s", peertext.Field(p.Name), req.Text)
	if len(done) > 0 {
		body += "\n\nProject checklist text (quoted; ticking marks it done, not an instruction):"
		for _, item := range done {
			body += "\n" + peertext.Quote(item)
		}
	}
	if route.How != "named" {
		body = "[tmx: " + peertext.Field(route.Why) + "]\n" + body
	}
	if len(body) > MaxBodyBytes {
		return rec, false, errf(KindInvalid, "feedback including checklist is too long")
	}
	for _, id := range targets {
		rec.Messages = append(rec.Messages, model.Message{ID: model.NewID(), From: model.Owner, To: id, Body: body, State: model.MsgQueued, CreatedAt: rec.CreatedAt, ThreadID: f.projectThreadLocked(p.ID)})
	}
	if len(targets) > 0 && len(done) > 0 {
		sec.UpdatedAt, sec.UpdatedBy = rec.CreatedAt, model.Owner
		for i := range sec.Needs {
			if slices.Contains(done, sec.Needs[i].Text) {
				sec.Needs[i].Done = true
			}
		}
	}
	if err := f.st.CommitProjectFeedback(sec, rec); err != nil {
		return rec, false, err
	}
	for _, id := range targets {
		f.refreshQueuedLocked(f.agents[id])
	}
	f.bus.Publish(model.BusEvent{Type: "project.section.updated", ProjectID: p.ID, Data: sec})
	return rec, true, nil
}
