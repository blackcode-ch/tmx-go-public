package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestCompanionPythonViews(t *testing.T) {
	d := apitest.New(t)
	if _, err := d.Fleet.CreateProject(fleet.OwnerCaller, fleet.ProjectRequest{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	a, err := d.Fleet.Agent("alpha-lead")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Fleet.CompanionApproval(a.ID, model.ToolCompanionQuestion, "Choose a date", "private context must stay hidden", model.CompanionApprovalInput{TaskID: "private-task-input"}); err != nil {
		t.Fatal(err)
	}
	link := linkClient(t, d)
	for path, field := range map[string]string{"projects": "projects", "health": "your_turn", "turns": "your_turn"} {
		var result map[string]any
		route := "/api/companion/link/" + path
		if status, _ := call(t, link, "GET", route, nil, &result); status != 0 || result[field] == nil {
			t.Fatalf("%s: %d %v", path, status, result)
		}
		b, _ := json.Marshal(result)
		if strings.Contains(string(b), "private") {
			t.Fatalf("private approval data leaked: %s", b)
		}
		if path == "turns" && (!strings.Contains(string(b), "Choose a date") || !strings.Contains(string(b), "alpha")) {
			t.Fatalf("turn: %s", b)
		}
		if path == "health" && result["your_turn"].(map[string]any)["open"] != float64(1) {
			t.Fatalf("health: %v", result)
		}
		if status, _ := call(t, tc.New(d.URL, d.OwnerToken), "GET", route, nil, nil); status != http.StatusForbidden {
			t.Fatalf("owner on link view: %d", status)
		}
		if status, _ := call(t, link, "POST", route, nil, nil); status != http.StatusForbidden {
			t.Fatalf("write on read view: %d", status)
		}
	}
}
