package fleet

import (
	"encoding/json"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// A migration batch may carry the owner-only flag under an earlier name. Reading it must keep
// every other field: ProjectCardImport embeds ProjectCardRequest, so a decoder placed on the
// embedded type would silently drop asker, status and answer.
func TestProjectImportLegacyOwnerOnly(t *testing.T) {
	old := model.LegacyOwnerOnlyKeys
	model.LegacyOwnerOnlyKeys = []string{"legacy_only"}
	t.Cleanup(func() { model.LegacyOwnerOnlyKeys = old })

	in := `{"id":"batch-1","source":"export","cards":[
		{"id":"c1","question":"Choose","legacy_only":true,"asker":"lead-a","status":"answered","answer":"A","created_at":5},
		{"id":"c2","question":"Other","owner_only":true,"asker":"lead-b","status":"open","created_at":6},
		{"id":"c3","question":"Third","asker":"lead-c","status":"open","created_at":7}]}`
	var req ProjectImportRequest
	if err := json.Unmarshal([]byte(in), &req); err != nil {
		t.Fatal(err)
	}
	if req.ID != "batch-1" || req.Source != "export" || len(req.Cards) != 3 {
		t.Fatalf("batch: %+v", req)
	}
	c1, c2, c3 := req.Cards[0], req.Cards[1], req.Cards[2]
	if !c1.OwnerOnly || c1.Asker != "lead-a" || c1.Status != "answered" || c1.Answer != "A" || c1.CreatedAt != 5 || c1.ID != "c1" || c1.Question != "Choose" {
		t.Fatalf("legacy card: %+v", c1)
	}
	if !c2.OwnerOnly || c2.Asker != "lead-b" || c3.OwnerOnly || c3.Asker != "lead-c" {
		t.Fatalf("other cards: %+v %+v", c2, c3)
	}
}

// close_when names project states; the neutral WAITING_ON_OWNER is always accepted (the private
// build adds the fleet's own name for it), and every listed state matches.
func TestProjectCardCloseWhenStates(t *testing.T) {
	for _, s := range projectCardStates {
		if !projectCardCloseWhen.MatchString("state:alpha:" + s) {
			t.Errorf("state %s rejected", s)
		}
	}
	if !projectCardCloseWhen.MatchString("state:alpha:WAITING_ON_OWNER") || projectCardCloseWhen.MatchString("state:alpha:WAITING_ON_NOBODY") {
		t.Fatal("close_when state list")
	}
}
