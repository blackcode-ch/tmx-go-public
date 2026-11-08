package companion

import (
	"path/filepath"
	"testing"
)

func TestCompanionHomeDefaultAndOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(EnvAIOSHome, "")
	if h, err := AIOSHome(); err != nil || h != filepath.Join(home, ".aios") {
		t.Fatalf("default home %q: %v", h, err)
	}
	profile := t.TempDir()
	t.Setenv(EnvAIOSHome, profile)
	if got, err := AIOSHome(); err != nil || got != profile {
		t.Fatalf("profile home %q: %v", got, err)
	}
	if Name != "tmxg" {
		t.Fatal("Companion entry collides")
	}
}
