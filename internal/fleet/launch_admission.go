package fleet

import (
	"runtime"

	"github.com/blackcode-ch/tmx-go/internal/agentpolicy"
	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Overridden only by fake-runner tests of platform-independent fleet behavior.
var launchGOOS = runtime.GOOS

// launchAdmissionLocked is shared by creation, adoption, restart, resume, and
// the final spawn path. Keep it free of filesystem/store/process mutations.
func (f *Fleet) launchAdmissionLocked(a model.Agent) error {
	// Shell registration and control are owner-only. It is the owner's terminal,
	// not an agent, and must remain usable to recover from agent policy failures.
	if a.Harness == model.HarnessShell {
		return nil
	}
	if f.opts.Elevated() {
		return errf(KindForbidden, "refusing to launch from an elevated process")
	}
	if err := harness.ValidateLaunchCwd(a.Cwd); err != nil {
		return errf(KindForbidden, "%s", err)
	}
	if err := agentpolicy.CheckLaunch(agentpolicy.LaunchPolicy{GOOS: launchGOOS, Harness: string(a.Harness), CodexParityGuard: f.codexParityEligibleLocked(a)}); err != nil {
		return errf(KindForbidden, "%s", err)
	}
	return f.guardAdmissionLocked(a)
}

func (f *Fleet) guardAdmissionLocked(a model.Agent) error {
	if a.Harness == model.HarnessShell {
		return nil
	}
	if !f.safetyLocked().settings.On(string(a.Level), a.Profile) {
		if launchGOOS == "windows" {
			return errf(KindForbidden, "Windows launches require an enabled command guard")
		}
		return nil
	}
	if a.Harness != model.HarnessClaude {
		if f.codexParityEligibleLocked(a) {
			return nil
		}
		return errf(KindForbidden, "the command guard has no enforced adapter for %s", a.Harness)
	}
	// o4 / tmx-lead, 3 Nov: also the Claude config folders the launch uses (the daemon's own
	// CLAUDE_CONFIG_DIR; per-account folders join via claudeConfigDirsLocked).
	if where := hooksDisabledIn(a.Cwd, f.claudeConfigDirsLocked(a)...); where != "" {
		return errf(KindForbidden, "the command guard is disabled in %s (disableAllHooks); remove that setting, then launch again", where)
	}
	return nil
}

// claudeConfigDirsLocked: the CLAUDE_CONFIG_DIR folders a Claude launch of a may read settings
// from: its account's home (#236), and the daemon's own folder (the default account; checked
// in any case, it is cheap).
func (f *Fleet) claudeConfigDirsLocked(a model.Agent) []string {
	dirs := []string{f.agentClaudeConfigDirLocked(a)}
	if d := f.opts.Paths.ClaudeConfigDir(); d != dirs[0] {
		dirs = append(dirs, d)
	}
	return dirs
}

func (f *Fleet) codexParityEligibleLocked(a model.Agent) bool {
	return launchGOOS == "windows" && a.Harness == model.HarnessCodex && !a.CodexFullAccess && !f.profileForLocked(a).ReadOnly && f.opts.Executable != ""
}
