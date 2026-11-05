package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// ExitDisabled is the exit code of the stubs a production start puts first on PATH for the
// harnesses that must not run as agents there (Hostb's GO-PROD-START.ps1: codex and agy, since
// the 9 Sep Codex sandbox lesson).
const ExitDisabled = 78

// maxStub: a disabled stub is a few lines; anything bigger is a real launcher.
const maxStub = 512

var stubRx = regexp.MustCompile(`(?i)\bexit\s+(/b\s+)?78\b`)

// IsDisabledStub reports whether path is such a stub: a small script that exits 78.
func IsDisabledStub(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Size() > maxStub {
		return false
	}
	b, err := os.ReadFile(path)
	return err == nil && stubRx.Match(b)
}

// SignInPath finds h's executable for the owner's sign-in (internal/accounts): like LookPath,
// but past any disabled stub, on PATH and then in the usual install folders (on Windows npm's
// %APPDATA%\npm). Agents keep LookPath, so the stub still keeps them from starting.
func SignInPath(h model.Harness) (string, error) {
	info, ok := Get(h)
	if !ok || len(info.commands) == 0 {
		return "", fmt.Errorf("harness %q has no executable", h)
	}
	dirs := filepath.SplitList(os.Getenv("PATH"))
	dirs = append(dirs, installDirs(h)...)
	var stub string
	for _, d := range dirs {
		if d == "" || !filepath.IsAbs(d) { // never the current folder
			continue
		}
		for _, c := range info.commands {
			for _, n := range signInNames(c) {
				p := filepath.Join(d, n)
				st, err := os.Stat(p)
				if err != nil || st.IsDir() || (runtime.GOOS != "windows" && st.Mode()&0o111 == 0) {
					continue
				}
				if IsDisabledStub(p) {
					stub = p
					continue
				}
				return p, nil
			}
		}
	}
	if stub != "" {
		return stub, nil // only the stub: the sign-in says so (exit 78)
	}
	return "", fmt.Errorf("%s is %w (%s)", info.Label, ErrNotInstalled, info.Install)
}

func signInNames(c string) []string {
	if runtime.GOOS == "windows" {
		return []string{c + ".exe", c + ".cmd", c + ".bat"}
	}
	return []string{c}
}
