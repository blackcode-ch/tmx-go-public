package fleet

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestProjectCreateAssignedLead(t *testing.T) {
	e, f, _, original, caller := peerEnv(t)
	for _, tc := range []struct{ name, lead, want string }{
		{"local", original.Name, original.ID}, {"local-qualified", original.Name + "@testbox", original.ID},
		{"remote", "project-lead@hostc", PeerParty("project-lead", "hostc")},
		{"none", "none", ""}, {"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := f.Snapshot()
			p, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: tc.name, Lead: &tc.lead})
			if err != nil {
				t.Fatal(err)
			}
			if p.LeadID != tc.want {
				t.Fatalf("lead = %q, want %q", p.LeadID, tc.want)
			}
			after, _ := f.Snapshot()
			if len(after.Agents) != len(before.Agents) {
				t.Fatal("assignment launched an agent")
			}
			stored, err := e.st.GetProject(p.ID)
			if err != nil || stored.LeadID != tc.want {
				t.Fatalf("stored lead: %+v %v", stored, err)
			}
			if tc.name == "local" {
				w, err := f.LaunchAgent(caller, LaunchRequest{Name: "assigned-worker", ProjectID: p.ID})
				if err != nil || w.ProjectID != p.ID || w.ParentID != original.ID {
					t.Fatalf("assigned lead worker: %+v %v", w, err)
				}
				if a := mustAgent(t, f, original.ID); a.ProjectID != original.ProjectID || a.SessionID != original.SessionID {
					t.Fatal("existing lead moved or restarted")
				}
			}
			if tc.name == "remote" {
				if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "replacement-lead", Level: model.LevelLead, ProjectID: p.ID}); KindOf(err) != KindConflict {
					t.Fatalf("remote lead replaced: %v", err)
				}
				sec, err := f.ProjectSection(OwnerCaller, p.ID)
				if err != nil || sec.Lead != tc.lead {
					t.Fatalf("section lead: %+v %v", sec, err)
				}
			}
		})
	}
}

func TestProjectCreateRejectsInvalidLeadBeforeMutation(t *testing.T) {
	e, f, _, _, _ := peerEnv(t)
	worker, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "not-lead", ProjectID: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"missing", worker.Name, "lead@unknown", "owner@hostc"} {
		before, _ := f.Snapshot()
		_, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "rejected", Lead: &target})
		if err == nil {
			t.Fatalf("accepted %q", target)
		}
		after, _ := f.Snapshot()
		if len(after.Projects) != len(before.Projects) {
			t.Fatal("rejected assignment created project")
		}
		if _, err := os.Stat(filepath.Join(e.workspace, "rejected")); !os.IsNotExist(err) {
			t.Fatalf("created folder: %v", err)
		}
	}
	unrelated, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "unrelated"})
	if err != nil {
		t.Fatal(err)
	}
	original := mustAgent(t, f, "alpha-lead")
	if _, err := f.LaunchAgent(AgentCaller(original.ID), LaunchRequest{Name: "cross-project", ProjectID: unrelated.ID}); KindOf(err) != KindForbidden {
		t.Fatalf("unassigned lead launched across projects: %v", err)
	}
	none := "none"
	if _, err := f.CreateProject(AgentCaller(worker.ID), ProjectRequest{Name: "worker-created", Lead: &none}); KindOf(err) != KindForbidden {
		t.Fatalf("worker created project: %v", err)
	}
	if _, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "ambiguous", Lead: &none, Profile: "lead"}); KindOf(err) != KindInvalid {
		t.Fatalf("ignored launch option: %v", err)
	}
}

func TestAssignedProjectPreservesMembersAndRejectsLaunchModel(t *testing.T) {
	_, f, _, _, _ := peerEnv(t)
	none := "none"
	if _, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "model-without-launch", Lead: &none, Model: "claude-opus-5-5"}); KindOf(err) != KindInvalid {
		t.Fatalf("ignored launch model: %v", err)
	}
	member, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "unassigned-member", Profile: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "member-without-lead", Lead: &none, Members: []string{member.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustAgent(t, f, member.ID); got.ProjectID != p.ID {
		t.Fatalf("member not joined: %+v", got)
	}
}
