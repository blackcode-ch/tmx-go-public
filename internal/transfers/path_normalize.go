package transfers

import (
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// normalizePath resolves the existing prefix, including for destinations that do
// not exist yet. Callers must reject unwanted links before normalizing.
func normalizePath(p string) string {
	return normalizePathForOS(p, runtime.GOOS, filepath.EvalSymlinks, longPath)
}

// Inject filesystem operations so Darwin alias regressions run on every OS.
func normalizePathForOS(p, goos string, resolve func(string) (string, error), expand func(string) string) string {
	clean, dir, base, join := path.Clean, path.Dir, path.Base, path.Join
	if goos == "windows" {
		clean, dir, base, join = filepath.Clean, filepath.Dir, filepath.Base, filepath.Join
	}
	p = clean(p)
	var existing func(string) string
	existing = func(p string) string {
		if real, err := resolve(p); err == nil {
			return clean(expand(real))
		}
		parent := dir(p)
		if parent != p {
			return join(existing(parent), base(p))
		}
		return clean(expand(p))
	}
	p = existing(p)
	if prefix, target := fixedOSPrefix(goos, p); prefix != "" {
		p = target + strings.TrimPrefix(p, prefix)
	}
	if goos == "windows" {
		p = strings.ToLower(p)
	}
	return clean(p)
}

func normalizeWindowsPath(p string, resolve func(string) (string, error), expand func(string) string) string {
	return normalizePathForOS(p, "windows", resolve, expand)
}

func equalPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func samePath(a, b string) bool {
	return normalizePath(a) == normalizePath(b)
}

func withinNormalized(p, root, separator string, normalize func(string) string) bool {
	if root == "" {
		return false
	}
	p, root = normalize(p), normalize(root)
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, separator)+separator)
}
