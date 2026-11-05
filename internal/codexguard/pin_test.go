package codexguard

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPinIdentityAndContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("fixed config"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := OpenPin(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Verify(); err != nil {
		t.Fatal(err)
	}
	if p.SHA256 == "" || !filepath.IsAbs(p.Path) {
		t.Fatal("missing identity")
	}
	err = os.WriteFile(path, []byte("changed"), 0600)
	if runtime.GOOS == "windows" {
		if err == nil {
			t.Fatal("Windows pin allowed config write")
		}
		if err := os.Remove(path); err == nil {
			t.Fatal("Windows pin allowed replacement")
		}
		if err := p.Verify(); err != nil {
			t.Fatal(err)
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Verify(); err == nil {
			t.Fatal("changed file was accepted")
		}
	}
}

func TestPinRejectsDirectory(t *testing.T) {
	if p, err := OpenPin(t.TempDir()); err == nil {
		p.Close()
		t.Fatal("directory accepted")
	}
}
