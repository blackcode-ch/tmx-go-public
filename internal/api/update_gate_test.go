package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

// secretToken stands for a GitHub token: it must never reach a prompt, a confirmation or the log.
const secretToken = "github_pat_never-in-a-prompt-0123456789"

// configAndUpdateDaemon is a test daemon whose API also has an updater (a dev build: it never
// checks GitHub or installs).
func configAndUpdateDaemon(t *testing.T) (d *apitest.Daemon, owner, agent *tc.Client, u *update.Updater) {
	t.Helper()
	d = apitest.New(t)
	url, u := d.WithUpdater(t)
	return d, tc.New(url, d.OwnerToken), tc.New(url, d.AgentToken(t, "head")), u
}

func headHarness(t *testing.T, d *apitest.Daemon) string {
	t.Helper()
	c, err := config.Load(config.Paths{Home: d.Home})
	if err != nil {
		t.Fatal(err)
	}
	return c.HeadHarness
}

func hasToken(t *testing.T, d *apitest.Daemon) bool {
	t.Helper()
	_, err := os.Stat(config.Paths{Home: d.Home}.GitHubTokenFile())
	return err == nil
}

// gateOn turns the owner gate on with a proof (its grace window stays off: every proof is for
// one request).
func gateOn(t *testing.T, owner *tc.Client) {
	t.Helper()
	on := map[string]bool{"enabled": true}
	s, conf := call(t, owner, "PUT", "/api/gate", on, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("gate on without proof: %d", s)
	}
	var st gate.Status
	if s, _ := call(t, confirmed(t, owner, conf), "PUT", "/api/gate", on, &st); s != 0 || !st.Enabled {
		t.Fatalf("gate on: %d %+v", s, st)
	}
}

// With the gate off (the default) the head harness, the update settings, the token and an
// install by hand go through as before: no 428, no prompt.
func TestConfigAndUpdatesGateOff(t *testing.T) {
	d, owner, _, _ := configAndUpdateDaemon(t)

	var v api.ConfigView
	if s, _ := call(t, owner, "PUT", "/api/config", map[string]string{"head_harness": "codex"}, &v); s != 0 || v.HeadHarness != "codex" || headHarness(t, d) != "codex" {
		t.Fatalf("head harness: %d %+v", s, v)
	}
	if s, _ := call(t, owner, "PUT", "/api/config", map[string]string{"head_harness": "codex"}, &v); s != 0 || v.HeadHarness != "codex" {
		t.Fatalf("same head harness again: %d %+v", s, v)
	}
	if s, _ := call(t, owner, "PUT", "/api/config", map[string]string{}, &v); s != 0 || v.HeadHarness != "codex" {
		t.Fatalf("empty config: %d %+v", s, v)
	}
	if s, _ := call(t, owner, "PUT", "/api/config", map[string]string{"head_harness": "shell"}, nil); s != http.StatusBadRequest {
		t.Fatalf("a harness without chat: %d", s)
	}

	var st update.Status
	if s, _ := call(t, owner, "PUT", "/api/update", map[string]any{"auto": false, "channel": "latest"}, &st); s != 0 || st.Auto || st.Channel != "latest" {
		t.Fatalf("update settings: %d %+v", s, st)
	}
	if got := d.UpdateSettings(); got.Auto == nil || *got.Auto || got.Channel != "latest" {
		t.Fatalf("saved update settings: %+v", got)
	}
	if s, _ := call(t, owner, "PUT", "/api/update", map[string]any{}, &st); s != 0 || st.Auto || st.Channel != "latest" {
		t.Fatalf("empty update settings: %d %+v", s, st)
	}
	if s, _ := call(t, owner, "PUT", "/api/update", map[string]any{"channel": "nightly"}, nil); s != http.StatusBadRequest {
		t.Fatalf("unknown channel: %d", s)
	}
	if s, _ := call(t, owner, "POST", "/api/update/install", map[string]bool{"now": true}, nil); s != 0 {
		t.Fatalf("install: %d", s)
	}
	if err := owner.Do(context.Background(), "POST", "/api/update/install", nil, nil); err != nil { // no body: now = false
		t.Fatalf("install without a body: %v", err)
	}
	if s, _ := call(t, owner, "PUT", "/api/update/token", map[string]string{"token": secretToken}, &st); s != 0 || !st.HasToken || !hasToken(t, d) {
		t.Fatalf("token: %d %+v", s, st)
	}
	st = update.Status{}
	if s, _ := call(t, owner, "PUT", "/api/update/token", map[string]string{"token": ""}, &st); s != 0 || st.HasToken || hasToken(t, d) {
		t.Fatalf("token removed: %d %+v", s, st)
	}
	if s, _ := call(t, owner, "POST", "/api/update/check", nil, nil); s != 0 {
		t.Fatalf("check: %d", s)
	}
	if got := d.GateAuth.Prompts(); len(got) != 0 {
		t.Fatalf("the gate is off but asked: %q", got)
	}
}

