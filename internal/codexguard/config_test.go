package codexguard

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConfigLeasePinsSettingsWithoutImport(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "generation")
	l, err := NewConfig(home, root, "Managed instructions")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`sandbox_mode = "danger-full-access"`, `approval_policy = "on-request"`, `web_search = "disabled"`, `notify = []`, `trust_level = "untrusted"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(string(b), "windows.sandbox") {
		t.Fatal("Windows sandbox configured")
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"config.toml", "AGENTS.md"} {
			if err := os.WriteFile(filepath.Join(home, name), []byte("tampered"), 0600); err == nil {
				t.Fatalf("changed own %s", name)
			}
			if err := os.Remove(filepath.Join(home, name)); err == nil {
				t.Fatalf("deleted own %s", name)
			}
			if err := os.Rename(filepath.Join(home, name), filepath.Join(home, name+".moved")); err == nil {
				t.Fatalf("renamed own %s", name)
			}
		}
		if err := os.Rename(home, filepath.Join(root, "swapped")); err == nil {
			t.Fatal("pinned config directory could be renamed")
		}
	}
	if another, err := NewConfig(home, root, "changed"); err == nil {
		another.Close()
		t.Fatal("reused existing generation")
	}
}
