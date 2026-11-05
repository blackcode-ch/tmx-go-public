package api_test

import (
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestProfileRoutes(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}

	var list []model.Profile
	if s := agent.do("GET", "/api/profiles", nil, &list); s != 200 || len(list) != 4 || list[3].Slug != "reviewer" {
		t.Fatalf("list: %d %+v", s, list)
	}
	for _, r := range []struct{ m, p string }{
		{"POST", "/api/profiles"}, {"PUT", "/api/profiles/worker"}, {"DELETE", "/api/profiles/reviewer"},
		{"POST", "/api/profiles/worker/reset"}, {"PATCH", "/api/agents/head"},
	} {
		if s := agent.do(r.m, r.p, `{}`, nil); s != 403 {
			t.Errorf("agent %s %s: %d", r.m, r.p, s)
		}
	}

	p := model.Profile{Slug: "qa", Name: "QA", Level: model.LevelWorker, Model: "claude-sonnet-5-5", ReadOnly: true,
		Instructions: "Test {{project}}."}
	var got model.Profile
	if s := owner.do("POST", "/api/profiles", p, &got); s != 201 || got.Slug != "qa" || got.BuiltIn {
		t.Fatalf("create: %d %+v", s, got)
	}
	if s := owner.do("POST", "/api/profiles", p, nil); s != 409 {
		t.Fatalf("duplicate: %d", s)
	}
	got.Description = "Runs the tests"
	if s := owner.do("PUT", "/api/profiles/qa", got, &got); s != 200 || got.Description != "Runs the tests" {
		t.Fatalf("put: %d %+v", s, got)
	}
	if s := owner.do("GET", "/api/profiles/qa", nil, &got); s != 200 || got.Name != "QA" {
		t.Fatalf("get: %d", s)
	}
	if s := owner.do("GET", "/api/profiles/ghost", nil, nil); s != 404 {
		t.Fatalf("get unknown: %d", s)
	}

	var a model.Agent
	if s := owner.do("POST", "/api/agents", map[string]any{"name": "q1", "profile": "qa", "project_id": workerProject(t, d)}, &a); s != 201 || a.Profile != "qa" || a.Level != model.LevelWorker {
		t.Fatalf("launch by profile: %d %+v", s, a)
	}
	if s := owner.do("DELETE", "/api/profiles/qa", nil, nil); s != 409 {
		t.Fatalf("delete in use: %d", s)
	}
	var up struct {
		Agent         model.Agent `json:"agent"`
		RestartNeeded bool        `json:"restart_needed"`
		Note          string      `json:"note"`
	}
	if s := owner.do("PATCH", "/api/agents/q1", map[string]any{"role": "e2e", "profile": "worker"}, &up); s != 200 ||
		up.Agent.Role != "e2e" || up.Agent.Profile != "worker" || !up.RestartNeeded || up.Note == "" {
		t.Fatalf("patch: %d %+v", s, up)
	}
	if s := owner.do("PATCH", "/api/agents/q1", map[string]any{"model": "gpt"}, nil); s != 403 {
		t.Fatalf("patch disallowed model: %d", s)
	}
	if s := owner.do("DELETE", "/api/profiles/qa", nil, nil); s != 200 {
		t.Fatalf("delete: %d", s)
	}
	if s := owner.do("DELETE", "/api/profiles/worker", nil, nil); s != 403 {
		t.Fatalf("delete built-in: %d", s)
	}
	if s := owner.do("POST", "/api/profiles/worker/reset", nil, &got); s != 200 || got.Modified {
		t.Fatalf("reset: %d %+v", s, got)
	}
	var snap model.Snapshot
	if owner.do("GET", "/api/snapshot", nil, &snap); len(snap.Profiles) != 4 {
		t.Fatalf("snapshot profiles: %+v", snap.Profiles)
	}
}
