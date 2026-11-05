package api_test

import (
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"testing"
)

func TestProjectArtifactsAPI(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	var p model.Project
	if code := owner.do("POST", "/api/projects", map[string]any{"name": "Artifacts"}, &p); code != 201 {
		t.Fatal(code)
	}
	lead := client{t: t, base: d.URL, token: d.AgentToken(t, "artifacts-lead")}
	head := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	anon := client{t: t, base: d.URL}
	path := "/api/projects/" + p.ID
	output := []model.ProjectOutput{{Path: "https://example.test/result", What: "Result"}}
	var part model.ProjectContribution
	if code := lead.do("POST", path+"/contribution", map[string]any{"status": "Ready", "final_output": output}, &part); code != 200 || part.AgentID != p.LeadID {
		t.Fatalf("%d %+v", code, part)
	}
	if code := head.do("POST", path+"/contribution", map[string]any{"status": "Overwrite"}, nil); code != 403 {
		t.Fatal(code)
	}
	if code := anon.do("GET", path+"/artifacts", nil, nil); code != 401 {
		t.Fatal(code)
	}
	var view model.ProjectArtifacts
	if code := owner.do("GET", path+"/artifacts", nil, &view); code != 200 || len(view.FinalOutput) != 1 || view.FinalOutput[0].Kind != "link" {
		t.Fatalf("%d %+v", code, view)
	}
}
