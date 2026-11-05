package fleet

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

const haiku = "claude-haiku-4-5-20251001"

func slugs(ps []model.Profile) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Slug)
	}
	return out
}

func TestProfilesSeededAndPolicyDerived(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	snap, err := f.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := slugs(snap.Profiles); !slices.Equal(got, []string{"head", "lead", "worker", "reviewer"}) {
		t.Fatalf("profiles %v", got)
	}
	for _, p := range snap.Profiles {
		if p.BuiltIn != model.IsBuiltInProfile(p.Slug) || p.Modified || p.CreatedAt == 0 {
			t.Fatalf("profile %s: %+v", p.Slug, p)
		}
	}
	want := model.DefaultPolicy()
	got := f.Policy()
	for lv, lp := range want.Levels {
		g := got.Levels[lv]
		if g.Model != lp.Model || !slices.Equal(g.AllowedModels, lp.AllowedModels) || g.PermissionMode != lp.PermissionMode ||
			g.CanCreateProjects != lp.CanCreateProjects || !slices.Equal(g.CanLaunch, lp.CanLaunch) {
			t.Fatalf("%s: got %+v want %+v", lv, g, lp)
		}
	}
	if h := mustAgent(t, f, "head"); h.Profile != "head" {
		t.Fatalf("head profile %q", h.Profile)
	}
}

// The per-level policy of config.json migrates into the built-in profiles on first start;
// a second start keeps what is stored.
func TestProfilesSeedFromConfiguredPolicy(t *testing.T) {
	e := newEnv(t)
	pol := model.DefaultPolicy()
	for lv, lp := range pol.Levels {
		lp.Model, lp.AllowedModels, lp.PermissionMode = haiku, []string{haiku}, "acceptEdits"
		pol.Levels[lv] = lp
	}
	lp := pol.Levels[model.LevelLead]
	lp.CanLaunch = nil
	pol.Levels[model.LevelLead] = lp
	open := func(p model.Policy) *Fleet {
		f, err := New(Options{Elevated: func() bool { return false }, Store: e.st, Paths: config.Paths{Home: e.home},
			Runners: map[model.Mode]runner.Runner{model.ModeChat: e.chat}, Workspace: e.workspace,
			Executable: "/opt/tmx/tmx", Policy: p})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			f.Close(ctx)
		})
		return f
	}
	f := open(pol)
	lead, _ := f.GetProfile("lead")
	if lead.Model != haiku || lead.PermissionMode != "acceptEdits" || len(lead.CanLaunch) != 0 || !lead.Modified {
		t.Fatalf("lead %+v", lead)
	}
	if rv, _ := f.GetProfile("reviewer"); rv.Model != "claude-sonnet-5" {
		t.Fatalf("custom profile took the policy: %+v", rv)
	}
	f2 := open(model.DefaultPolicy())
	if lead, _ := f2.GetProfile("lead"); lead.Model != haiku {
		t.Fatalf("stored profiles were re-seeded: %+v", lead)
	}
}

