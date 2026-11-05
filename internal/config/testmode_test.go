package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTestModePath(t *testing.T) {
	// Both allowed and rejected fixtures live in the real temporary directory.
	base := t.TempDir()
	root := filepath.Join(base, "temp")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(key, root)
	}
	for _, path := range []string{root, filepath.Join(root, "new", "leaf")} {
		if err := testModePath(path); err != nil {
			t.Fatalf("temporary path refused: %v", err)
		}
	}
	for _, path := range []string{base, root + "-sibling", filepath.Join(root, "..", "outside")} {
		if err := testModePath(path); err == nil || !strings.Contains(err.Error(), "TEST-MODE") {
			t.Fatalf("outside path not refused: %q, %v", path, err)
		}
	}
	outside := filepath.Join(base, "outside")
	t.Setenv(EnvHome, outside)
	if h, err := DefaultHome(); err == nil || h != "" {
		t.Fatalf("DefaultHome returned unsafe path: %q, %v", h, err)
	}
	if _, err := Resolve(); err == nil {
		t.Fatal("Resolve accepted outside path")
	}
	if err := (Paths{Home: outside}).Ensure(); err == nil {
		t.Fatal("Ensure accepted outside path")
	}
	if _, err := os.Lstat(outside); !os.IsNotExist(err) {
		t.Fatalf("rejection created a directory: %v", err)
	}
}

func TestTestModePathSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "temp")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(base, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(key, root)
	}
	if err := testModePath(filepath.Join(link, "new", "leaf")); err == nil {
		t.Fatal("symlink escape accepted")
	}
}
