package mcp

import (
	"regexp"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

// The Python People tool names work: a draft is stored (never sent), listed, then closed.
func TestPersonTools(t *testing.T) {
	d := apitest.New(t)
	if _, err := d.Fleet.PutPerson(fleet.OwnerCaller, fleet.PersonInput{Key: "sam"}); err != nil {
		t.Fatal(err)
	}
	cs := session(t, d, "head")
	out, isErr := call(t, cs, "tmx_person_propose", map[string]any{"person": "sam", "title": "Ask for the invoice", "draft": "Hi Sam"})
	if isErr || !strings.Contains(out, "nothing was sent") {
		t.Fatalf("propose: %s", out)
	}
	id := regexp.MustCompile(`Stored as (\S+)`).FindStringSubmatch(out)[1]
	if out, _ := call(t, cs, "tmx_person_outbox", nil); !strings.Contains(out, "Ask for the invoice") {
		t.Fatalf("outbox: %s", out)
	}
	if out, isErr := call(t, cs, "tmx_person_ask_close", map[string]any{"id": id}); isErr || !strings.Contains(out, "done") {
		t.Fatalf("close: %s", out)
	}
	if out, _ := call(t, cs, "tmx_person_outbox", nil); out != "Nothing." {
		t.Fatalf("outbox after close: %s", out)
	}
	if out, isErr := call(t, cs, "tmx_person_draft_withdraw", map[string]any{"id": id}); isErr || !strings.Contains(out, "dropped") {
		t.Fatalf("withdraw: %s", out)
	}
}
