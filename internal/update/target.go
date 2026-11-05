package update

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// target is what an update replaces on this machine, and which release file it takes.
type target struct {
	bundle bool   // a macOS .app bundle (else one executable file)
	path   string // the bundle directory or the executable
	goos   string
	goarch string
	desk   bool
}

// detectTarget works out the install from the running executable: inside tmxg.app on macOS
// the bundle; the Windows desktop exe; otherwise the headless binary.
func detectTarget(exe string, desktop bool, goos, goarch string) (target, error) {
	exe = filepath.Clean(exe)
	t := target{path: exe, goos: goos, goarch: goarch, desk: desktop}
	if goos == "darwin" {
		if i := strings.Index(exe, ".app"+string(filepath.Separator)+"Contents"+string(filepath.Separator)+"MacOS"+string(filepath.Separator)); i >= 0 {
			t.bundle, t.path = true, exe[:i+len(".app")]
			return t, nil
		}
		if desktop {
			return t, errors.New("this desktop build does not run from a tmxg.app bundle; update it by hand")
		}
	}
	return t, nil
}

// asset is the release file name for version v (the names the Makefile and CI give them).
func (t target) asset(v string) string {
	switch {
	case t.bundle:
		return fmt.Sprintf("tmxg-desktop_%s_darwin_%s.app.zip", v, t.goarch)
	case t.desk && t.goos == "windows":
		return fmt.Sprintf("tmxg-desktop_%s_windows_%s.exe", v, t.goarch)
	default:
		ext := ""
		if t.goos == "windows" {
			ext = ".exe"
		}
		return fmt.Sprintf("tmxg_%s_%s_%s%s", v, t.goos, t.goarch, ext)
	}
}

// executable is the binary inside what path names (the bundle's, or path itself).
func (t target) executable(path string) string {
	if t.bundle {
		return filepath.Join(path, "Contents", "MacOS", "tmxg")
	}
	return path
}
