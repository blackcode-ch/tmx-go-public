package cli

import (
	"strings"
	"testing"
)

func TestGateGraceCommands(t *testing.T) {
	d := cliDaemon(t) // the test daemon starts with the window off
	if out, _, code := tmx(t, "gate"); code != 0 || !strings.Contains(out, "asks every time") || !strings.Contains(out, "recovery:") {
		t.Fatalf("gate status: %d %s", code, out)
	}
	// Longer requires a fresh proof; a CLI/file-token caller gets no UI grace.
	out, errs, code := tmx(t, "gate", "grace", "1h")
	if code != 0 || !strings.Contains(errs, "tmx is trying to stop asking for 1 hour after each confirmation") ||
		!strings.Contains(out, "no prompt for 1 hour (locked now") {
		t.Fatalf("grace 1h: %d %s %s", code, out, errs)
	}
	prompts := len(d.GateAuth.Prompts())
	// Shorter needs no prompt and does not open a window.
	if out, _, code := tmx(t, "gate", "grace", "15"); code != 0 || !strings.Contains(out, "no prompt for 15 minutes (locked now") {
		t.Fatalf("grace 15: %d %s", code, out)
	}
	if out, _, code := tmx(t, "gate", "lock"); code != 0 || !strings.Contains(out, "locked now") {
		t.Fatalf("lock: %d %s", code, out)
	}
	if out, _, code := tmx(t, "gate", "grace", "off"); code != 0 || !strings.Contains(out, "asks every time") {
		t.Fatalf("grace off: %d %s", code, out)
	}
	if len(d.GateAuth.Prompts()) != prompts {
		t.Fatal("shorter, lock or off prompted")
	}
	for _, bad := range [][]string{{"gate", "grace"}, {"gate", "grace", "7m"}, {"gate", "grace", ""}, {"gate", "grace", "2d"}, {"gate", "lock", "now"}} {
		if _, errs, code := tmx(t, bad...); code != 2 || !strings.Contains(errs, "tmx gate grace") {
			t.Fatalf("%q: %d %s", bad, code, errs)
		}
	}
	for in, want := range map[string]int{"off": 0, "5m": 5, "5min": 5, "30": 30, "1h": 60, "2H": 120, "24h": 1440} {
		if got, err := parseGrace(in); err != nil || got != want {
			t.Errorf("parseGrace(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}
