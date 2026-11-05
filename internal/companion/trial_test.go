package companion

import "testing"

func TestTrialCompanionRequiresIsolatedHome(t *testing.T) {
	t.Setenv(EnvAIOSHome, "")
	if h, err := AIOSHome(); err == nil {
		t.Fatalf("implicitly uses shared home %q", h)
	}
	t.Setenv(EnvAIOSHome, t.TempDir())
	if _, err := AIOSHome(); err != nil {
		t.Fatal(err)
	}
	if Name != "tmxg" {
		t.Fatal("Companion entry collides")
	}
}
