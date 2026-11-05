package api_test

import (
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"testing"
)

func TestHarnessesDiscoverAdoptRoutes(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	var hs []struct {
		Name       string
		Modes      []string
		Structured bool
		Installed  bool
	}
	if s := agent.do("GET", "/api/harnesses", nil, &hs); s != 200 || len(hs) != 7 || hs[1].Name != "codex" || !hs[1].Structured || hs[3].Name != "agy" || hs[4].Name != "gemini" || hs[6].Name != "shell" || !hs[6].Installed {
		t.Fatalf("%d %+v", s, hs)
	}
	for _, r := range []struct{ m, p string }{{"GET", "/api/discover"}, {"POST", "/api/adopt"}} {
		if s := agent.do(r.m, r.p, `{}`, nil); s != 403 {
			t.Errorf("agent %s %s: %d", r.m, r.p, s)
		}
	}
	var list struct{ Agents []any }
	if s := owner.do("GET", "/api/discover", nil, &list); s != 403 {
		t.Fatalf("discover: %d %+v", s, list)
	}
	// pid 1 is never an agent: refused before anything is signalled
	if s := owner.do("POST", "/api/adopt", map[string]any{"pid": 1, "confirm": true}, nil); s != 403 {
		t.Fatalf("adopt pid 1: %d", s)
	}
}
