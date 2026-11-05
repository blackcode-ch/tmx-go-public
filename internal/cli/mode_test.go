package cli

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

func TestModeCommand(t *testing.T) {
	d := cliDaemon(t)
	p, err := d.Fleet.CreateProject(fleet.OwnerCaller, fleet.ProjectRequest{Name: "Modes"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Fleet.LaunchAgent(fleet.OwnerCaller, fleet.LaunchRequest{Name: "w1", ProjectID: p.ID}); err != nil {
		t.Fatal(err)
	}
	if _, _, code := tmx(t, "stop", "w1"); code != 0 {
		t.Fatal("stop")
	}
	if out, _, code := tmx(t, "mode", "w1"); code != 0 || !strings.HasPrefix(out, "w1: chat") {
		t.Fatalf("show: %d %q", code, out)
	}
	if out, _, code := tmx(t, "mode", "w1", "screen"); code != 0 || !strings.Contains(out, "Claude Code's own screen") {
		t.Fatalf("switch a stopped agent: %d %q", code, out)
	}
	if out, _, code := tmx(t, "mode", "w1"); code != 0 || !strings.Contains(out, "terminal (its own screen), switched from chat") {
		t.Fatalf("show again: %d %q", code, out)
	}
	if out, _, code := tmx(t, "mode", "w1", "chat", "--start"); code != 0 || !strings.Contains(out, "starts in chat") || !strings.Contains(out, "w1: starting") {
		t.Fatalf("back and start: %d %q", code, out)
	}
	if _, errOut, code := tmx(t, "mode", "head", "nope"); code == 0 || !strings.Contains(errOut, "chat or terminal") {
		t.Fatalf("bad mode: %d %q", code, errOut)
	}
}