// With the gate on each of them answers 428 and does nothing; the confirmed proof passes once,
// for exactly that request. The token is bound into the proof but never shown.
func TestConfigAndUpdatesGateOn(t *testing.T) {
	cases := []struct {
		name, method, path string
		body, other        any
		reason             string
		done               func(t *testing.T, d *apitest.Daemon) bool // whether the change happened
	}{
		{"head harness", "PUT", "/api/config", map[string]string{"head_harness": "codex"}, map[string]string{"head_harness": "claude"},
			"switch the head agent's harness to Codex", func(t *testing.T, d *apitest.Daemon) bool { return headHarness(t, d) == "codex" }},
		{"auto off", "PUT", "/api/update", map[string]any{"auto": false}, map[string]any{"auto": false, "channel": "latest"},
			"turn automatic updates off", func(t *testing.T, d *apitest.Daemon) bool { a := d.UpdateSettings().Auto; return a != nil && !*a }},
		{"channel", "PUT", "/api/update", map[string]any{"channel": "latest"}, map[string]any{"channel": "stable"},
			"take updates from fleet releases including prereleases",
			func(t *testing.T, d *apitest.Daemon) bool { return d.UpdateSettings().Channel == "latest" }},
		{"install now", "POST", "/api/update/install", map[string]bool{"now": true}, map[string]bool{"now": false},
			"install the newest release now and restart, cutting off agents' steps in progress", nil},
		{"install later", "POST", "/api/update/install", map[string]bool{"now": false}, map[string]bool{"now": true},
			"install the newest release once no agent is busy", nil},
		{"token", "PUT", "/api/update/token", map[string]string{"token": secretToken}, map[string]string{"token": secretToken + "x"},
			"save a GitHub token for updates", func(t *testing.T, d *apitest.Daemon) bool { return hasToken(t, d) }},
		{"token removed", "PUT", "/api/update/token", map[string]string{"token": ""}, map[string]string{"token": secretToken},
			"remove the GitHub token for updates", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, owner, _, _ := configAndUpdateDaemon(t)
			gateOn(t, owner)

			s, conf := call(t, owner, c.method, c.path, c.body, nil)
			if s != http.StatusPreconditionRequired || conf.Reason != c.reason {
				t.Fatalf("without proof: %d %q, want 428 %q", s, conf.Reason, c.reason)
			}
			if c.done != nil && c.done(t, d) {
				t.Fatal("the change happened before the owner confirmed")
			}
			proof := confirmed(t, owner, conf)
			// The proof is for this body only (here the other body asks anew).
			s, other := call(t, proof, c.method, c.path, c.other, nil)
			if s != http.StatusPreconditionRequired {
				t.Fatalf("a proof for one body let another through: %d", s)
			}
			confirmed(t, owner, other) // let that prompt end
			if c.done != nil && c.done(t, d) {
				t.Fatal("the change happened with a proof for another request")
			}
			if s, _ := call(t, proof, c.method, c.path, c.body, nil); s != 0 {
				t.Fatalf("with proof: %d", s)
			}
			if c.done != nil && !c.done(t, d) {
				t.Fatal("confirmed, but the change did not happen")
			}
			if c.path == "/api/config" {
				// The same head harness again changes nothing: it neither asks nor needs a proof.
				n := len(d.GateAuth.Prompts())
				if s, _ := call(t, owner, c.method, c.path, c.body, nil); s != 0 || len(d.GateAuth.Prompts()) != n {
					t.Fatalf("an unchanged head harness: %d, %d prompts", s, len(d.GateAuth.Prompts())-n)
				}
			} else if s, again := call(t, proof, c.method, c.path, c.body, nil); s != http.StatusPreconditionRequired {
				t.Fatalf("a proof was used twice: %d", s)
			} else {
				confirmed(t, owner, again)
			}

			for _, p := range d.GateAuth.Prompts() {
				if strings.Contains(p, secretToken) {
					t.Fatalf("a prompt shows the token: %q", p)
				}
			}
			for _, cf := range []gate.Confirmation{conf, other} {
				b, _ := json.Marshal(cf)
				if strings.Contains(string(b), secretToken) {
					t.Fatalf("a confirmation carries the token: %s", b)
				}
			}
		})
	}
}

