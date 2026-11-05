package api_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestAgentProjectRoutes(t *testing.T) {
	d := apitest.New(t)
	o := client{t: t, base: d.URL, token: d.OwnerToken}
	var a model.Agent
	if code := o.do("POST", "/api/agents", map[string]any{"name": "page", "project_id": workerProject(t, d)}, &a); code != 201 {
		t.Fatal(code)
	}
	w := client{t: t, base: d.URL, token: d.AgentToken(t, a.ID)}
	var view fleet.AgentProjectView
	patch := map[string]any{"what": "A small project", "status": "In progress", "progress": 50, "expected_progress_version": 0, "progress_basis": map[string]any{"revision": "v1", "steps": []map[string]any{{"id": "a", "label": "First", "done": true}, {"id": "b", "label": "Second", "done": false}}}}
	if code := w.do("POST", "/api/sessions/page/project", patch, &view); code != 200 || view.Progress.Version != 1 || *view.Progress.Percent != 50 {
		t.Fatalf("write: %d %+v", code, view)
	}
	if code := w.do("POST", "/api/agents/page/project", patch, nil); code != 409 {
		t.Fatalf("stale progress: %d", code)
	}
	if code := w.do("POST", "/api/agents/page/project", map[string]any{"status": "new", "unknown": true}, nil); code != 400 {
		t.Fatalf("unknown: %d", code)
	}
	if code := w.do("GET", "/api/agents/page/project", nil, &view); code != 200 || string(view.Brief["status"]) != `"In progress"` {
		t.Fatalf("atomic merge: %d %+v", code, view)
	}
	if code := w.do("POST", "/api/agents/page/project", map[string]string{"folder": t.TempDir()}, nil); code != 403 {
		t.Fatalf("outside folder: %d", code)
	}
	sub := filepath.Join(a.Cwd, "project")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if code := w.do("POST", "/api/agents/page/project", map[string]string{"folder": "project"}, &view); code != 200 || view.Folder != sub {
		t.Fatalf("inside folder: %d %+v", code, view)
	}
	if code := w.do("POST", "/api/agents/page/project", map[string]string{"folder": ""}, &view); code != 200 || view.Folder != a.Cwd {
		t.Fatalf("reset folder: %d %+v", code, view)
	}
	if code := w.do("GET", "/api/agents/head/project", nil, nil); code != 403 {
		t.Fatalf("read other: %d", code)
	}
	if code := w.do("POST", "/api/agents/page/update", map[string]string{"role": "changed"}, nil); code != 403 {
		t.Fatalf("worker update: %d", code)
	}
	if code := o.do("POST", "/api/sessions/page/update", map[string]string{"role": "changed"}, nil); code != 200 {
		t.Fatalf("owner update: %d", code)
	}
	if code := o.do("POST", "/api/agents/page/update", map[string]string{"successor": "head"}, nil); code != 400 {
		t.Fatalf("unsupported update: %d", code)
	}
}
