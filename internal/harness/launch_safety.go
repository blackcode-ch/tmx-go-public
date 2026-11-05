package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateLaunchCwd admits a working directory for every harness, before any
// filesystem or process mutation. Missing descendants are checked through their
// nearest existing ancestor; callers recheck after creating the directory.
func ValidateLaunchCwd(cwd string) error {
	// Process privilege is checked by fleet admission and again by real runners.
	return validateLaunchCwd(cwd, false)
}

func validateLaunchCwd(cwd string, elevated bool) error {
	if elevated {
		return fmt.Errorf("refusing to launch from an elevated process")
	}
	if !filepath.IsAbs(cwd) || unsafeLaunchPath(cwd) {
		return fmt.Errorf("launch requires an absolute working folder, not a root, home, or system folder")
	}
	existing := filepath.Clean(cwd)
	var tail []string
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			// Resolve below: Windows junctions may have ModeIrregular rather
			// than ModeSymlink. A dangling link is never treated as missing.
			break
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect working folder: %w", err)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return fmt.Errorf("working folder has no existing ancestor")
		}
		tail = append(tail, filepath.Base(existing))
		existing = parent
	}
	final, err := finalCodexPath(existing)
	if err != nil {
		return fmt.Errorf("resolve working folder: %w", err)
	}
	info, err := os.Stat(final)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("working folder ancestor must be an existing directory")
	}
	for i := len(tail) - 1; i >= 0; i-- {
		final = filepath.Join(final, tail[i])
	}
	if unsafeLaunchPath(final) {
		return fmt.Errorf("working folder resolves to a root, home, or system folder")
	}
	// Environment-defined system folders may themselves use junctions or short
	// names. Compare their canonical descendants as well as the lexical spelling.
	for _, root := range codexExtraRoots() {
		if root == "" {
			continue
		}
		resolved, err := finalCodexPath(root)
		if err != nil {
			continue
		}
		base, target := normalizeCodexPath(resolved), normalizeCodexPath(final)
		if target == base || strings.HasPrefix(target, base+"/") {
			return fmt.Errorf("working folder resolves inside a system folder")
		}
	}
	// File identity catches short names and alternate spellings of protected roots.
	if len(tail) == 0 {
		for _, root := range append(codexHomeDirs(), codexExtraRoots()...) {
			if root == "" {
				continue
			}
			other, err := os.Stat(root)
			if err == nil && os.SameFile(info, other) {
				return fmt.Errorf("working folder resolves to a protected root")
			}
		}
	}
	return nil
}

func unsafeLaunchPath(cwd string) bool {
	if unsafeCodexPath(cwd) || isCodexHomeRoot(cwd) {
		return true
	}
	p := normalizeCodexPath(cwd)
	if len(p) >= 2 && p[1] == ':' {
		p = p[2:]
	}
	if p == "/root" || p == "/users" || p == "/home" || p == "/system32" || strings.HasPrefix(p, "/system32/") {
		return true
	}
	for _, base := range []string{"/users/", "/home/"} {
		if rest, ok := strings.CutPrefix(p, base); ok && !strings.Contains(rest, "/") {
			return true
		}
	}
	return false
}
