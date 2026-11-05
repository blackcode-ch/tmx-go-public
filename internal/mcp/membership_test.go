package mcp

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
)

// The feature pass on Hostb (3 Nov, tmx-go #188): the matrix tool's op=get had no id, and a
// lead with no project could not launch workers.

func TestMatrixGetByIDAndNoThread(t *testing.T) {
	d := apitest.New(t)
	head := session(t, d, "head")

	out := mustOK(t, head, "matrix", map[string]any{"op": "new", "thread": "Ship the app"})
	i, j := strings.Index(out, "["), strings.Index(out, "]")
	if i < 0 || j < i {
		t.Fatalf("op=new: %s", out)
	}
	id := out[i+1 : j]

	for _, args := range []map[string]any{{"op": "get", "id": id}, {"op": "get", "thread": id}, {"op": "get", "thread": "Ship the app"}} {
		if got := mustOK(t, head, "matrix", args); !strings.Contains(got, `Thread "Ship the app" [`+id+`]`) {
			t.Fatalf("op=get %v: %s", args, got)
		}
	}
	// No thread: a clear error, never GET /api/matrix/threads/ ("no such API route").
	for _, op := range []string{"get", "state", "attach"} {
		got, isErr := call(t, head, "matrix", map[string]any{"op": op})
		if !isErr || !strings.Contains(got, "needs the thread") || strings.Contains(got, "no such API route") {
			t.Fatalf("op=%s with no thread: %v %s", op, isErr, got)
		}
	}
}

func TestProjectMemberLetsALeadLaunch(t *testing.T) {
	d := apitest.New(t)
	head := session(t, d, "head")
	mustOK(t, head, "project_create", map[string]any{"name": "Shop", "brief": "an online shop"})
	shopLead, err := d.Fleet.Agent("shop-lead")
	if err != nil {
		t.Fatal(err)
	}
	shop := shopLead.ProjectID

	// A lead launched by the head with no project.
	mustOK(t, head, "agent_launch", map[string]any{"name": "app-lead", "profile": "lead", "task": "build the app"})
	if a, _ := d.Fleet.Agent("app-lead"); a.ProjectID != "" || a.Level != "lead" {
		t.Fatalf("app-lead: level %s, project %q", a.Level, a.ProjectID)
	}
	appLead := session(t, d, "app-lead")
	if got, isErr := call(t, appLead, "agent_launch", map[string]any{"name": "app-w0", "task": "x"}); !isErr || !strings.Contains(got, "project_member_add") {
		t.Fatalf("launch with no project: %v %s", isErr, got)
	}

	// Never on its own behalf, nor by a worker of the project.
	if got, isErr := call(t, appLead, "project_member_add", map[string]any{"project": "shop", "agent": "app-lead"}); !isErr || !strings.Contains(got, "only the owner, the head or the project's lead") {
		t.Fatalf("self-add: %v %s", isErr, got)
	}
	sl := session(t, d, "shop-lead")
	mustOK(t, sl, "agent_launch", map[string]any{"name": "shop-api", "task": "the API"})
	if got, isErr := call(t, session(t, d, "shop-api"), "project_member_add", map[string]any{"project": "shop", "agent": "app-lead"}); !isErr {
		t.Fatalf("a worker added a member: %s", got)
	}
	// Another project's lead is never pulled in (go-reviewer-o, 3 Nov).
	mustOK(t, head, "project_create", map[string]any{"name": "Wiki"})
	if got, isErr := call(t, sl, "project_member_add", map[string]any{"project": "shop", "agent": "wiki-lead"}); !isErr || !strings.Contains(got, "wiki") {
		t.Fatalf("another project's lead added: %v %s", isErr, got)
	}
	// The head belongs to no project.
	if _, isErr := call(t, head, "project_member_add", map[string]any{"project": "shop", "agent": "head"}); !isErr {
		t.Fatal("the head was added to a project")
	}

	// The project's lead adds it; adding it again is a no-op.
	mustOK(t, sl, "project_member_add", map[string]any{"project": "shop", "agent": "app-lead"})
	mustOK(t, head, "project_member_add", map[string]any{"project": "Shop", "agent": "app-lead"})
	if a, _ := d.Fleet.Agent("app-lead"); a.ProjectID != shop {
		t.Fatalf("app-lead project %q, want %q", a.ProjectID, shop)
	}
	mustOK(t, appLead, "agent_launch", map[string]any{"name": "app-w1", "task": "the screens"})
	if w, err := d.Fleet.Agent("app-w1"); err != nil || w.ProjectID != shop {
		t.Fatalf("app-w1: project %q, err %v", w.ProjectID, err)
	}

	// One project per agent: a second project can't take it.
	if got, isErr := call(t, head, "project_create", map[string]any{"name": "Blog", "members": []string{"app-lead"}}); !isErr || !strings.Contains(got, "already in project") {
		t.Fatalf("member of two projects: %v %s", isErr, got)
	}
	if _, err := d.Fleet.Agent("blog-lead"); err == nil {
		t.Fatal("a refused project_create still created the project's lead")
	}

	// project_create with members: they join at once.
	mustOK(t, head, "agent_launch", map[string]any{"name": "docs-writer-lead", "profile": "lead", "task": "write"})
	mustOK(t, head, "project_create", map[string]any{"name": "Docs", "members": []string{"docs-writer-lead"}})
	docsLead, _ := d.Fleet.Agent("docs-lead")
	if a, _ := d.Fleet.Agent("docs-writer-lead"); a.ProjectID == "" || a.ProjectID != docsLead.ProjectID {
		t.Fatalf("docs-writer-lead project %q, want %q", a.ProjectID, docsLead.ProjectID)
	}
}
