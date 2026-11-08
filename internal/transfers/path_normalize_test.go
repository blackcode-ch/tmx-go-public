package transfers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDarwinNormalizationOnEveryOS(t *testing.T) {
	// Resolve only an existing prefix: the remaining components do not exist yet.
	resolve := func(p string) (string, error) {
		if p == "/var/folders" {
			return "/private/var/folders", nil
		}
		return "", os.ErrNotExist
	}
	normalize := func(p string) string {
		return normalizePathForOS(p, "darwin", resolve, func(p string) string { return p })
	}
	for _, pair := range [][2]string{
		{"/var/folders/x", "/private/var/folders/x"},
		{"/var/folders/unused/../secret/file", "/private/var/folders/secret/file"},
		{"/tmp/new/file", "/private/tmp/new/file"},
		{"/etc/config", "/private/etc/config"},
	} {
		if a, b := normalize(pair[0]), normalize(pair[1]); a != b {
			t.Errorf("alias pair differs: %q != %q", a, b)
		}
	}
	// This is the containment predicate used by sourceAllowed for secret folders
	// and by both source and destination protected-root checks.
	for _, tc := range []struct {
		source, secret string
		refused        bool
	}{
		{"/private/var/folders/secret/file", "/var/folders/secret", true},
		{"/var/folders/secret/file", "/private/var/folders/secret", true},
		{"/private/var/folders/secret", "/var/folders/secret", true},
		{"/private/var/folders/secret-old/file", "/var/folders/secret", false},
		{"/variable/file", "/var", false},
	} {
		if got := withinNormalized(tc.source, tc.secret, "/", normalize); got != tc.refused {
			t.Errorf("secret refusal (%q, %q) = %v", tc.source, tc.secret, got)
		}
	}
}

func TestNormalizeExistingPrefix(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	suffix := filepath.Join("not-created", "file")
	if got, want := normalizePath(filepath.Join(root, suffix)), filepath.Join(normalizePath(real), suffix); got != want {
		t.Fatalf("existing prefix: got %q want %q", got, want)
	}
}
