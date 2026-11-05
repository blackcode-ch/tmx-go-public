package fleet

import (
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// AccountHomes are the harnesses' named accounts (accounts.Profiles): an agent runs as one,
// whose home its harness is pointed at.
type AccountHomes interface {
	// Resolve is the account an agent of h asking for name runs as ("" = h's default).
	Resolve(h model.Harness, name string) (string, error)
	// Home is the account's private home; "" = the harness's usual one.
	Home(h model.Harness, name string) string
	// Env points h at the account's home (nothing for the usual one).
	Env(h model.Harness, name string) []string
}

// AccountHarness is the accounts' name of an agent's harness ("agy" is "antigravity" there).
func AccountHarness(h model.Harness) model.Harness {
	switch h {
	case "agy":
		return "antigravity"
	case "":
		return model.HarnessClaude
	}
	return h
}

// hasAccounts reports whether agents of h can pick an account.
func hasAccounts(h model.Harness) bool {
	switch AccountHarness(h) {
	case model.HarnessClaude, model.HarnessCodex, "antigravity", model.HarnessGemini:
		return true
	}
	return false
}

// launchAccountLocked is the account a new agent of h asking for name runs as: "" for a
// harness without accounts or when there is no account list.
func (f *Fleet) launchAccountLocked(h model.Harness, name string) (string, error) {
	if !hasAccounts(h) || f.opts.Accounts == nil {
		if name != "" && name != "default" {
			return "", errf(KindInvalid, "%s has no account %q here", h, name)
		}
		return "", nil
	}
	n, err := f.opts.Accounts.Resolve(AccountHarness(h), name)
	if err != nil {
		return "", errf(KindInvalid, "%s", err.Error())
	}
	return n, nil
}

// accountHomeLocked is a's account's private home, "" = its harness's usual one.
func (f *Fleet) accountHomeLocked(a model.Agent) string {
	if a.Account == "" || f.opts.Accounts == nil || !hasAccounts(a.Harness) {
		return ""
	}
	return f.opts.Accounts.Home(AccountHarness(a.Harness), a.Account)
}

// accountEnvLocked points a's harness at its account's home.
func (f *Fleet) accountEnvLocked(a model.Agent) []string {
	if a.Account == "" || f.opts.Accounts == nil || !hasAccounts(a.Harness) {
		return nil
	}
	return f.opts.Accounts.Env(AccountHarness(a.Harness), a.Account)
}

// agentClaudeConfigDirLocked is the CLAUDE_CONFIG_DIR claude agent a runs with, without making
// it: its account's home, or the daemon's own folder for the default account. The launch check
// for settings that switch hooks off reads it (#234's claudeConfigDirsLocked).
func (f *Fleet) agentClaudeConfigDirLocked(a model.Agent) string {
	if home := f.accountHomeLocked(a); home != "" {
		return home
	}
	return f.opts.Paths.ClaudeConfigDir()
}

// claudeConfigDirLocked is the CLAUDE_CONFIG_DIR of claude agent a: its account's home, or the
// daemon's own folder for the default account.
func (f *Fleet) claudeConfigDirLocked(a model.Agent) (string, error) {
	if home := f.accountHomeLocked(a); home != "" {
		return config.EnsureClaudeDir(home)
	}
	return f.opts.Paths.EnsureClaudeConfigDir()
}
