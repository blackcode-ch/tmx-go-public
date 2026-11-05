package codexguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareAndResumePins(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"codex.exe", "tmx.exe"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name+" fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base := filepath.Join(root, "managed")
	guardCtx := Guard{Own: root, Workspace: root, State: filepath.Join(root, "state")}
	l, err := Prepare(base, root, filepath.Join(root, "codex.exe"), filepath.Join(root, "tmx.exe"), root, guardCtx)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	if l.Manifest == "" {
		t.Fatal("manifest path missing")
	}
	l.Close()
	l, err = Prepare(base, root, filepath.Join(root, "codex.exe"), filepath.Join(root, "tmx.exe"), root, guardCtx)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if err := os.WriteFile(filepath.Join(base, "home", "config.toml"), []byte("notify=['changed']"), 0600); err != nil {
		t.Fatal(err)
	}
	if bad, err := Prepare(base, root, filepath.Join(root, "codex.exe"), filepath.Join(root, "tmx.exe"), root, guardCtx); err == nil {
		bad.Close()
		t.Fatal("changed config admitted")
	}
}
