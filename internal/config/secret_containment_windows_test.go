//go:build aclmutate

package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"golang.org/x/sys/windows"
)

func daclSnapshot(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

func TestSecretsExistingDACLUnchanged(t *testing.T) {
	dir := t.TempDir()
	setDACL(t, dir, "D:(A;OICI;FA;;;WD)")
	child := filepath.Join(dir, "legacy")
	if err := os.WriteFile(child, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	childBefore := daclSnapshot(t, child)
	before := daclSnapshot(t, dir)
	if err := MkdirSecret(dir); err != nil {
		t.Fatal(err)
	}
	if got := daclSnapshot(t, dir); got != before {
		t.Fatalf("existing DACL changed: %s -> %s", before, got)
	}
	if got := daclSnapshot(t, child); got != childBefore {
		t.Error("existing child DACL changed")
	}
}

func TestSecretsJunctionRefused(t *testing.T) {
	for _, placement := range []string{"leaf", "home", "ancestor"} {
		t.Run(placement, func(t *testing.T) {
			root, target := t.TempDir(), t.TempDir()
			setDACL(t, target, "D:(A;OICI;FA;;;WD)")
			before := daclSnapshot(t, target)
			link := filepath.Join(root, "junction")
			cmd := hiddenexec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("temp junction: %v: %s", err, out)
			}
			t.Cleanup(func() {
				if err := os.Remove(link); err != nil {
					t.Error(err)
				}
			})
			dir := link
			if placement == "home" {
				dir = filepath.Join(link, "secrets")
			}
			if placement == "ancestor" {
				dir = filepath.Join(link, "home", "secrets")
			}
			if err := MkdirSecret(dir); err == nil {
				t.Error("accepted junction")
			}
			if got := daclSnapshot(t, target); got != before {
				t.Errorf("junction target DACL changed: %s -> %s", before, got)
			}
			if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
				t.Errorf("junction target modified: %v, %v", entries, err)
			}
		})
	}
}

// Pure path-policy tests: never pass actual system/profile/drive roots to code
// capable of creating objects or changing a DACL, even to test rejection.
func TestSecretsForbiddenRoots(t *testing.T) {
	p := secretDirPolicy{profile: `X:\People\owner`, profiles: `X:\People`, system: []string{`X:\Windows`, `X:\Program Files`, `X:\ProgramData`}}
	for _, path := range []string{`X:\`, `X:\People`, `X:\People\owner`, `x:\people\another`, `X:\Windows`, `X:\Windows\System32`, `X:\Program Files\app`, `X:\ProgramData`} {
		if err := p.check(path); err == nil {
			t.Errorf("accepted forbidden path %q", path)
		}
	}
	for _, path := range []string{`X:\People\owner\AppData\tmx\secrets`, `X:\Windows-other\secrets`, `X:\tmx\secrets`} {
		if err := p.check(path); err != nil {
			t.Errorf("rejected safe path %q: %v", path, err)
		}
	}
}

func TestSecretsEnsureRejectsHomeJunctionBeforeStateWrites(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	link := filepath.Join(root, "home")
	cmd := hiddenexec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("temp junction: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}
	})
	before := daclSnapshot(t, target)
	if err := (Paths{Home: link}).Ensure(); err == nil {
		t.Error("Ensure accepted home junction")
	}
	if err := WriteSecretFile(filepath.Join(link, "token"), []byte("fixture")); err == nil {
		t.Error("write accepted home junction")
	}
	if f, err := CreateSecretFile(filepath.Join(link, "token")); err == nil {
		f.Close()
		t.Error("create accepted home junction")
	}
	if got := daclSnapshot(t, target); got != before {
		t.Error("target DACL changed")
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Errorf("target modified: %v, %v", entries, err)
	}
}

func TestSecretsRefusesFileAsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	before := daclSnapshot(t, path)
	if err := MkdirSecret(path); err == nil {
		t.Error("accepted regular file")
	}
	if got := daclSnapshot(t, path); got != before {
		t.Error("file DACL changed")
	}
}

func TestSecretsConcurrentCreation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home", "secrets")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := MkdirSecret(dir); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	assertPrivate(t, dir, true, true)
}

func TestSecretsDirectorySymlinkRefused(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	before := daclSnapshot(t, target)
	if err := MkdirSecret(link); err == nil {
		t.Error("accepted directory symlink")
	}
	if got := daclSnapshot(t, target); got != before {
		t.Error("symlink target DACL changed")
	}
}