func TestLaunchByProfile(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	p, _ := f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	lead := mustAgent(t, f, "alpha-lead")
	if lead.Profile != "lead" {
		t.Fatalf("lead profile %q", lead.Profile)
	}
	lc := callerFor(t, f, e.chat, lead.ID)

	rv, err := f.LaunchAgent(lc, LaunchRequest{Name: "rv", Profile: "reviewer", Prompt: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if rv.Level != model.LevelWorker || rv.Profile != "reviewer" || rv.ParentID != lead.ID || rv.ProjectID != p.ID {
		t.Fatalf("reviewer: %+v", rv)
	}
	spec := e.chat.Last(rv.ID).Spec
	if !slices.Equal(spec.DisallowedTools, model.ReadOnlyDenied) || !slices.Equal(spec.AllowedTools, []string{"mcp__tmxg"}) {
		t.Fatalf("tools: %+v / %+v", spec.AllowedTools, spec.DisallowedTools)
	}
	for _, want := range []string{`You are "rv", a reviewer in project "alpha"`, "You report to alpha-lead", readOnlyNote, mailRules} {
		if !strings.Contains(spec.SystemPrompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, spec.SystemPrompt)
		}
	}
	if strings.Contains(spec.SystemPrompt, "{{") {
		t.Fatalf("unrendered variables:\n%s", spec.SystemPrompt)
	}

	cases := []struct {
		name string
		c    Caller
		req  LaunchRequest
		kind Kind
	}{
		{"unknown profile", OwnerCaller, LaunchRequest{Name: "x1", Profile: "nope"}, KindInvalid},
		{"level mismatch", OwnerCaller, LaunchRequest{Name: "x2", Profile: "reviewer", Level: model.LevelLead}, KindInvalid},
		{"head profile", OwnerCaller, LaunchRequest{Name: "x3", Profile: "head"}, KindForbidden},
		{"model outside profile", OwnerCaller, LaunchRequest{Name: "x4", Profile: "reviewer", Model: opus}, KindForbidden},
	}
	for _, tc := range cases {
		if _, err := f.LaunchAgent(tc.c, tc.req); KindOf(err) != tc.kind {
			t.Errorf("%s: err %v (kind %d), want kind %d", tc.name, err, KindOf(err), tc.kind)
		}
	}

	// can_launch is enforced for agents, not for the owner.
	lp, _ := f.GetProfile("lead")
	lp.CanLaunch = []string{"worker"}
	if _, err := f.UpdateProfile(OwnerCaller, "lead", lp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.LaunchAgent(lc, LaunchRequest{Name: "rv2", Profile: "reviewer"}); KindOf(err) != KindForbidden {
		t.Fatalf("lead launched a profile outside can_launch: %v", err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "rv3", Profile: "reviewer", ProjectID: "alpha"}); err != nil {
		t.Fatal(err)
	}
}

func TestProfileCRUD(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	custom := model.Profile{Slug: "docs", Name: " Docs writer ", Level: model.LevelWorker,
		Instructions: "You are {{name}} in {{project}}; {{unknown}} stays.", Model: haiku,
		AllowedTools: []string{"Read", " Read ", "Bash(git log:*)"}, DisallowedTools: []string{"WebFetch"}}
	if _, err := f.CreateProfile(AgentCaller("x"), custom); KindOf(err) != KindForbidden {
		t.Fatal("agent created a profile")
	}
	got, err := f.CreateProfile(OwnerCaller, custom)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Docs writer" || !slices.Equal(got.AllowedModels, []string{haiku}) || got.PermissionMode != "auto" ||
		!slices.Equal(got.AllowedTools, []string{"Read", "Bash(git log:*)"}) || got.BuiltIn || got.CanLaunch == nil {
		t.Fatalf("normalized: %+v", got)
	}
	if _, err := f.CreateProfile(OwnerCaller, custom); KindOf(err) != KindConflict {
		t.Fatal("duplicate slug")
	}
	bad := []model.Profile{
		{Slug: "Bad Slug", Name: "x", Level: model.LevelWorker, Model: haiku},
		{Slug: "b1", Name: "", Level: model.LevelWorker, Model: haiku},
		{Slug: "b2", Name: "x", Level: "boss", Model: haiku},
		{Slug: "b3", Name: "x", Level: model.LevelWorker},
		{Slug: "b4", Name: "x", Level: model.LevelWorker, Model: haiku, PermissionMode: "yolo"},
		{Slug: "b5", Name: "x", Level: model.LevelWorker, Model: haiku, AllowedTools: []string{"Read,Write"}},
		{Slug: "b6", Name: "x", Level: model.LevelLead, Model: haiku, CanLaunch: []string{"ghost"}},
		{Slug: "b7", Name: "x", Level: model.LevelLead, Model: haiku, CanLaunch: []string{"head"}},
	}
	for _, b := range bad {
		if _, err := f.CreateProfile(OwnerCaller, b); KindOf(err) != KindInvalid {
			t.Errorf("%s accepted: %v", b.Slug, err)
		}
	}

	// Allow the lead to launch it, launch one, and see the template and tools.
	lead, _ := f.GetProfile("lead")
	lead.CanLaunch = append(lead.CanLaunch, "docs")
	if _, err := f.UpdateProfile(OwnerCaller, "lead", lead); err != nil {
		t.Fatal(err)
	}
	a, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "dw", Profile: "docs", ProjectID: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	spec := e.chat.Last(a.ID).Spec
	if a.Model != haiku || !strings.HasPrefix(spec.SystemPrompt, "You are dw in alpha; {{unknown}} stays.") ||
		!slices.Equal(spec.AllowedTools, []string{"mcp__tmxg", "Read", "Bash(git log:*)"}) ||
		!slices.Equal(spec.DisallowedTools, []string{"WebFetch"}) {
		t.Fatalf("spec %+v", spec)
	}

	// An edit applies at the next start.
	got.Instructions, got.ReadOnly = "Second version for {{name}}.", true
	if _, err := f.UpdateProfile(OwnerCaller, "docs", got); err != nil {
		t.Fatal(err)
	}
	if _, err := f.RestartAgent(OwnerCaller, "dw"); err != nil {
		t.Fatal(err)
	}
	spec = e.chat.Last(a.ID).Spec
	if !strings.HasPrefix(spec.SystemPrompt, "Second version for dw.") || !slices.Contains(spec.DisallowedTools, "Bash") {
		t.Fatalf("after edit: %+v", spec)
	}
	lvl := got
	lvl.Level = model.LevelLead
	if _, err := f.UpdateProfile(OwnerCaller, "docs", lvl); KindOf(err) != KindConflict {
		t.Fatalf("level change while in use: %v", err)
	}

	// Delete: not while in use; afterwards it leaves the launch rights.
	if err := f.DeleteProfile(OwnerCaller, "docs"); KindOf(err) != KindConflict {
		t.Fatalf("delete in use: %v", err)
	}
	if err := f.RemoveAgent(OwnerCaller, "dw"); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteProfile(OwnerCaller, "docs"); err != nil {
		t.Fatal(err)
	}
	if lead, _ := f.GetProfile("lead"); slices.Contains(lead.CanLaunch, "docs") {
		t.Fatalf("deleted profile still launchable: %v", lead.CanLaunch)
	}

	// Built-ins: not deletable, level fixed, resettable.
	if err := f.DeleteProfile(OwnerCaller, "worker"); KindOf(err) != KindForbidden {
		t.Fatalf("built-in deleted: %v", err)
	}
	w, _ := f.GetProfile("worker")
	w.Level = model.LevelLead
	if _, err := f.UpdateProfile(OwnerCaller, "worker", w); KindOf(err) != KindInvalid {
		t.Fatalf("built-in level change: %v", err)
	}
	w.Level, w.Instructions = model.LevelWorker, "custom"
	if w, err = f.UpdateProfile(OwnerCaller, "worker", w); err != nil || !w.Modified {
		t.Fatalf("edit built-in: %+v %v", w, err)
	}
	if w, err = f.ResetProfile(OwnerCaller, "worker"); err != nil || w.Modified || w.Instructions != workerDefault(t) {
		t.Fatalf("reset: %+v %v", w, err)
	}
	if _, err := f.ResetProfile(OwnerCaller, "reviewer"); KindOf(err) != KindInvalid {
		t.Fatalf("reset of a custom profile: %v", err)
	}

	// Everything survives a daemon restart.
	f2 := e.started()
	if lead2, _ := f2.GetProfile("lead"); !reflect.DeepEqual(lead2.CanLaunch, []string{"worker", "reviewer"}) {
		t.Fatalf("after restart: %+v", lead2.CanLaunch)
	}
}

func workerDefault(t *testing.T) string {
	t.Helper()
	d, _ := model.DefaultProfile("worker")
	return d.Instructions
}

func TestSetPolicyKeepsCustomLaunchRights(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	pol := f.Policy()
	lp := pol.Levels[model.LevelLead]
	lp.Model, lp.AllowedModels = haiku, []string{haiku, "claude-sonnet-5"}
	pol.Levels[model.LevelLead] = lp
	if _, err := f.SetPolicy(OwnerCaller, pol); err != nil {
		t.Fatal(err)
	}
	lead, _ := f.GetProfile("lead")
	if lead.Model != haiku || !slices.Equal(lead.CanLaunch, []string{"worker", "reviewer"}) {
		t.Fatalf("lead %+v", lead)
	}
	lp.CanLaunch = nil
	pol.Levels[model.LevelLead] = lp
	f.SetPolicy(OwnerCaller, pol)
	if lead, _ := f.GetProfile("lead"); len(lead.CanLaunch) != 0 {
		t.Fatalf("lead can still launch %v", lead.CanLaunch)
	}
}

func TestUpdateAgent(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	w, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "w1", ProjectID: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }
	if _, err := f.UpdateAgent(AgentCaller(w.ID), "w1", AgentPatch{Role: str("x")}); KindOf(err) != KindForbidden {
		t.Fatal("agent edited an agent")
	}
	up, err := f.UpdateAgent(OwnerCaller, "w1", AgentPatch{Role: str("tests"), Brief: str("cover the API")})
	if err != nil || !up.Changed || !up.RestartNeeded || up.Agent.Role != "tests" || up.Note != "Saved; applies on restart." {
		t.Fatalf("update: %+v %v", up, err)
	}
	if up, _ := f.UpdateAgent(OwnerCaller, "w1", AgentPatch{Role: str("tests")}); up.Changed || up.RestartNeeded {
		t.Fatalf("no-op: %+v", up)
	}
	if _, err := f.UpdateAgent(OwnerCaller, "w1", AgentPatch{Model: str("not-an-allowed-model")}); KindOf(err) != KindForbidden {
		t.Fatalf("disallowed model: %v", err)
	}
	if _, err := f.UpdateAgent(OwnerCaller, "w1", AgentPatch{Profile: str("lead")}); KindOf(err) != KindInvalid {
		t.Fatalf("profile of another level: %v", err)
	}
	if _, err := f.UpdateAgent(OwnerCaller, "w1", AgentPatch{Profile: str("ghost")}); KindOf(err) != KindInvalid {
		t.Fatalf("unknown profile: %v", err)
	}

	rv, _ := f.GetProfile("reviewer")
	rv.Model, rv.AllowedModels = haiku, []string{haiku}
	f.UpdateProfile(OwnerCaller, "reviewer", rv)
	up, err = f.UpdateAgent(OwnerCaller, "w1", AgentPatch{Profile: str("reviewer")})
	if err != nil || up.Agent.Profile != "reviewer" || up.Agent.Model != haiku {
		t.Fatalf("profile change: %+v %v", up, err)
	}
	// Running process unchanged until restart; then the new profile applies.
	if spec := e.chat.Last(w.ID).Spec; len(spec.DisallowedTools) != 0 {
		t.Fatal("applied without a restart")
	}
	f.RestartAgent(OwnerCaller, "w1")
	spec := e.chat.Last(w.ID).Spec
	if spec.Model != haiku || !slices.Equal(spec.DisallowedTools, model.ReadOnlyDenied) || !strings.Contains(spec.SystemPrompt, "Your role: tests") {
		t.Fatalf("after restart: %+v", spec)
	}
	f.StopAgent(OwnerCaller, "w1")
	if up, _ := f.UpdateAgent(OwnerCaller, "w1", AgentPatch{Brief: str("")}); up.RestartNeeded || up.Note != "Saved; applies when the agent starts." {
		t.Fatalf("stopped: %+v", up)
	}

	sh, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "sh", Harness: model.HarnessShell})
	if _, err := f.UpdateAgent(OwnerCaller, sh.ID, AgentPatch{Model: str(opus)}); KindOf(err) != KindInvalid {
		t.Fatalf("shell model: %v", err)
	}
}

