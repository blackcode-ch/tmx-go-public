package cli

import (
	"strings"
	"testing"
)

func TestProcessesCommands(t *testing.T) {
	cliDaemon(t)
	if out, err, code := tmx(t, "processes", "list", "head"); code != 0 || !strings.Contains(out, "[") {
		t.Fatalf("list: %d %s %s", code, out, err)
	}
	if out, err, code := tmx(t, "processes", "system"); code != 0 || !strings.Contains(out, "system_tool") {
		t.Fatalf("system: %d %s %s", code, out, err)
	}
	if _, _, code := tmx(t, "processes", "start", "head"); code != 2 {
		t.Fatal("missing command accepted")
	}
}
