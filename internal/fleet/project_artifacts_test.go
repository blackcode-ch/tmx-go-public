package fleet

import (
	"github.com/blackcode-ch/tmx-go/internal/model"
	"testing"
)

func TestContributionOwnershipAndPersistence(t *testing.T) {
	e, f, p := sectionFixture(t)
	f.agents["worker"] = &agentRec{a: model.Agent{ID: "worker", Name: "worker", ProjectID: p.ID}}
	status := "Ready"
	outputs := []model.ProjectOutput{{Path: "build/result.txt", What: "Result"}}
	for _, c := range []Caller{OwnerCaller, {AgentID: "head"}, {AgentID: "unknown"}} {
		if _, err := f.SetMyProjectContribution(c, p.ID, ProjectContributionPatch{Status: &status}); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	part, err := f.SetMyProjectContribution(Caller{AgentID: "worker"}, p.Slug, ProjectContributionPatch{Status: &status, FinalOutput: &outputs})
	if err != nil || part.AgentID != "worker" {
		t.Fatalf("%+v %v", part, err)
	}
	outputs[0].Path = "mutated"
	_, err = f.SetMyProjectContribution(Caller{AgentID: "lead"}, p.ID, ProjectContributionPatch{Status: &status})
	if err != nil {
		t.Fatal(err)
	}
	f.agents["worker"].a.Name = "renamed"
	part, err = f.SetMyProjectContribution(Caller{AgentID: "worker"}, p.ID, ProjectContributionPatch{})
	if err != nil || part.AgentName != "renamed" || part.Status != status || part.FinalOutput[0].Path != "build/result.txt" {
		t.Fatalf("%+v %v", part, err)
	}
	view, err := e.fleet().ProjectArtifacts(OwnerCaller, p.ID)
	if err != nil || len(view.Contributions) != 2 || len(view.FinalOutput) != 1 || view.FinalOutput[0].AgentID != "worker" {
		t.Fatalf("%+v %v", view, err)
	}
	empty, list := "", []model.ProjectOutput{}
	part, err = f.SetMyProjectContribution(Caller{AgentID: "worker"}, p.ID, ProjectContributionPatch{Status: &empty, FinalOutput: &list})
	if err != nil || part.Status != "" || len(part.FinalOutput) != 0 {
		t.Fatalf("%+v %v", part, err)
	}
}

func TestFinalArtifactsLinksAndAuthority(t *testing.T) {
	_, f, p := sectionFixture(t)
	f.agents["worker"] = &agentRec{a: model.Agent{ID: "worker", Name: "worker", ProjectID: p.ID}}
	outputs := []model.ProjectOutput{{Path: "https://example.test/report?q=a%2Fb#result"}, {Path: "HTTP://example.test/guide"}, {Path: "out/report.txt"}, {Path: `C:\build\result.txt`}}
	if _, err := f.SetProjectSection(Caller{AgentID: "worker"}, p.ID, ProjectSectionPatch{FinalOutput: &outputs}); err == nil {
		t.Fatal("worker changed shared output")
	}
	if _, err := f.SetProjectSection(Caller{AgentID: "lead"}, p.ID, ProjectSectionPatch{FinalOutput: &outputs}); err != nil {
		t.Fatal(err)
	}
	view, err := f.ProjectArtifacts(OwnerCaller, p.ID)
	if err != nil || len(view.FinalOutput) != 4 {
		t.Fatalf("%+v %v", view, err)
	}
	for i, want := range []string{"link", "link", "path", "path"} {
		if view.FinalOutput[i].Kind != want || view.FinalOutput[i].Path != outputs[i].Path {
			t.Fatalf("%+v", view.FinalOutput[i])
		}
	}
	if _, err := f.ProjectArtifacts(Caller{AgentID: "unknown"}, p.ID); err == nil {
		t.Fatal("unknown reader")
	}
	changed := "Should not persist"
	for _, bad := range [][]model.ProjectOutput{{{Path: ""}}, {{Path: "https:/broken"}}, {{Path: "file\nname"}}, make([]model.ProjectOutput, 21)} {
		if _, err := f.SetProjectSection(OwnerCaller, p.ID, ProjectSectionPatch{Status: &changed, FinalOutput: &bad}); err == nil {
			t.Fatal("invalid output accepted")
		}
		if _, err := f.SetMyProjectContribution(Caller{AgentID: "lead"}, p.ID, ProjectContributionPatch{Status: &changed, FinalOutput: &bad}); err == nil {
			t.Fatal("invalid contribution accepted")
		}
	}
	sec, _ := f.ProjectSection(OwnerCaller, p.ID)
	if sec.Status != "" || len(sec.FinalOutput) != 4 {
		t.Fatalf("partial write %+v", sec)
	}
	empty := []model.ProjectOutput{}
	if _, err := f.SetProjectSection(OwnerCaller, p.ID, ProjectSectionPatch{FinalOutput: &empty}); err != nil {
		t.Fatal(err)
	}
	view, _ = f.ProjectArtifacts(OwnerCaller, p.ID)
	if len(view.FinalOutput) != 0 || len(view.Contributions) != 0 {
		t.Fatalf("%+v", view)
	}
}
