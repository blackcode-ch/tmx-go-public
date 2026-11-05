package api_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recworker"
)

// waitWorker waits for the recorder to launch a recording's report worker and returns its agent.
func waitWorker(t *testing.T, d *apitest.Daemon, w *recworker.Service, id string) model.Agent {
	t.Helper()
	for range 400 {
		if ag := w.Tracking(id); ag != "" {
			a, err := d.Fleet.Agent(ag)
			if err != nil {
				t.Fatal(err)
			}
			return a
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Stop launched no report worker")
	return model.Agent{}
}

// The whole path on a real daemon (fleet, launch policy, profiles, routes; the harness is the fake runner): Stop
// launches the worker with its locked-down profile, only that worker may process the recording and it is told
// states only, and when a worker ends before it asked the daemon makes the report itself and removes the worker.
func TestStopLaunchesTheReportWorker(t *testing.T) {
	d := apitest.New(t)
	on := true
	d.SetSummaries(config.SummaryConfig{Auto: &on})
	url, w := d.WithRecWorker(t, recworker.Options{Every: 20 * time.Millisecond})
	owner := client{t: t, base: url, token: d.OwnerToken}

	rec := stoppedRecording(t, d, owner)
	a := waitWorker(t, d, w, rec.ID)
	if a.Model != recworker.Model || a.Level != model.LevelWorker || a.Profile != recworker.ProfileSlug || !strings.HasPrefix(a.Name, "rec-") {
		t.Fatalf("worker: %+v", a)
	}
	p, err := d.Fleet.GetProfile(recworker.ProfileSlug)
	if err != nil || p.PermissionMode != "dontAsk" || p.ReadOnly || !slices.Equal(p.AllowedTools, []string{"Bash(tmx rec process:*)"}) ||
		!slices.Contains(p.DisallowedTools, "Read") {
		t.Fatalf("profile as the fleet stored it: %+v %v", p, err)
	}
	// what the harness is started with: claude may run the one command (no plan mode, Bash not denied)
	spec := d.Proc(t, a.Name).Spec
	if spec.PermissionMode != "dontAsk" || !slices.Contains(spec.AllowedTools, "Bash(tmx rec process:*)") || slices.Contains(spec.DisallowedTools, "Bash") {
		t.Fatalf("the worker could not run its command: mode %q, allowed %v, denied %v", spec.PermissionMode, spec.AllowedTools, spec.DisallowedTools)
	}
	if sent := strings.Join(d.Proc(t, a.Name).Sent(), "\n"); !strings.Contains(sent, "tmx rec process "+rec.ID) {
		t.Fatalf("the worker was told: %q", sent)
	}

	// only the worker: not the owner, not another agent
	path := "/api/recordings/" + rec.ID + "/process"
	for name, tok := range map[string]string{"owner": d.OwnerToken, "head": d.AgentToken(t, "head")} {
		if s := (client{t: t, base: url, token: tok}).do("POST", path, nil, nil); s != 403 {
			t.Fatalf("%s may not process: %d", name, s)
		}
	}
	worker := client{t: t, base: url, token: d.AgentToken(t, a.Name)}
	var x api.ProcessResponse
	if s := worker.do("POST", path, nil, &x); s != 200 || x.State == model.SummaryNone {
		t.Fatalf("worker process: %d %+v", s, x)
	}

	// a second recording whose worker dies before it asks: the daemon makes the report itself, the worker is removed
	rec2 := stoppedRecording(t, d, owner)
	a2 := waitWorker(t, d, w, rec2.ID)
	d.Proc(t, a2.Name).Exit(1, errors.New("claude: not logged in"))
	got := waitSummary(t, owner, rec2.ID, func(x model.RecordingSummary) bool { return x.State != model.SummaryNone && !x.State.Pending() })
	if !strings.HasPrefix(got.Note, "Made by the daemon: the report worker "+a2.Name+" didn't make it (") {
		t.Fatalf("the report must say which path made it: %q", got.Note)
	}
	for range 400 {
		if _, err := d.Fleet.Agent(a2.ID); err != nil {
			return // removed
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the dead worker was not removed")
}
