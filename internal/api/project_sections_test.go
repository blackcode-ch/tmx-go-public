package api_test

import (
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"testing"
)

func TestProjectSectionAPI(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	anon := client{t: t, base: d.URL}
	var p model.Project
	if code := owner.do("POST", "/api/projects", map[string]any{"name": "Section API"}, &p); code != 201 {
		t.Fatal(code)
	}
	agent = client{t: t, base: d.URL, token: d.AgentToken(t, "section-api-lead")}
	path := "/api/projects/" + p.ID
	var sec model.ProjectSection
	if code := agent.do("POST", path+"/section", map[string]any{"status": "Ready", "needs": []string{"Review"}}, &sec); code != 200 || sec.Status != "Ready" {
		t.Fatalf("%d %+v", code, sec)
	}
	if code := anon.do("GET", path+"/section", nil, nil); code != 401 {
		t.Fatal(code)
	}
	if code := agent.do("POST", path+"/feedback", map[string]any{"id": "0123456789abcdef", "text": "Forged"}, nil); code != 403 {
		t.Fatal(code)
	}
	var rec model.ProjectFeedback
	if code := owner.do("POST", path+"/feedback", map[string]any{"id": "0123456789abcdef", "done": []string{"Review"}}, &rec); code != 200 || len(rec.Messages) == 0 {
		t.Fatalf("%d %+v", code, rec)
	}
	if code := owner.do("GET", path+"/section", nil, &sec); code != 200 || !sec.Needs[0].Done {
		t.Fatalf("%d %+v", code, sec)
	}
}
