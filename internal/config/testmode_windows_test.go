//go:build windows && aclmutate

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTestModeDACLWritesRefused(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "temp")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	outside := filepath.Join(base, "outside")
	if err := mkdirSecret(outside); err == nil || !strings.Contains(err.Error(), "TEST-MODE") {
		t.Fatalf("directory DACL creation not refused: %v", err)
	}
	f, err := createSecret(outside)
	if f != nil {
		f.Close()
		t.Fatal("file DACL creation returned a file")
	}
	if err == nil || !strings.Contains(err.Error(), "TEST-MODE") {
		t.Fatalf("file DACL creation not refused: %v", err)
	}
	if _, err := os.Lstat(outside); !os.IsNotExist(err) {
		t.Fatalf("rejection created an object: %v", err)
	}
}
