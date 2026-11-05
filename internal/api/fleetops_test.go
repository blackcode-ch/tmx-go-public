package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Fleet operations without the owner's token (api/fleetops.go): the head and the leads read
// the owner's status and the log and restart the daemon; a worker, Companion and anyone else
// cannot; nothing returned carries a token, PIN or hash.
func TestFleetOpsAccess(t *testing.T) {
	d := apitest.New(t)
	owner := client{t, d.URL, d.OwnerToken}
	if s := owner.do("POST", "/api/projects", map[string]string{"name": "Alpha"}, nil); s != 201 {
		t.Fatalf("project: %d", s)
	}
	var w model.Agent
	if s := owner.do("POST", "/api/agents", map[string]string{"name": "w1", "project_id": workerProject(t, d)}, &w); s != 201 {
		t.Fatalf("worker: %d", s)
	}
	head := client{t, d.URL, d.AgentToken(t, "head")}
	lead := client{t, d.URL, d.AgentToken(t, "alpha-lead")}
	worker := client{t, d.URL, d.AgentToken(t, "w1")}
	anon := client{t, d.URL, ""}

	var st map[string]json.RawMessage
	if s := head.do("GET", "/api/fleet-ops/status", nil, &st); s != http.StatusOK {
		t.Fatalf("head status: %d", s)
	}
	if string(st["pin_set"]) != "false" || st["devices"] == nil {
		t.Fatalf("status: %v", st)
	}
	for k := range st {
		if k != "devices" && k != "pin_set" && k != "gate" {
			t.Fatalf("unexpected field %q", k)
		}
	}
	if s := lead.do("GET", "/api/fleet-ops/status", nil, nil); s != http.StatusOK {
		t.Fatalf("lead status: %d", s)
	}
	for _, c := range []struct {
		name string
		cl   client
		want int
	}{{"worker", worker, 403}, {"owner token (has the full API)", owner, 403}, {"anonymous", anon, 401}} {
		for _, p := range []string{"/api/fleet-ops/status", "/api/fleet-ops/log"} {
			if s := c.cl.do("GET", p, nil, nil); s != c.want {
				t.Errorf("%s GET %s: %d, want %d", c.name, p, s, c.want)
			}
		}
		if s := c.cl.do("POST", "/api/fleet-ops/restart", map[string]any{}, nil); s != c.want {
			t.Errorf("%s restart: %d, want %d", c.name, s, c.want)
		}
	}
	// The test daemon has no self-restart wired: the head is admitted, and told so.
	if s := head.do("POST", "/api/fleet-ops/restart", map[string]any{}, nil); s != http.StatusNotImplemented {
		t.Fatalf("head restart without the hook: %d", s)
	}
}

func TestRedactLog(t *testing.T) {
	testToken := strings.Repeat("test", 4)
	for in, want := range map[string]string{
		`level=INFO msg="pair token stored" token=` + testToken + ` peer=hosta`: `level=INFO msg="pair token stored" token=[redacted] peer=hosta`,
		`Authorization: Bearer 9f8e7d6c5b4a39281706f5e4d3c2b1a0ffeeddccbbaa9988`: `Authorization: Bearer [redacted]`,
		`device session_hash 0123456789abcdef0123456789abcdef saved`:             `device session_hash [redacted] saved`,
		`{"salt":"000102030405060708090a0b0c0d0e0f","hash":"53e5"}`:              `{"salt":"[redacted]","hash":"[redacted]"}`,
		`pin=482915 wrong`: `pin=[redacted] wrong`,
		`msg="browser device event" event=paired device=short`: `msg="browser device event" event=paired device=short`,
	} {
		if got := api.RedactLog(in); got != want {
			t.Errorf("%s\n got  %s\n want %s", in, got, want)
		}
		if strings.Contains(api.RedactLog(in), "482915") {
			t.Error("PIN survived")
		}
	}
}