// A request that changes nothing, or that is refused anyway, never asks.
func TestConfigAndUpdatesNoPromptForNothing(t *testing.T) {
	d, owner, _, _ := configAndUpdateDaemon(t)
	if s, _ := call(t, owner, "PUT", "/api/config", map[string]string{"head_harness": "codex"}, nil); s != 0 { // the gate is off yet
		t.Fatalf("head harness: %d", s)
	}
	gateOn(t, owner)
	prompts := len(d.GateAuth.Prompts())
	for _, r := range []struct {
		method, path string
		body         any
		want         int
	}{
		{"PUT", "/api/config", map[string]string{}, 0},
		{"PUT", "/api/config", map[string]string{"head_harness": "codex"}, 0}, // what it is already
		{"PUT", "/api/config", map[string]string{"head_harness": "shell"}, http.StatusBadRequest},
		{"PUT", "/api/update", map[string]any{}, 0},
		{"PUT", "/api/update", map[string]any{"channel": "nightly"}, http.StatusBadRequest},
		{"POST", "/api/update/check", nil, 0},
		{"GET", "/api/update", nil, 0},
		{"GET", "/api/config", nil, 0},
	} {
		if s, _ := call(t, owner, r.method, r.path, r.body, nil); s != r.want {
			t.Errorf("%s %s %v: %d, want %d", r.method, r.path, r.body, s, r.want)
		}
	}
	if got := d.GateAuth.Prompts(); len(got) != prompts {
		t.Fatalf("asked for nothing: %q", got[prompts:])
	}
}

// Inside the grace window the owner's requests pass without a prompt; an agent's token (and
// Companion's) never reaches these routes, window or not.
func TestConfigAndUpdatesGraceWindow(t *testing.T) {
	d, owner, agent, _ := configAndUpdateDaemon(t)
	owner.Header = http.Header{api.UISessionHeader: {strings.Repeat("ab", 32)}}
	hour := map[string]int{"grace_minutes": 60}
	s, conf := call(t, owner, "PUT", "/api/gate/grace", hour, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("grace without proof: %d", s)
	}
	if s, _ := call(t, confirmed(t, owner, conf), "PUT", "/api/gate/grace", hour, nil); s != 0 {
		t.Fatalf("grace: %d", s)
	}
	gateOn(t, owner)
	var gs gate.Status
	if call(t, owner, "GET", "/api/gate", nil, &gs); !gs.Enabled || gs.GraceUntil == 0 {
		t.Fatalf("no open window: %+v", gs)
	}
	prompts := len(d.GateAuth.Prompts())

	tok, err := config.CompanionToken(config.Paths{Home: d.Home}, true)
	if err != nil {
		t.Fatal(err)
	}
	link := *agent
	link.Token = tok
	routes := []struct {
		method, path string
		body         any
	}{
		{"PUT", "/api/config", map[string]string{"head_harness": "codex"}},
		{"PUT", "/api/update", map[string]any{"auto": false}},
		{"POST", "/api/update/install", map[string]bool{"now": false}},
		{"PUT", "/api/update/token", map[string]string{"token": secretToken}},
	}
	for _, who := range []struct {
		name string
		c    *tc.Client
	}{{"agent", agent}, {"Companion", &link}} {
		for _, r := range routes {
			if s, _ := call(t, who.c, r.method, r.path, r.body, nil); s != http.StatusForbidden {
				t.Errorf("%s %s %s inside the window: %d, want 403", who.name, r.method, r.path, s)
			}
		}
	}
	if headHarness(t, d) == "codex" || d.UpdateSettings().Auto != nil || hasToken(t, d) {
		t.Fatal("a refused request changed something")
	}

	for _, r := range routes {
		if s, _ := call(t, owner, r.method, r.path, r.body, nil); s != 0 {
			t.Errorf("owner %s %s inside the window: %d", r.method, r.path, s)
		}
	}
	if a := d.UpdateSettings().Auto; headHarness(t, d) != "codex" || a == nil || *a || !hasToken(t, d) {
		t.Fatal("the owner's changes inside the window did not happen")
	}
	if got := d.GateAuth.Prompts(); len(got) != prompts {
		t.Fatalf("the window prompted: %q", got[prompts:])
	}
}
