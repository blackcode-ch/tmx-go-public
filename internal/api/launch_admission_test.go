package api_test

import (
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestAPILaunchAdmissionAcrossHarnesses(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	home, system := t.TempDir(), t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("SystemRoot", system)
	for _, h := range []model.Harness{model.HarnessClaude, model.HarnessCodex, model.HarnessOpenCode, model.HarnessPi} {
		for _, cwd := range []string{home, system, filepath.Join(system, "new-child")} {
			if status := owner.do("POST", "/api/agents", map[string]any{"name": "unsafe", "harness": h, "cwd": cwd}, nil); status != 403 {
				t.Fatalf("%s %s: status=%d", h, cwd, status)
			}
		}
	}
	if _, err := d.Fleet.Agent("unsafe"); err == nil {
		t.Fatal("rejected agent persisted")
	}
}