// Agents stored before profiles get the built-in profile of their level.
func TestAgentsWithoutProfileGetTheirLevel(t *testing.T) {
	e := newEnv(t)
	now := model.Now()
	if err := e.st.PutAgent(model.Agent{ID: "old1", Name: "old", Level: model.LevelWorker, Harness: model.HarnessClaude,
		Mode: model.ModeChat, Model: "claude-sonnet-5", State: model.StateStopped, Desired: "stopped",
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	f := e.fleet()
	if a := mustAgent(t, f, "old"); a.Profile != "worker" {
		t.Fatalf("profile %q", a.Profile)
	}
	if a, _ := e.st.GetAgent("old1"); a.Profile != "worker" {
		t.Fatalf("stored profile %q", a.Profile)
	}
}

func TestProjectWithLeadProfile(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	arch := model.Profile{Slug: "architect", Name: "Architect lead", Level: model.LevelLead, Model: "claude-sonnet-5",
		Instructions: "Lead {{project}} at {{project_folder}}.", CanLaunch: []string{"worker"}}
	if _, err := f.CreateProfile(OwnerCaller, arch); err != nil {
		t.Fatal(err)
	}
	head := mustAgent(t, f, "head")
	hc := callerFor(t, f, e.chat, head.ID)
	if _, err := f.CreateProject(hc, ProjectRequest{Name: "beta", Profile: "architect"}); KindOf(err) != KindForbidden {
		t.Fatalf("head used a lead profile outside its can_launch: %v", err)
	}
	if _, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "gamma", Profile: "reviewer"}); KindOf(err) != KindInvalid {
		t.Fatalf("worker profile as lead: %v", err)
	}
	p, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "beta", Profile: "architect"})
	if err != nil {
		t.Fatal(err)
	}
	lead := mustAgent(t, f, "beta-lead")
	if lead.Profile != "architect" || !strings.HasPrefix(e.chat.Last(lead.ID).Spec.SystemPrompt, "Lead beta at "+p.Folder+".") {
		t.Fatalf("lead %+v", lead)
	}
}

