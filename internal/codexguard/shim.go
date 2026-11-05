package codexguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

// ShimNames matches Python tiers.py's SAC, PC guard and slot shim name sets.
// PATH interception has the same built-in/absolute-path limitations as Python.
var ShimNames = []string{"uv", "uvx", "pip", "pip3", "virtualenv", "python", "python3", "py",
	"bcdedit", "diskpart", "vssadmin", "wbadmin", "cipher", "format", "wmic",
	"npm", "pnpm", "yarn", "pytest", "cargo", "dotnet"}

// ManifestFileName sits next to the shims directory (base/manifest.json,
// base/shims/<name>.exe), never in an agent-settable environment variable: a
// shim is the agent's own child process and could otherwise point RunShim at
// an attacker-controlled manifest (e.g. one claiming Own=C:\) by setting the
// old TMXG_CODEX_SHIM_MANIFEST before invoking pip/uv/etc.
const ManifestFileName = "manifest.json"

// ManifestPathFromExecutable derives the pinned manifest's path from a running
// shim's own executable path (os.Executable()), which the shim cannot spoof:
// exe is base/shims/<name>.exe, so the manifest is the sibling of its parent.
func ManifestPathFromExecutable(exe string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(exe)), ManifestFileName)
}

type Target struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Targets map[string]Target `json:"targets"`
	Shell   Target            `json:"shell"`
	Codex   Target            `json:"codex"`
	Guard   Guard             `json:"guard"`
}

// Guard is the launch-time trust boundary pinned into the manifest, so a shim
// never takes Own/Workspace/State from the agent's own environment: TMXG_GUARD_*
// is set on the agent's process by tmx at launch, but a shim runs as the agent's
// own child (e.g. from pip or uv), which can rewrite that same environment first.
type Guard struct {
	Own       string `json:"own"`
	Workspace string `json:"workspace"`
	State     string `json:"state"`
}

func ShimName(path string) string {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	for _, candidate := range ShimNames {
		if name == candidate {
			return name
		}
	}
	return ""
}

// ResolveTarget uses only absolute PATH entries and never searches the cwd.
// Batch wrappers use the separately pinned cmd adapter with restricted arguments.
func ResolveTarget(name, searchPath string) (string, error) {
	for _, dir := range filepath.SplitList(searchPath) {
		if !filepath.IsAbs(dir) {
			continue
		}
		for _, ext := range []string{".exe", ".cmd", ".bat"} {
			path := filepath.Join(dir, name+ext)
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("no native executable for %s on the launch PATH", name)
}

// RunShim runs exactly one manifest-bound native program after guard approval.
// It preserves stdin/stdout/stderr and argv. No shell evaluates an argument.
// authorize receives the manifest's own Guard boundary, never the agent's
// environment: a shim is the agent's own child process and can rewrite its
// environment before invoking uv/pip/etc., but not the pinned manifest file.
func RunShim(ctx context.Context, name string, args []string, manifestPath string, in io.Reader, out, errw io.Writer, authorize func(context.Context, string, []string, string, Guard) error) int {
	if ShimName(name) != name {
		fmt.Fprintln(errw, "tmx: invalid shim name")
		return 126
	}
	b, err := os.ReadFile(manifestPath)
	var manifest Manifest
	if err != nil || len(b) > 1<<20 || json.Unmarshal(b, &manifest) != nil {
		fmt.Fprintln(errw, "tmx: missing or invalid pinned shim manifest")
		return 126
	}
	target, ok := manifest.Targets[name]
	if !ok || !filepath.IsAbs(target.Path) || target.SHA256 == "" {
		fmt.Fprintln(errw, "tmx: "+name+" is not available to agents on this machine (no pinned native program; .com programs are not wrapped)")
		return 126
	}
	pin, err := OpenPin(target.Path)
	if err != nil {
		fmt.Fprintln(errw, "tmx: cannot pin executable")
		return 126
	}
	defer pin.Close()
	if pin.SHA256 != target.SHA256 {
		fmt.Fprintln(errw, "tmx: executable changed since launch")
		return 126
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(errw, "tmx: cannot determine command folder")
		return 126
	}
	if err := validateShimCwd(cwd); err != nil {
		fmt.Fprintln(errw, "tmx: "+err.Error())
		return 126
	}
	if authorize == nil {
		fmt.Fprintln(errw, "tmx: command guard is unavailable")
		return 126
	}
	if err := authorize(ctx, target.Path, args, cwd, manifest.Guard); err != nil {
		fmt.Fprintln(errw, "tmx: "+err.Error())
		return 126
	}
	if err := pin.Verify(); err != nil {
		fmt.Fprintln(errw, "tmx: executable identity changed")
		return 126
	}
	cmd := hiddenexec.Command(target.Path, args...)
	if ext := strings.ToLower(filepath.Ext(target.Path)); ext == ".cmd" || ext == ".bat" {
		shellPin, err := OpenPin(manifest.Shell.Path)
		if err != nil {
			fmt.Fprintln(errw, "tmx: batch interpreter unavailable")
			return 126
		}
		defer shellPin.Close()
		if shellPin.SHA256 != manifest.Shell.SHA256 {
			fmt.Fprintln(errw, "tmx: batch interpreter changed")
			return 126
		}
		cmd, err = batchCommand(shellPin.Path, target.Path, args)
		if err != nil {
			fmt.Fprintln(errw, "tmx: "+err.Error())
			return 126
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errw
	cmd.Dir = cwd
	err = cmd.Run()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	fmt.Fprintln(errw, "tmx: guarded program could not start")
	return 126
}

func validateShimCwd(cwd string) error {
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return fmt.Errorf("command working folder could not be resolved")
	}
	if !filepath.IsAbs(real) || strings.EqualFold(filepath.Clean(real), filepath.VolumeName(real)+string(filepath.Separator)) {
		return fmt.Errorf("refusing a command launched from a drive root")
	}
	for _, key := range []string{"SystemRoot", "WINDIR", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
		base := os.Getenv(key)
		if base == "" {
			continue
		}
		base = filepath.Clean(base)
		if strings.EqualFold(real, base) || strings.HasPrefix(strings.ToLower(real), strings.ToLower(base+string(filepath.Separator))) {
			return fmt.Errorf("refusing a command launched from a system folder")
		}
	}
	return nil
}
