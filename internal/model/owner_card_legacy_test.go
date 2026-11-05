package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// withLegacyKeys sets LegacyOwnerOnlyKeys for one test (a neutral stand-in for the private build's
// earlier name, so this test reads the same in the public snapshot).
func withLegacyKeys(t *testing.T, keys ...string) {
	old := LegacyOwnerOnlyKeys
	LegacyOwnerOnlyKeys = keys
	t.Cleanup(func() { LegacyOwnerOnlyKeys = old })
}

func TestOwnerCardLegacyOwnerOnly(t *testing.T) {
	withLegacyKeys(t, "legacy_only")
	// Written under both names, so a peer or a rolled-back binary on the earlier name still sees it.
	b, err := json.Marshal(OwnerCard{Type: "decision", OwnerOnly: true, Urgent: true})
	if err != nil || !strings.Contains(string(b), `"owner_only":true`) || !strings.Contains(string(b), `"legacy_only":true`) {
		t.Fatalf("marshal: %s %v", b, err)
	}
	var back OwnerCard
	if err := json.Unmarshal(b, &back); err != nil || !back.OwnerOnly || !back.Urgent || back.Type != "decision" {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	// Read under the earlier name alone (a stored card or an older peer), via the *OwnerCard field too.
	var a struct {
		Card *OwnerCard `json:"card"`
	}
	if err := json.Unmarshal([]byte(`{"card":{"type":"decision","legacy_only":true,"priority":2}}`), &a); err != nil ||
		a.Card == nil || !a.Card.OwnerOnly || a.Card.Priority != 2 {
		t.Fatalf("legacy read: %+v %v", a.Card, err)
	}
	// Not owner-only: neither key is written.
	if b, _ := json.Marshal(OwnerCard{Type: "decision"}); strings.Contains(string(b), "only") {
		t.Fatalf("not owner-only: %s", b)
	}
	// A legacy false never turns it on.
	var off OwnerCard
	if err := json.Unmarshal([]byte(`{"type":"decision","legacy_only":false}`), &off); err != nil || off.OwnerOnly {
		t.Fatalf("legacy false: %+v %v", off, err)
	}
}

func TestOwnerCardNoLegacyKeys(t *testing.T) {
	withLegacyKeys(t) // a public build
	b, _ := json.Marshal(OwnerCard{Type: "decision", OwnerOnly: true})
	if string(b) != `{"owner_only":true,"type":"decision"}` {
		t.Fatalf("marshal: %s", b)
	}
	var c OwnerCard
	if err := json.Unmarshal([]byte(`{"type":"decision","legacy_only":true}`), &c); err != nil || c.OwnerOnly {
		t.Fatalf("an unknown key must not count: %+v %v", c, err)
	}
}
