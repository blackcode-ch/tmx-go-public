package model

import "slices"

// Profile is an agent type the owner can customise: its base level (which keeps the
// hierarchy rights), its instructions, its model and permission policy, its tool limits and
// what it may launch. Agents carry the slug of their profile; changes apply at their next start.
type Profile struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Level       Level  `json:"level"`
	// Instructions is a template ({{name}}, {{project}}, ...; see DESIGN.md "Profiles").
	// tmx appends the role, the brief, the read-only note and the mail rules; those are not editable.
	Instructions      string   `json:"instructions"`
	Model             string   `json:"model"`
	AllowedModels     []string `json:"allowed_models"`
	PermissionMode    string   `json:"permission_mode"`
	AllowedTools      []string `json:"allowed_tools"`    // claude --allowedTools (besides mcp__tmxg)
	DisallowedTools   []string `json:"disallowed_tools"` // claude --disallowedTools
	ReadOnly          bool     `json:"read_only"`        // disallows ReadOnlyDenied as well
	CanCreateProjects bool     `json:"can_create_projects"`
	CanLaunch         []string `json:"can_launch"` // profile slugs
	// CompanionTasks lets agents of this profile hand tasks to Companion (MCP companion_task;
	// every hand-off still waits for the owner's OK). Off by default.
	CompanionTasks bool `json:"companion_tasks"`

	// Derived, never stored.
	BuiltIn  bool `json:"built_in"`
	Modified bool `json:"modified"` // a built-in that differs from its default

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// Bus event types for profiles.
const (
	EvProfileUpdated = "profile.updated"
	EvProfileRemoved = "profile.removed"
)

// ReadOnlyDenied are the tools a read-only profile disallows: everything that writes files
// or runs commands.
var ReadOnlyDenied = []string{"Write", "Edit", "MultiEdit", "NotebookEdit", "Bash", "PowerShell"}

// PermissionModes are the values claude's --permission-mode accepts.
var PermissionModes = []string{"auto", "default", "acceptEdits", "plan", "dontAsk", "bypassPermissions"}

// Built-in profile slugs: one per level, named after it. They cannot be deleted and keep their level.
var BuiltInProfiles = []string{string(LevelHead), string(LevelLead), string(LevelWorker)}

// IsBuiltInProfile reports whether slug is one of the built-in profiles.
func IsBuiltInProfile(slug string) bool { return slices.Contains(BuiltInProfiles, slug) }

const (
	headInstructions = `You are "{{name}}", the head agent of the machine "{{machine}}", run by tmx. ` +
		`You talk with the owner (the human) and run this machine's projects through other agents.

Turn the owner's requests into projects with mcp__tmxg__project_create: it creates the project folder under {{workspace}} and launches the project's lead. ` +
		`Delegate the work to the leads (send) and follow it with fleet and agent_status. ` +
		`Do not do project work yourself: no coding or editing in project folders.

Keep the owner informed with short updates (send to "owner"): what started, what finished, what needs them. ` +
		`When a decision only the owner can make blocks progress, use ask_owner.`

	leadInstructions = `You are "{{name}}", the lead of project "{{project}}" in the folder {{project_folder}}, run by tmx. Your parent is {{parent}}.

You own the project: plan the work, launch workers with mcp__tmxg__agent_launch (the model is set by their profile; leave it out; mcp__tmxg__profiles lists the profiles you may launch), ` +
		`give each worker one clear task, review what they deliver, and stop workers that are done (agent_stop).

Report progress, results and blockers to your parent with report; keep reports short and concrete.`

	workerInstructions = `You are "{{name}}", a worker in project "{{project}}", run by tmx; your folder is {{folder}}. Your lead is {{parent}}.

Do the task you are given, staying inside the project. ` +
		`When you are done or blocked, report to your lead with report: what changed, where, and how you verified it. Set done=true only when your assigned work is complete; eligible workers are shelved after the turn and can be resumed. Leave done=false when blocked or reporting progress.`

	reviewerInstructions = `You are "{{name}}", a reviewer in project "{{project}}", run by tmx; your folder is {{folder}}. You report to {{parent}}.

You review; you never change anything. Read the code and the changes you are asked about and check them against the task: ` +
		`bugs, missing tests, security problems, unclear code.

Report to {{parent}} with report: a verdict first (approve, or changes needed), then concrete findings with file and line, most important first.`
)

// DefaultProfiles use Opus for leads and Sonnet for ordinary workers. Workers may
// select Opus for senior work or Haiku for intern work; reviewer stays read-only.
func DefaultProfiles() []Profile {
	const opus, sonnet, haiku = "claude-opus-5-5", "claude-sonnet-5", "claude-haiku-4-5-20251001"
	return []Profile{
		{Slug: "head", Name: "Head", Level: LevelHead,
			Description:  "Talks with you, creates projects and briefs their leads.",
			Instructions: headInstructions, Model: opus, AllowedModels: []string{opus}, PermissionMode: "auto",
			CanCreateProjects: true, CanLaunch: []string{"lead", "worker", "reviewer"}},
		{Slug: "lead", Name: "Lead", Level: LevelLead,
			Description:  "Owns one project and launches its workers.",
			Instructions: leadInstructions, Model: opus, AllowedModels: []string{opus, sonnet}, PermissionMode: "auto",
			CanLaunch: []string{"worker", "reviewer"}},
		{Slug: "worker", Name: "Worker", Level: LevelWorker,
			Description:  "Does the work and reports to its lead.",
			Instructions: workerInstructions, Model: sonnet, AllowedModels: []string{sonnet, opus, haiku}, PermissionMode: "auto"},
		{Slug: "reviewer", Name: "Reviewer", Level: LevelWorker,
			Description:  "Reviews code and changes without editing anything: no shell, no file edits.",
			Instructions: reviewerInstructions, Model: sonnet, AllowedModels: []string{sonnet}, PermissionMode: "auto",
			ReadOnly: true},
	}
}

// DefaultProfile returns the default of a built-in (or the example) profile.
func DefaultProfile(slug string) (Profile, bool) {
	for _, p := range DefaultProfiles() {
		if p.Slug == slug {
			return p, true
		}
	}
	return Profile{}, false
}

// SameSettings reports whether a and b have the same editable settings (timestamps and
// derived fields aside).
func (p Profile) SameSettings(o Profile) bool {
	eq := func(a, b []string) bool { return slices.Equal(nonNil(a), nonNil(b)) }
	return p.Slug == o.Slug && p.Name == o.Name && p.Description == o.Description && p.Level == o.Level &&
		p.Instructions == o.Instructions && p.Model == o.Model && eq(p.AllowedModels, o.AllowedModels) &&
		p.PermissionMode == o.PermissionMode && eq(p.AllowedTools, o.AllowedTools) &&
		eq(p.DisallowedTools, o.DisallowedTools) && p.ReadOnly == o.ReadOnly &&
		p.CanCreateProjects == o.CanCreateProjects && eq(p.CanLaunch, o.CanLaunch) &&
		p.CompanionTasks == o.CompanionTasks
}

// Clone returns a deep copy.
func (p Profile) Clone() Profile {
	p.AllowedModels = slices.Clone(p.AllowedModels)
	p.AllowedTools = slices.Clone(p.AllowedTools)
	p.DisallowedTools = slices.Clone(p.DisallowedTools)
	p.CanLaunch = slices.Clone(p.CanLaunch)
	return p
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
