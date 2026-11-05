//go:build !windows || tmx_ci

// On Windows only in CI (-tags tmx_ci), never on a dev PC: this file carries dangerous command
// strings (kill-by-name), the class Defender flagged on 3 Nov. Matched as data; nothing is run.

package guardhook

import (
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func setup(t *testing.T) (*apitest.Daemon, Options) {
	d := apitest.New(t)
	head, _ := d.Fleet.Agent("head")
	o := Options{Client: client.New(d.URL, d.AgentToken(t, "head")), Wait: 3 * time.Second, Poll: 200 * time.Millisecond,
		Context: guard.Context{OS: "linux", Home: "/home/x", Own: head.Cwd, Workspace: d.Workspace, StateDir: d.Home}}
	return d, o
}

func pending(t *testing.T, d *apitest.Daemon) model.Approval {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if list, _ := d.Fleet.Approvals(model.ApprovalPending); len(list) > 0 {
			return list[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no hold appeared")
	return model.Approval{}
}

func TestAllowIsSilentBlockIsDenied(t *testing.T) {
	d, o := setup(t)
	if dec := run(t, o, input("Bash", map[string]any{"command": "go test ./..."})); dec != nil {
		t.Fatalf("allowed call answered: %+v", dec)
	}
	if dec := run(t, o, input("Write", map[string]any{"file_path": o.Context.Own + "/x.go", "content": "x"})); dec != nil {
		t.Fatalf("allowed write answered: %+v", dec)
	}
	dec := run(t, o, input("Bash", map[string]any{"command": "pkill node"}))
	if dec == nil || dec.PermissionDecision != "deny" || !strings.Contains(dec.PermissionDecisionReason, "Kill tmx's own processes by name") { // node: the fixed carve-out
		t.Fatalf("block: %+v", dec)
	}
	tl, _ := d.Fleet.Timeline("head", 0, 100)
	if last := tl[len(tl)-1]; last.Kind != model.EntryError || last.Meta["guard"] == nil {
		t.Fatalf("no block row: %+v", last)
	}
}

func TestHoldAllowedDeniedTimedOut(t *testing.T) {
	d, o := setup(t)
	sudo := input("Bash", map[string]any{"command": "sudo true"})

	got := make(chan *Decision, 1)
	go func() { got <- run(t, o, sudo) }()
	ap := pending(t, d)
	if ap.Kind != model.ApprovalGuard {
		t.Fatalf("hold: %+v", ap)
	}
	if _, err := d.Fleet.Decide(fleet.OwnerCaller, ap.ID, fleet.DecideRequest{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	if dec := <-got; dec == nil || dec.PermissionDecision != "allow" {
		t.Fatalf("allowed hold: %+v", dec)
	}

	go func() { got <- run(t, o, sudo) }()
	ap = pending(t, d)
	d.Fleet.Decide(fleet.OwnerCaller, ap.ID, fleet.DecideRequest{Decision: "deny", Message: "use the brew formula"})
	if dec := <-got; dec == nil || dec.PermissionDecision != "deny" || !strings.Contains(dec.PermissionDecisionReason, "use the brew formula") {
		t.Fatalf("denied hold: %+v", dec)
	}

	o.Wait = 300 * time.Millisecond
	o.Poll = 100 * time.Millisecond
	dec := run(t, o, sudo)
	if dec == nil || dec.PermissionDecision != "deny" || !strings.Contains(dec.PermissionDecisionReason, "did not answer") {
		t.Fatalf("timed out hold: %+v", dec)
	}
	if list, _ := d.Fleet.Approvals(model.ApprovalPending); len(list) != 0 {
		t.Fatalf("a timed-out hold stayed pending: %+v", list)
	}
}

func TestFailsClosed(t *testing.T) {
	_, o := setup(t)
	for name, opts := range map[string]Options{
		"unreachable": {Client: client.New("http://127.0.0.1:1", "x"), Context: o.Context, Wait: time.Second},
		"no daemon":   {Context: o.Context, Wait: time.Second},
		"bad token":   {Client: client.New(o.Client.BaseURL, "not-a-token"), Context: o.Context, Wait: time.Second},
	} {
		dec := run(t, opts, input("Bash", map[string]any{"command": "sudo true"}))
		if dec == nil || dec.PermissionDecision != "deny" || !strings.Contains(dec.PermissionDecisionReason, "could not be reached") {
			t.Errorf("%s: hold not denied: %+v", name, dec)
		}
		if dec := run(t, opts, input("Bash", map[string]any{"command": "rm -rf /"})); dec == nil || dec.PermissionDecision != "deny" {
			t.Errorf("%s: block not denied: %+v", name, dec)
		}
		if dec := run(t, opts, input("Bash", map[string]any{"command": "ls"})); dec != nil {
			t.Errorf("%s: allow not silent: %+v", name, dec)
		}
	}
	if dec := run(t, o, "{not json"); dec == nil || dec.PermissionDecision != "deny" {
		t.Fatalf("unreadable input: %+v", dec)
	}
}
