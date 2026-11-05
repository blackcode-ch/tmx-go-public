package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// Cards stored before the rename say "owner_only"; they still read as owner-only, and are
// written with "owner_only" (plus the legacy key, for peers on older versions).
func TestOwnerCardReadsLegacyOwnerOnly(t *testing.T) {
	for in, want := range map[string]bool{
		`{"type":"decision","owner_only":true}`: true,
		`{"type":"decision","owner_only":true}`:  true,
		`{"type":"decision"}`:                    false,
	} {
		var c OwnerCard
		if err := json.Unmarshal([]byte(in), &c); err != nil || c.OwnerOnly != want || c.Type != "decision" {
			t.Fatalf("%s: %+v %v", in, c, err)
		}
	}
	var a Approval
	if err := json.Unmarshal([]byte(`{"id":"x","card":{"type":"decision","priority":2,"owner_only":true}}`), &a); err != nil ||
		a.Card == nil || !a.Card.OwnerOnly || a.Card.Priority != 2 {
		t.Fatalf("approval: %+v %v", a.Card, err)
	}
	b, _ := json.Marshal(a.Card)
	if !strings.Contains(string(b), `"owner_only":true`) {
		t.Fatalf("marshal: %s", b)
	}
}
