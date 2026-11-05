package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestGateAndGuardCommands(t *testing.T) {
	d := cliDaemon(t)
	if out, _, code := tmx(t, "gate"); code != 0 || !strings.Contains(out, "owner gate: off") {
		t.Fatalf("gate status: %d %s", code, out)
	}
	out, errs, code := tmx(t, "gate", "on")
	if code != 0 || !strings.Contains(out, "owner gate: on") || !strings.Contains(errs, "Confirm on this machine with a test prompt: tmx is trying to turn on the owner gate") {
		t.Fatalf("gate on: %d %s %s", code, out, errs)
	}

	// A guard hold, approved from the CLI through the gate.
	agent := client.New(d.URL, d.AgentToken(t, "head"))
	var ap model.Approval
	if err := agent.Post(context.Background(), "/api/guard/holds",
		map[string]string{"tool": "Bash", "command": "sudo true", "rule": "elevation", "match": "sudo true"}, &ap); err != nil {
		t.Fatal(err)
	}
	if out, _, _ := tmx(t, "approvals"); !strings.Contains(out, "held by the guard for head (Administrator rights): sudo true") {
		t.Fatalf("approvals: %s", out)
	}
	out, errs, code = tmx(t, "approve", ap.ID)
	if code != 0 || !strings.Contains(out, "allowed") || !strings.Contains(errs, "allow “sudo true” for head") {
		t.Fatalf("approve: %d %s %s", code, out, errs)
	}

	if out, _, code := tmx(t, "guard"); code != 0 || !strings.Contains(out, "guard: on") {
		t.Fatalf("guard: %d %s", code, out)
	}
	out, errs, code = tmx(t, "guard", "off", "--for", "worker")
	if code != 0 || !strings.Contains(out, "off for worker") || !strings.Contains(errs, "Confirm on this machine with a test prompt: tmx is trying to relax agent safety settings") {
		t.Fatalf("guard off --for: %d %s %s", code, out, errs)
	}
	if out, _, code := tmx(t, "guard", "on", "--for", "worker"); code != 0 || strings.Contains(out, "off for") {
		t.Fatalf("guard on --for: %d %s", code, out)
	}
	d.GateAuth.SetAnswer(func(context.Context, string) error { return context.Canceled })
	if _, errs, code := tmx(t, "gate", "off"); code != 1 || !strings.Contains(errs, "not confirmed") {
		t.Fatalf("gate off refused: %d %s", code, errs)
	}
	d.GateAuth.SetAnswer(nil)
	if out, _, code := tmx(t, "gate", "off"); code != 0 || !strings.Contains(out, "owner gate: off") {
		t.Fatalf("gate off: %d %s", code, out)
	}
	if _, errs, code := tmx(t, "hook", "nope"); code != 2 || !strings.Contains(errs, "pretooluse") {
		t.Fatalf("hook usage: %d %s", code, errs)
	}
}
