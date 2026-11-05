package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testModePath is a runtime backstop, including for tests of config consumers.
// No environment switch can disable it in a Go test binary.
func testModePath(path string) error {
	if !testing.Testing() {
		return nil
	}
	root, err := filepath.Abs(os.TempDir())
	if err != nil {
		return err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	inside := func(base, name string) bool {
		rel, err := filepath.Rel(base, name)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	refused := func() error {
		return fmt.Errorf("TEST-MODE: refusing path outside os.TempDir(): %s", path)
	}
	if !inside(root, target) {
		return refused()
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("TEST-MODE: resolve temporary directory: %w", err)
	}
	// Resolve the deepest existing ancestor; a new leaf need not exist yet.
	ancestor := target
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(ancestor) == ancestor {
			return fmt.Errorf("TEST-MODE: inspect path: %w", err)
		}
		ancestor = filepath.Dir(ancestor)
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return fmt.Errorf("TEST-MODE: resolve path ancestry: %w", err)
	}
	if !inside(root, resolved) {
		return refused()
	}
	return nil
}

// CheckTestModePath applies the same backstop to another state folder (the release home of
// internal/release): in a Go test binary, a path outside os.TempDir() is refused.
func CheckTestModePath(path string) error { return testModePath(path) }
