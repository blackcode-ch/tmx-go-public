package transfers

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestNormalizeWindowsPathSynthetic(t *testing.T) {
	// A fake filesystem lets this cover runner paths on another drive even
	// when the developer machine has no D: drive or 8.3 creation is disabled.
	resolve := func(p string) (string, error) {
		if strings.EqualFold(p, `d:\a\RUNNER~1\Temp`) {
			return `D:\a\RUNNER~1\Temp`, nil
		}
		return "", os.ErrNotExist
	}
	expand := func(p string) string {
		return strings.ReplaceAll(p, "RUNNER~1", "Runner Account")
	}
	got := normalizeWindowsPath(`d:\a\runner~1\Temp\unused\..\Secret\file`, resolve, expand)
	if !equalPath(got, `D:\a\Runner Account\Temp\SECRET\file`) {
		t.Fatalf("normalized path: %q", got)
	}
	for _, tc := range []struct {
		path, root string
		want       bool
	}{
		{`d:\a\Secrets\file`, `D:\A\SECRETS`, true},
		{`d:\a\Secrets-old\file`, `D:\a\Secrets`, false},
		{`C:\a\Secrets\file`, `D:\a\Secrets`, false},
		{`d:\a\file`, `D:\`, true},
	} {
		if got := within(tc.path, tc.root); got != tc.want {
			t.Errorf("within(%q, %q) = %v", tc.path, tc.root, got)
		}
	}
}

func TestWindowsShortPathProtectionAndExport(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Long secret folder name")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	name, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 32768)
	n, err := syscall.GetShortPathName(name, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	short := syscall.UTF16ToString(buf[:n])
	if strings.EqualFold(short, root) {
		t.Skip("8.3 names disabled on this volume")
	}
	if !samePath(strings.ToUpper(short), root) || !within(short, root) || !within(root, short) {
		t.Fatal("short and long folder names disagree")
	}
	s := &Service{}
	s.SecretFolders = []string{root}
	if err := s.sourceAllowed(short); err == nil {
		t.Fatal("short secret folder accepted")
	}
	s.SecretFolders = []string{short}
	if err := s.sourceAllowed(root); err == nil {
		t.Fatal("long secret folder accepted")
	}
	file := filepath.Join(short, "payload.txt")
	if err := os.WriteFile(file, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	x := NewExports(filepath.Join(t.TempDir(), "exports"))
	e, err := x.Prepare(file, "peer", "/dest", "id", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Folder packs historically stored the original short spelling.
	e.Path = file
	if err := x.save(e); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Get(e.Ticket, "peer"); err != nil {
		t.Fatal(err)
	}
}
