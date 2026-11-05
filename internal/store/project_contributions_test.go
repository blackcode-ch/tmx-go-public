package store

import (
	"github.com/blackcode-ch/tmx-go/internal/model"
	"testing"
)

func TestContributionIsolationAndCascade(t *testing.T) {
	s := openTest(t)
	for _, id := range []string{"a", "b"} {
		if err := s.PutProject(model.Project{ID: id, Slug: id}); err != nil {
			t.Fatal(err)
		}
		for _, agent := range []string{"one", "two"} {
			if err := s.PutProjectContribution(model.ProjectContribution{ProjectID: id, AgentID: agent, Status: "original"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.PutProjectContribution(model.ProjectContribution{ProjectID: "a", AgentID: "one", Status: "changed"}); err != nil {
		t.Fatal(err)
	}
	a, err := s.ProjectContributions("a")
	if err != nil || len(a) != 2 || a[0].Status != "changed" || a[1].Status != "original" {
		t.Fatalf("%+v %v", a, err)
	}
	b, err := s.ProjectContributions("b")
	if err != nil || len(b) != 2 || b[0].Status != "original" {
		t.Fatalf("%+v %v", b, err)
	}
	if err := s.DeleteProject("a"); err != nil {
		t.Fatal(err)
	}
	a, err = s.ProjectContributions("a")
	if err != nil || len(a) != 0 {
		t.Fatalf("%+v %v", a, err)
	}
}