func TestRenderInstructions(t *testing.T) {
	got := RenderInstructions("Hi {{ name }}, {{name}}! {{nope}} {{Name}}", map[string]string{"name": "ann"})
	if got != "Hi ann, ann! {{nope}} {{Name}}" {
		t.Fatal(got)
	}
}

func TestProjectLeadAndWorkerModelChoice(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	for _, tc := range []struct{ name, requested, want string }{
		{"default", "", opus}, {"opus", opus, opus}, {"sonnet", "claude-sonnet-5", "claude-sonnet-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: tc.name, Model: tc.requested})
			if err != nil {
				t.Fatal(err)
			}
			lead := mustAgent(t, f, p.LeadID)
			if lead.Model != tc.want {
				t.Fatalf("lead model = %q, want %q", lead.Model, tc.want)
			}
			caller := callerFor(t, f, e.chat, lead.ID)
			for _, grade := range []struct{ name, model string }{{"senior", opus}, {"junior", "claude-sonnet-5"}, {"intern", haiku}} {
				a, err := f.LaunchAgent(caller, LaunchRequest{Name: tc.name + "-" + grade.name, Model: grade.model})
				if err != nil {
					t.Fatal(err)
				}
				if a.Model != grade.model {
					t.Fatalf("%s model = %q", grade.name, a.Model)
				}
			}
		})
	}
	if _, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "bad-model", Model: "unknown"}); KindOf(err) != KindForbidden {
		t.Fatalf("unsupported model: %v", err)
	}
}
