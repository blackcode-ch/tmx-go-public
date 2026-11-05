package harness

import (
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// CodexSandboxArgs limit a workspace-write sandbox to the working folder (macOS and Linux;
// on Windows CodexPolicy picks danger-full-access and these options are inert). The Windows
// branch below runs only with spec.CodexWindowsSandbox, which nothing sets today, and then
// unelevated: no administrator-approved setup. UNVERIFIED there: the unelevated sandbox writes
// lasting ACL entries on its writable roots, and it is not confirmed that the pinned Codex
// honours windows.sandbox (ignored, it may fall back to the elevated setup). Check it on a
// throwaway Windows VM with icacls snapshots before and after, before wiring any opt-in (9 Sep).
func CodexSandboxArgs(spec runner.Spec) []string {
	args := []string{
		"-c", "sandbox_workspace_write.writable_roots=[]",
		"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true",
		"-c", "sandbox_workspace_write.exclude_slash_tmp=true",
		"-c", "sandbox_workspace_write.network_access=false",
	}
	if codexGOOS == "windows" && spec.CodexWindowsSandbox {
		args = append(args, "-c", `windows.sandbox="unelevated"`)
	}
	return args
}

func ValidateCodexLaunch(spec runner.Spec) error {
	if err := validateCodexSpec(spec, runner.Elevated()); err != nil {
		return err
	}
	if spec.CodexFullAccess && spec.PermissionMode != "plan" {
		slog.Warn("Codex full access explicitly enabled for agent", "agent", spec.AgentID, "name", spec.Name, "cwd", spec.Cwd)
	}
	return nil
}

func validateCodexSpec(spec runner.Spec, elevated bool) error {
	if err := validateLaunchCwd(spec.Cwd, elevated); err != nil {
		return err
	}
	if len(spec.ExtraArgs) != 0 || len(spec.AddDirs) != 0 {
		return fmt.Errorf("Codex extra arguments and additional writable directories are not allowed")
	}
	if info, err := os.Stat(spec.Cwd); err != nil || !info.IsDir() {
		return fmt.Errorf("Codex working folder must be an existing directory")
	}
	return nil
}

// codexHomeDirs returns the candidate canonical home/profile roots to refuse as a Codex
// working folder. Overridable in tests (see codex_safety_test.go) since os.UserHomeDir and
// USERPROFILE/HOME reflect the real machine, not a test fixture.
var codexHomeDirs = defaultCodexHomeDirs

func defaultCodexHomeDirs() []string {
	var dirs []string
	for _, v := range []string{os.Getenv("USERPROFILE"), os.Getenv("HOME")} {
		if v != "" {
			dirs = append(dirs, v)
		}
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		dirs = append(dirs, h)
	}
	return dirs
}

// isCodexHomeRoot reports whether dir IS one of the canonical home/profile roots (not a
// subfolder of one) — accepting a cwd under home is fine, accepting home itself makes the
// whole profile writable.
func isCodexHomeRoot(dir string) bool {
	if dir == "" {
		return false
	}
	normalizedDir := normalizeCodexPath(dir)
	for _, home := range codexHomeDirs() {
		if home == "" {
			continue
		}
		if normalizedDir == normalizeCodexPath(home) {
			return true
		}
	}
	return false
}

func normalizeCodexPath(dir string) string {
	return path.Clean(strings.ToLower(strings.ReplaceAll(dir, `\`, "/")))
}

// codexExtraRoots returns additional canonical roots (beyond the structural "/" and the
// blocked system-folder list) to refuse as a Codex working folder. Overridable in tests so a
// junction can target a fake stand-in instead of a real drive/system root.
var codexExtraRoots = defaultCodexExtraRoots

func defaultCodexExtraRoots() []string {
	var dirs []string
	for _, key := range []string{"SystemRoot", "windir", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
		if v := os.Getenv(key); v != "" {
			dirs = append(dirs, v)
		}
	}
	return dirs
}

func unsafeCodexPath(dir string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(dir, `\`, "/"))
	if normalized == "" || strings.HasPrefix(normalized, "//") {
		return true
	}
	normalized = path.Clean(normalized)
	local := normalized
	if len(local) >= 2 && local[1] == ':' {
		local = local[2:]
		if !strings.HasPrefix(local, "/") {
			return true
		}
	}
	if local == "/" || local == "." {
		return true
	}
	if macUserTemp(local) {
		return false
	}
	blocked := []string{"/windows", "/program files", "/program files (x86)", "/programdata", "/system volume information", "/$recycle.bin",
		"/system", "/library", "/usr", "/bin", "/sbin", "/etc", "/var", "/private/etc", "/private/var", "/applications", "/opt", "/boot", "/dev", "/proc", "/sys"}
	for _, system := range blocked {
		if local == system || strings.HasPrefix(local, system+"/") {
			return true
		}
	}
	for _, system := range codexExtraRoots() {
		system = strings.ToLower(strings.ReplaceAll(system, `\`, "/"))
		if system != "" && (normalized == path.Clean(system) || strings.HasPrefix(normalized, path.Clean(system)+"/")) {
			return true
		}
	}
	return false
}

// macUserTemp reports whether p (cleaned, lower case) is inside one user's own temp or cache
// folder on macOS: /var/folders/<bucket>/<user>/<T|C|0>/... (the real path starts with /private).
// Those belong to the user, not the system; the rest of /var and /private/var stays refused.
func macUserTemp(p string) bool {
	for _, base := range []string{"/var/folders/", "/private/var/folders/"} {
		if rest, ok := strings.CutPrefix(p, base); ok {
			return len(strings.Split(rest, "/")) >= 3
		}
	}
	return false
}
