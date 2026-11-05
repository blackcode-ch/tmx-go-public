package cli

import (
	"runtime"
	"strings"
	"testing"
)

func TestResourceCommands(t *testing.T) {
	d := cliDaemon(t)
	if out, errs, code := tmx(t, "resources"); code != 0 || !strings.Contains(out, "machine testbox") || !strings.Contains(out, "head") {
		t.Fatalf("resources: %d %s %s", code, out, errs)
	}
	// The owner's slot runs the command and passes its exit code on.
	command := []string{"sh", "-c", "exit 3"}
	if runtime.GOOS == "windows" {
		command = []string{"cmd.exe", "/d", "/c", "exit /b 3"}
	}
	if _, errs, code := tmx(t, append([]string{"slot", "--"}, command...)...); code != 3 {
		t.Fatalf("slot exit code %d %s", code, errs)
	}
	if _, errs, code := tmx(t, "slot", "--kind", "gpu", "--", "true"); code != 1 || !strings.Contains(errs, "unknown slot kind") {
		t.Fatalf("unknown kind: %d %s", code, errs)
	}
	if out, _, code := tmx(t, "slots"); code != 0 || !strings.Contains(out, "heavy    0 of 2") {
		t.Fatalf("slots after release: %s", out)
	}
	if _, errs, code := tmx(t, "resume", "head"); code != 1 || !strings.Contains(errs, "not paused") {
		t.Fatalf("resume: %d %s", code, errs)
	}
	if out, _, code := tmx(t, "exempt", "head", "--hours", "2"); code != 0 || !strings.Contains(out, "no brakes for 2 hours") {
		t.Fatalf("exempt: %s", out)
	}
	if d.Resources.Config().Exempt["head"] == 0 {
		t.Fatal("exemption not stored")
	}
	if out, _, code := tmx(t, "start", "head", "--force"); code != 0 || !strings.Contains(out, "head:") {
		t.Fatalf("start: %s", out)
	}
}

func TestOrdinal(t *testing.T) {
	for n, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 21: "21st", 0: "next"} {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q", n, got)
		}
	}
}
