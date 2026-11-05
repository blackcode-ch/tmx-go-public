package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/codexguard"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// PrepareCodexGuard is called by both production runners before process creation.
// Merely obtaining a fleet admission does not skip any of these checks.
func PrepareCodexGuard(spec runner.Spec) (runner.Spec, *codexguard.Lease, error) {
	if spec.CodexGuardHome == "" {
		return spec, nil, nil
	}
	if runtime.GOOS != "windows" || spec.CodexWindowsSandbox || spec.CodexFullAccess || spec.PermissionMode == "plan" {
		return spec, nil, fmt.Errorf("Codex parity requires Windows, normal access and a writable profile; Windows sandbox is prohibited")
	}
	path, err := LookPath("codex", spec.Command)
	if err != nil {
		return spec, nil, err
	}
	if !strings.EqualFold(filepath.Ext(path), ".exe") {
		// The npm launcher is a wrapper. Pin the package's native executable,
		// never node+an unpinned JS launcher or a PATH-selected codex.cmd.
		arch := "x64"
		triple := "x86_64-pc-windows-msvc"
		if runtime.GOARCH == "arm64" {
			arch = "arm64"
			triple = "aarch64-pc-windows-msvc"
		}
		path = filepath.Join(filepath.Dir(path), "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-"+arch, "vendor", triple, "bin", "codex.exe")
	}
	// Own/Workspace/State come from spec.Env as tmx itself set it at launch admission
	// (guardSpecLocked), never re-read from the agent's own environment later: a shim
	// is the agent's own child process and could otherwise widen its own boundary by
	// setting TMXG_GUARD_* before calling into pip/uv/etc.
	guardCtx := codexguard.Guard{Own: spec.Cwd}
	for _, entry := range spec.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case guard.EnvWorkspace:
			guardCtx.Workspace = value
		case guard.EnvState:
			guardCtx.State = value
		}
	}
	lease, err := codexguard.Prepare(spec.CodexGuardHome, spec.Cwd, path, spec.CodexGuardExecutable, os.Getenv("PATH"), guardCtx)
	if err != nil {
		return spec, nil, err
	}
	// The agent's account's CODEX_HOME (spec.Env, the last one wins), else the daemon's.
	sourceHome := os.Getenv("CODEX_HOME")
	for _, kv := range spec.Env {
		if v, ok := strings.CutPrefix(kv, "CODEX_HOME="); ok && v != "" {
			sourceHome = v
		}
	}
	if sourceHome == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			lease.Close()
			return spec, nil, homeErr
		}
		sourceHome = filepath.Join(home, ".codex")
	}
	if err := codexguard.SyncOwnerAuth(sourceHome, lease.Config.Home); err != nil {
		lease.Close()
		return spec, nil, err
	}
	spec.Command = lease.Binary.Path
	spec.Env = append(spec.Env, lease.Env(os.Getenv("PATH"))...)
	return spec, lease, nil
}
