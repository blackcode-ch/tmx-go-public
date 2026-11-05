package claude

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestInstallPaths(t *testing.T) {
	home := filepath.Join("h", "u")
	got := installPaths("darwin", home, "")
	want := []string{filepath.Join(home, ".local", "bin", "claude"), filepath.Join(home, ".claude", "local", "claude")}
	if !slices.Equal(got, want) {
		t.Fatalf("darwin: %v", got)
	}
	// Windows: the .exe before the .cmd, and npm's global folder last.
	appdata := filepath.Join("h", "u", "AppData", "Roaming")
	got = installPaths("windows", home, appdata)
	want = []string{
		filepath.Join(home, ".local", "bin", "claude.exe"), filepath.Join(home, ".local", "bin", "claude.cmd"),
		filepath.Join(home, ".claude", "local", "claude.exe"), filepath.Join(home, ".claude", "local", "claude.cmd"),
		filepath.Join(appdata, "npm", "claude.exe"), filepath.Join(appdata, "npm", "claude.cmd"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("windows: %v", got)
	}
	if got := installPaths("windows", home, ""); len(got) != 4 {
		t.Fatalf("windows without APPDATA: %v", got)
	}
}
