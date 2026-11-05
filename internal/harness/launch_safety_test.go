package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUniversalLaunchCwd(t *testing.T) {
	home, system, safe := t.TempDir(), t.TempDir(), t.TempDir()
	withCodexHomeDirs(t, home)
	withCodexExtraRoots(t, system)
	for _, tc := range []struct {
		name, cwd      string
		elevated, want bool
	}{
		{"safe", safe, false, true},
		{"new-project", filepath.Join(safe, "new", "child"), false, true},
		{"elevated", safe, true, false},
		{"home", home, false, false},
		{"home-child", filepath.Join(home, "project"), false, true},
		{"system", system, false, false},
		{"system-child", filepath.Join(system, "new-child"), false, false},
		{"relative", "..", false, false},
		{"drive-root", `C:\`, false, false},
		{"other-profile", `C:\Users\someone`, false, false},
		{"linux-profile", "/home/someone", false, false},
		{"system32", `C:\System32`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateLaunchCwd(tc.cwd, tc.elevated); (err == nil) != tc.want {
				t.Fatalf("admission=%v; want accepted=%v", err, tc.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(safe, "new")); !os.IsNotExist(err) {
		t.Fatal("admission mutated filesystem")
	}
}

func TestUniversalLaunchJunctionBeforeCreation(t *testing.T) {
	system := t.TempDir()
	withCodexExtraRoots(t, system)
	link := filepath.Join(t.TempDir(), "alias")
	mklinkJunction(t, link, system)
	if err := validateLaunchCwd(filepath.Join(link, "not-created"), false); err == nil {
		t.Fatal("new descendant of junction to system admitted")
	}
}
