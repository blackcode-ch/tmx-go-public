package transfers

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFixedOSPrefix(t *testing.T) {
	for _, tc := range []struct{ path, prefix, target string }{
		{"/var", "/var", "/private/var"},
		{"/var/folders/user/temp/file", "/var", "/private/var"},
		{"/tmp/new/file", "/tmp", "/private/tmp"},
		{"/etc/config", "/etc", "/private/etc"},
		{"/var/link/file", "/var", "/private/var"},
		{"/variable/file", "", ""},
		{"/tmp-other/file", "", ""},
		{"/etcetera/file", "", ""},
		{"/home/user/var/file", "", ""},
		{"/private/var/folders/file", "", ""},
		{"var/folders/file", "", ""},
		{"/VAR/file", "", ""},
		{`C:\tmp\file`, "", ""},
	} {
		for _, goos := range []string{"darwin", "windows", "linux"} {
			t.Run(goos+tc.path, func(t *testing.T) {
				prefix, target := fixedOSPrefix(goos, tc.path)
				wantPrefix, wantTarget := tc.prefix, tc.target
				if goos != "darwin" {
					wantPrefix, wantTarget = "", ""
				}
				if prefix != wantPrefix || target != wantTarget {
					t.Fatalf("got (%q, %q), want (%q, %q)", prefix, target, wantPrefix, wantTarget)
				}
			})
		}
	}
}

func TestNoLinksBelowOSPrefix(t *testing.T) {
	root := t.TempDir()
	if err := noLinks(filepath.Join(root, "not-created", "file")); err != nil {
		t.Fatalf("ordinary temporary path refused: %v", err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	for _, p := range []string{link, filepath.Join(link, "new", "file")} {
		if err := noLinks(p); err == nil || !strings.Contains(err.Error(), "path traverses a link") {
			t.Fatalf("user-controlled symlink %q: %v", p, err)
		}
	}
}

func TestDarwinOSPrefixes(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("real Darwin OS layout")
	}
	for _, prefix := range []string{"/var", "/tmp", "/etc"} {
		if err := noLinks(prefix); err != nil {
			t.Errorf("fixed OS alias %s refused: %v", prefix, err)
		}
	}
}
