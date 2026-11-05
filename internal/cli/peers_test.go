package cli

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

func TestPeersImportPythonCommand(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if _, _, code := tmx(t, "peers"); code != 2 {
		t.Fatalf("no subcommand: exit %d", code)
	}
	if _, _, code := tmx(t, "peers", "export"); code != 2 {
		t.Fatalf("unknown subcommand: exit %d", code)
	}
	// The test daemon is a trial: it never imports secrets.
	if _, _, code := tmx(t, "peers", "import-python", "--from", t.TempDir()); code != 2 {
		t.Fatalf("--from accepted: exit %d", code)
	}
	_, errs, code := tmx(t, "peers", "import-python")
	if code == 0 || !strings.Contains(errs, "production") {
		t.Fatalf("trial import: exit %d %q", code, errs)
	}
}
