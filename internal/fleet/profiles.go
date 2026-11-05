package fleet

import (
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Profile limits.
const (
	maxProfileName   = 60
	maxProfileDesc   = 500
	maxInstructions  = 32 << 10
	maxToolEntries   = 100
	maxToolEntryLen  = 200
	maxAllowedModels = 20
)

var levelOrder = []model.Level{model.LevelHead, model.LevelLead, model.LevelWorker}

// loadProfiles fills f.profiles from the store (called by New, before anything runs). A store
// without profiles (a new install, or one from before profiles) is seeded with
// model.DefaultProfiles, the built-ins taking their model, permission and launch settings
// from seed (config.json "policy"): that is how the per-level policy migrates. Built-ins that
// are missing are restored; agents without a (known) profile get the built-in of their level.
func (f *Fleet) loadProfiles(seed model.Policy) error {
	list, err := f.st.ListProfiles()
	if err != nil {
		return err
	}
	for i := range list {
		p := list[i]
		f.profiles[p.Slug] = &p
	}
	var add []model.Profile
	if len(list) == 0 {
		add = model.DefaultProfiles()
		applyPolicy(add, seed, add)
	} else {
		for _, slug := range model.BuiltInProfiles {
			if _, ok := f.profiles[slug]; !ok {
				d, _ := model.DefaultProfile(slug)
				d.CanLaunch = slices.DeleteFunc(d.CanLaunch, func(s string) bool { return f.profiles[s] == nil && !model.IsBuiltInProfile(s) })
				add = append(add, d)
			}
		}
	}
	now := model.Now()
	for _, p := range add {
		p.CreatedAt, p.UpdatedAt = now, now
		normalizeSlices(&p)
		if err := f.st.PutProfile(p); err != nil {
			return err
		}
		f.profiles[p.Slug] = &p
	}
	if err := f.st.UpgradeModelDefaults(); err != nil {
		return err
	}
	upgraded, err := f.st.ListProfiles()
	if err != nil {
		return err
	}
	for i := range upgraded {
		p := upgraded[i]
		f.profiles[p.Slug] = &p
	}
	for _, r := range f.agents {
		if p := f.profiles[r.a.Profile]; p == nil || p.Level != r.a.Level {
			r.a.Profile = string(r.a.Level)
			if err := f.st.PutAgent(r.a); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyPolicy overlays a per-level policy on the built-in profiles in ps; all is every
// profile (for the launch rights).
func applyPolicy(ps []model.Profile, pol model.Policy, all []model.Profile) {
	for i := range ps {
		p := &ps[i]
		lp, ok := pol.Levels[p.Level]
		if !ok || !model.IsBuiltInProfile(p.Slug) {
			continue
		}
		p.Model, p.AllowedModels = lp.Model, slices.Clone(lp.AllowedModels)
		p.PermissionMode, p.CanCreateProjects = lp.PermissionMode, lp.CanCreateProjects
		p.CanLaunch = launchSlugs(lp.CanLaunch, p.CanLaunch, all)
	}
}

// launchSlugs maps a per-level can_launch list to profile slugs: the built-in profile of every
// level in levels, plus the custom profiles in current whose level is still allowed.
func launchSlugs(levels []model.Level, current []string, all []model.Profile) []string {
	out := []string{}
	for _, p := range all {
		if !slices.Contains(levels, p.Level) {
			continue
		}
		if model.IsBuiltInProfile(p.Slug) || slices.Contains(current, p.Slug) {
			out = append(out, p.Slug)
		}
	}
	return out
}

// profileListLocked returns the profiles in display order: the built-ins by level, then the
// custom ones by name.
func (f *Fleet) profileListLocked() []model.Profile {
	out := make([]model.Profile, 0, len(f.profiles))
	for _, p := range f.profiles {
		out = append(out, f.decorate(*p))
	}
	rank := func(p model.Profile) int {
		if i := slices.Index(model.BuiltInProfiles, p.Slug); i >= 0 {
			return i
		}
		return len(model.BuiltInProfiles)
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		if out[i].Name != out[j].Name {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// decorate returns a copy with the derived fields set.
func (f *Fleet) decorate(p model.Profile) model.Profile {
	p = p.Clone()
	p.BuiltIn = model.IsBuiltInProfile(p.Slug)
	p.Modified = false
	if p.BuiltIn {
		if d, ok := model.DefaultProfile(p.Slug); ok {
			p.Modified = !p.SameSettings(d)
		}
	}
	return p
}

// profileForLocked is the profile an agent runs with: its own, else the built-in of its level.
func (f *Fleet) profileForLocked(a model.Agent) model.Profile {
	if p, ok := f.profiles[a.Profile]; ok && p.Level == a.Level {
		return *p
	}
	if p, ok := f.profiles[string(a.Level)]; ok {
		return *p
	}
	d, _ := model.DefaultProfile(string(a.Level))
	return d
}

func (f *Fleet) usersOfLocked(slug string) []string {
	var names []string
	for _, a := range f.agentsLocked() {
		if a.Profile == slug {
			names = append(names, a.Name)
		}
	}
	return names
}

// Profiles returns every profile in display order.
func (f *Fleet) Profiles() []model.Profile {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.profileListLocked()
}

// GetProfile returns one profile.
func (f *Fleet) GetProfile(slug string) (model.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.profiles[slug]
	if !ok {
		return model.Profile{}, errf(KindNotFound, "no profile %q", slug)
	}
	return f.decorate(*p), nil
}

// CreateProfile adds a custom profile (owner only).
func (f *Fleet) CreateProfile(c Caller, p model.Profile) (model.Profile, error) {
	if !c.Owner {
		return model.Profile{}, errf(KindForbidden, "only the owner can change profiles")
	}
	p.Slug = strings.TrimSpace(p.Slug)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !model.ValidName(p.Slug) || p.Slug == model.Owner {
		return model.Profile{}, errf(KindInvalid, "%q is not a valid profile slug (lowercase letters, digits and dashes, up to 40)", p.Slug)
	}
	if _, ok := f.profiles[p.Slug]; ok || model.IsBuiltInProfile(p.Slug) {
		return model.Profile{}, errf(KindConflict, "a profile %q already exists", p.Slug)
	}
	if err := f.validateProfileLocked(&p); err != nil {
		return model.Profile{}, err
	}
	now := model.Now()
	p.CreatedAt, p.UpdatedAt = now, now
	return f.putProfileLocked(p)
}

// UpdateProfile replaces a profile's settings (owner only). The slug stays; a built-in keeps
// its level, and a custom profile changes level only while no agent uses it. Agents pick up
// the change at their next start.
func (f *Fleet) UpdateProfile(c Caller, slug string, p model.Profile) (model.Profile, error) {
	if !c.Owner {
		return model.Profile{}, errf(KindForbidden, "only the owner can change profiles")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.profiles[slug]
	if !ok {
		return model.Profile{}, errf(KindNotFound, "no profile %q", slug)
	}
	if p.Slug != "" && p.Slug != slug {
		return model.Profile{}, errf(KindInvalid, "a profile's slug cannot be changed (%s → %s)", slug, p.Slug)
	}
	p.Slug = slug
	if p.Level == "" {
		p.Level = cur.Level
	}
	if p.Level != cur.Level {
		if model.IsBuiltInProfile(slug) {
			return model.Profile{}, errf(KindInvalid, "the built-in profile %s stays a %s profile", slug, cur.Level)
		}
		if users := f.usersOfLocked(slug); len(users) > 0 {
			return model.Profile{}, errf(KindConflict, "the level of %s cannot change while agents use it (%s)", slug, strings.Join(users, ", "))
		}
	}
	if err := f.validateProfileLocked(&p); err != nil {
		return model.Profile{}, err
	}
	p.CreatedAt, p.UpdatedAt = cur.CreatedAt, model.Now()
	return f.putProfileLocked(p)
}

// ResetProfile puts a built-in profile back to its default (owner only).
func (f *Fleet) ResetProfile(c Caller, slug string) (model.Profile, error) {
	if !c.Owner {
		return model.Profile{}, errf(KindForbidden, "only the owner can change profiles")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.profiles[slug]
	if !ok {
		return model.Profile{}, errf(KindNotFound, "no profile %q", slug)
	}
	if !model.IsBuiltInProfile(slug) {
		return model.Profile{}, errf(KindInvalid, "only built-in profiles can be reset; %s is a custom profile", slug)
	}
	d, _ := model.DefaultProfile(slug)
	d.CanLaunch = slices.DeleteFunc(d.CanLaunch, func(s string) bool { return f.profiles[s] == nil })
	normalizeSlices(&d)
	d.CreatedAt, d.UpdatedAt = cur.CreatedAt, model.Now()
	return f.putProfileLocked(d)
}

// DeleteProfile removes a custom profile no agent uses (owner only) and drops it from the
// launch rights of the others.
func (f *Fleet) DeleteProfile(c Caller, slug string) error {
	if !c.Owner {
		return errf(KindForbidden, "only the owner can change profiles")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.profiles[slug]; !ok {
		return errf(KindNotFound, "no profile %q", slug)
	}
	if model.IsBuiltInProfile(slug) {
		return errf(KindForbidden, "the built-in profile %s cannot be deleted; reset it instead", slug)
	}
	if users := f.usersOfLocked(slug); len(users) > 0 {
		return errf(KindConflict, "%s is used by %s; change their profile or remove them first", slug, strings.Join(users, ", "))
	}
	if err := f.st.DeleteProfile(slug); err != nil {
		return err
	}
	delete(f.profiles, slug)
	f.bus.Publish(model.BusEvent{Type: model.EvProfileRemoved, Data: map[string]string{"slug": slug}})
	for _, p := range f.profileListLocked() {
		if slices.Contains(p.CanLaunch, slug) {
			p.CanLaunch = slices.DeleteFunc(p.CanLaunch, func(s string) bool { return s == slug })
			p.UpdatedAt = model.Now()
			if _, err := f.putProfileLocked(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// putProfileLocked stores p, registers it and publishes profile.updated.
func (f *Fleet) putProfileLocked(p model.Profile) (model.Profile, error) {
	p.BuiltIn, p.Modified = false, false
	if err := f.st.PutProfile(p); err != nil {
		return model.Profile{}, err
	}
	cp := p.Clone()
	f.profiles[p.Slug] = &cp
	out := f.decorate(cp)
	f.bus.Publish(model.BusEvent{Type: model.EvProfileUpdated, Data: out})
	return out, nil
}

// validateProfileLocked checks p and normalizes it in place (trimmed text, de-duplicated
// lists, the default model added to the allowed ones, "auto" for an empty permission mode).
func (f *Fleet) validateProfileLocked(p *model.Profile) error {
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	switch {
	case p.Name == "":
		return errf(KindInvalid, "the profile needs a name")
	case utf8.RuneCountInString(p.Name) > maxProfileName:
		return errf(KindInvalid, "the profile name is longer than %d characters", maxProfileName)
	case utf8.RuneCountInString(p.Description) > maxProfileDesc:
		return errf(KindInvalid, "the description is longer than %d characters", maxProfileDesc)
	case len(p.Instructions) > maxInstructions:
		return errf(KindInvalid, "the instructions are longer than %d KB", maxInstructions>>10)
	}
	if !slices.Contains(levelOrder, p.Level) {
		return errf(KindInvalid, "unknown level %q (head, lead or worker)", p.Level)
	}
	if d, ok := model.DefaultProfile(p.Slug); ok && model.IsBuiltInProfile(p.Slug) && p.Level != d.Level {
		return errf(KindInvalid, "the built-in profile %s stays a %s profile", p.Slug, d.Level)
	}

	p.Model = strings.TrimSpace(p.Model)
	if p.Model == "" {
		return errf(KindInvalid, "the profile has no default model")
	}
	models := cleanList(p.AllowedModels)
	if !slices.Contains(models, p.Model) {
		models = slices.Insert(models, 0, p.Model)
	}
	if len(models) > maxAllowedModels {
		return errf(KindInvalid, "at most %d allowed models", maxAllowedModels)
	}
	p.AllowedModels = models

	if p.PermissionMode == "" {
		p.PermissionMode = "auto"
	}
	if !slices.Contains(model.PermissionModes, p.PermissionMode) {
		return errf(KindInvalid, "unknown permission mode %q (%s)", p.PermissionMode, strings.Join(model.PermissionModes, ", "))
	}
	var err error
	if p.AllowedTools, err = cleanTools("allowed", p.AllowedTools); err != nil {
		return err
	}
	if p.DisallowedTools, err = cleanTools("disallowed", p.DisallowedTools); err != nil {
		return err
	}

	launch := []string{}
	for _, s := range p.CanLaunch {
		s = strings.TrimSpace(s)
		if s == "" || slices.Contains(launch, s) {
			continue
		}
		target, ok := f.profiles[s]
		lv := model.Level("")
		switch {
		case s == p.Slug:
			lv = p.Level
		case ok:
			lv = target.Level
		default:
			return errf(KindInvalid, "can_launch names an unknown profile %q", s)
		}
		if lv == model.LevelHead {
			return errf(KindInvalid, "%s is a head profile; there is one head per machine and nobody launches it", s)
		}
		launch = append(launch, s)
	}
	p.CanLaunch = launch
	normalizeSlices(p)
	return nil
}

func cleanList(in []string) []string {
	out := []string{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// cleanTools validates a claude tool list: names or rules like Bash(git status:*), passed
// comma-joined on the command line, so no commas inside.
func cleanTools(which string, in []string) ([]string, error) {
	out := cleanList(in)
	if len(out) > maxToolEntries {
		return nil, errf(KindInvalid, "at most %d %s tools", maxToolEntries, which)
	}
	for _, t := range out {
		if strings.Contains(t, ",") || len(t) > maxToolEntryLen || strings.ContainsAny(t, "\n\r") {
			return nil, errf(KindInvalid, "%q is not a valid %s tool (one tool name or rule per entry, no commas)", t, which)
		}
	}
	return out, nil
}

func normalizeSlices(p *model.Profile) {
	for _, s := range []*[]string{&p.AllowedModels, &p.AllowedTools, &p.DisallowedTools, &p.CanLaunch} {
		if *s == nil {
			*s = []string{}
		}
	}
}

// pickModel applies a profile: "" = its default model; anything else must be allowed.
func pickModel(p model.Profile, requested string) (string, error) {
	if requested == "" {
		return p.Model, nil
	}
	if !slices.Contains(p.AllowedModels, requested) {
		return "", errf(KindForbidden, "model %s is not allowed for the %s profile (allowed: %s)",
			requested, p.Slug, strings.Join(p.AllowedModels, ", "))
	}
	return requested, nil
}

// launchProfileLocked resolves the profile of a new agent from the request's profile and
// level ("" profile = the built-in of the level; "" level = worker).
func (f *Fleet) launchProfileLocked(slug string, level model.Level) (model.Profile, error) {
	if slug == "" {
		if level == "" {
			level = model.LevelWorker
		}
		if !slices.Contains(levelOrder, level) {
			return model.Profile{}, errf(KindInvalid, "unknown level %q", level)
		}
		slug = string(level)
	}
	p, ok := f.profiles[slug]
	if !ok {
		return model.Profile{}, errf(KindInvalid, "no profile %q", slug)
	}
	if level != "" && p.Level != level {
		return model.Profile{}, errf(KindInvalid, "the profile %s is for %s agents, not %s", slug, p.Level, level)
	}
	if p.Level == model.LevelHead {
		return model.Profile{}, errf(KindForbidden, "there is exactly one head per machine; it cannot be launched")
	}
	return *p, nil
}

// policyLocked derives the per-level policy (GET /api/policy, Snapshot.policy) from the
// built-in profiles.
func (f *Fleet) policyLocked() model.Policy {
	out := model.Policy{Levels: map[model.Level]model.LevelPolicy{}}
	for _, lv := range levelOrder {
		p, ok := f.profiles[string(lv)]
		if !ok {
			continue
		}
		var can []model.Level
		for _, l := range levelOrder {
			for _, s := range p.CanLaunch {
				if t, ok := f.profiles[s]; ok && t.Level == l && !slices.Contains(can, l) {
					can = append(can, l)
				}
			}
		}
		out.Levels[lv] = model.LevelPolicy{
			Model: p.Model, AllowedModels: slices.Clone(p.AllowedModels), PermissionMode: p.PermissionMode,
			CanCreateProjects: p.CanCreateProjects, CanLaunch: can,
		}
	}
	return out
}
