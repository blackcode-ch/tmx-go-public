package release

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

// rebuild uses only committed source, never the caller's working tree or dist.
// Dependencies/toolchains must already be cached on the offline signer.
func rebuild(source, tag, commit string) ([]byte, error) {
	return rebuildWithCommand(source, tag, commit, rebuildCommand)
}

func rebuildWithCommand(source, tag, commit string, command func(string, string, ...string) (string, error)) ([]byte, error) {
	if _, ok := ParseTag(tag); source == "" || !ok || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) {
		return nil, fmt.Errorf("rebuild requires an exact fleet tag and full reviewed public commit SHA")
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(source); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("rebuild requires a local public source repository")
	}
	stage, err := os.MkdirTemp("", "tmx-rebuild-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	checkout := filepath.Join(stage, "source")

	if _, err := command(stage, "git", "clone", "--local", "--no-hardlinks", "--no-checkout", "--", source, checkout); err != nil {
		return nil, err
	}
	resolved, err := command(checkout, "git", "rev-parse", "--verify", "refs/tags/"+tag+"^{commit}")
	if err != nil {
		return nil, err
	}
	if resolved != commit {
		return nil, fmt.Errorf("tag %s resolves to %s, not reviewed commit %s", tag, resolved, commit)
	}
	if _, err := command(checkout, "git", "-c", "core.autocrlf=false", "checkout", "--detach", commit); err != nil {
		return nil, err
	}
	mod, err := os.ReadFile(filepath.Join(checkout, "go.mod"))
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(mod), "\ntoolchain go1.26.6\n") {
		return nil, fmt.Errorf("tag must pin toolchain go1.26.6 in go.mod")
	}
	for _, args := range [][]string{{"--dir", "ui", "install", "--offline", "--frozen-lockfile"}, {"--dir", "ui", "build"}} {
		if _, err := command(checkout, "pnpm", args...); err != nil {
			return nil, err
		}
	}
	if _, err := command(checkout, "go", "run", "./cmd/public-release", "-mode", "build", "-tag", tag, "-dir", "release-dist"); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(checkout, "release-dist", "SHA256SUMS"))
}

func compareRebuild(ci, local []byte) error {
	if bytes.Equal(ci, local) {
		return nil
	}
	// The entire four-line manifests are small and make every difference visible.
	return fmt.Errorf("refusing signature: rebuild SHA256SUMS differs\n--- CI SHA256SUMS\n%s\n+++ signer SHA256SUMS\n%s", ci, local)
}

func rebuildCommand(dir, program string, args ...string) (string, error) {
	c := hiddenexec.Command(program, args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GOTOOLCHAIN=go1.26.6", "GOENV=off", "GOFLAGS=", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := c.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("rebuild %s failed: %w\n%s", program, err, output)
	}
	return strings.TrimSpace(string(output)), nil
}
