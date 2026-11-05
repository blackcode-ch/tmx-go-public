package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectImportCommand(t *testing.T) {
	cliDaemon(t)
	if out, errs, code := tmx(t, "project", "new", "Imported"); code != 0 {
		t.Fatal(out, errs)
	}
	path := filepath.Join(t.TempDir(), "migration.json")
	data := `{"id":"batch","source":"python@hostb","section":{"status":"Ready","needs":[{"text":"Checked","done":true}],"updated_at":1000,"updated_by":"legacy"},"cards":[{"id":"old-card","question":"Choose?","created_at":900}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		out, errs, code := tmx(t, "project", "import", "imported", "--file", path)
		if code != 0 || !strings.Contains(out, "import-") || !strings.Contains(out, "legacy") {
			t.Fatal(code, out, errs)
		}
	}
}
