package fleet

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/accounts"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
)

// An agent runs as the account its launch names, else its harness's default; a Claude agent's
// CLAUDE_CONFIG_DIR is then that account's home, and an unknown account is refused.
func TestLaunchAccount(t *testing.T) {
	e := newEnv(t)
	p, err := accounts.OpenProfiles(filepath.Join(e.home, "ai-accounts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(model.HarnessClaude, "second"); err != nil {
		t.Fatal(err)
	}
	e.accounts = p
	f := e.started()
	proj := workerProject(t, f)

	w, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: proj, Name: "w-second", Account: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Account != "second" {
		t.Fatalf("account %q", w.Account)
	}
	eventually(t, "started", func() bool { return e.chat.Last(w.ID) != nil })
	if dir := claude.ConfigDir(e.chat.Last(w.ID).Spec.Env); dir != p.Home(model.HarnessClaude, "second") {
		t.Fatalf("CLAUDE_CONFIG_DIR %q", dir)
	}

	d, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: proj, Name: "w-default"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "started", func() bool { return e.chat.Last(d.ID) != nil })
	if d.Account != accounts.DefaultName {
		t.Fatalf("account %q", d.Account)
	}
	assertIsolatedClaude(t, e, e.chat.Last(d.ID).Spec) // the daemon's own folder, as before

	if err := p.SetDefault(model.HarnessClaude, "second"); err != nil {
		t.Fatal(err)
	}
	n, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: proj, Name: "w-newdefault"})
	if err != nil || n.Account != "second" {
		t.Fatalf("launch default: %q %v", n.Account, err)
	}

	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: proj, Name: "w-bad", Account: "nope"}); err == nil {
		t.Fatal("an unknown account was accepted")
	}
}

// The CLAUDE_CONFIG_DIR the launch's hooks check reads: the account's folder, else the daemon's.
func TestAgentClaudeConfigDir(t *testing.T) {
	e := newEnv(t)
	p, _ := accounts.OpenProfiles(filepath.Join(e.home, "ai-accounts"))
	_ = p.Add(model.HarnessClaude, "second")
	f := &Fleet{opts: Options{Accounts: p, Paths: config.Paths{Home: e.home}}}
	if d := f.agentClaudeConfigDirLocked(model.Agent{Harness: model.HarnessClaude, Account: "second"}); d != p.Home(model.HarnessClaude, "second") {
		t.Errorf("account: %s", d)
	}
	if d := f.agentClaudeConfigDirLocked(model.Agent{Harness: model.HarnessClaude, Account: accounts.DefaultName}); d != (config.Paths{Home: e.home}).ClaudeConfigDir() {
		t.Errorf("default: %s", d)
	}
}

// An account's own CLAUDE_CONFIG_DIR with the hooks switched off refuses that account's
// launches; the default account still launches (tmx-lead, 3 Nov: #234 wired to #236).
func TestHooksDisabledInAccountRefusesLaunch(t *testing.T) {
	e := newEnv(t)
	p, err := accounts.OpenProfiles(filepath.Join(e.home, "ai-accounts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(model.HarnessClaude, "second"); err != nil {
		t.Fatal(err)
	}
	e.accounts = p
	f := e.started()
	proj := workerProject(t, f)
	home := p.Home(model.HarnessClaude, "second")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"disableAllHooks": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: proj, Name: "w-second", Account: "second"})
	if err == nil || !strings.Contains(err.Error(), "disableAllHooks") {
		t.Fatalf("launch on an account with hooks off: %v", err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: proj, Name: "w-default"}); err != nil {
		t.Fatalf("the default account: %v", err)
	}
}

func TestAccountEnvForOtherHarnesses(t *testing.T) {
	p, _ := accounts.OpenProfiles(t.TempDir())
	_ = p.Add(model.HarnessCodex, "work")
	_ = p.Add("antigravity", "work")
	f := &Fleet{opts: Options{Accounts: p}}
	codex := f.accountEnvLocked(model.Agent{Harness: model.HarnessCodex, Account: "work"})
	if !slices.Equal(codex, []string{"CODEX_HOME=" + p.Home(model.HarnessCodex, "work")}) {
		t.Errorf("codex %v", codex)
	}
	agy := f.accountEnvLocked(model.Agent{Harness: "agy", Account: "work"})
	if home := p.Home("antigravity", "work"); !slices.Equal(agy, []string{"HOME=" + home, "USERPROFILE=" + home}) {
		t.Errorf("agy %v", agy)
	}
	if env := f.accountEnvLocked(model.Agent{Harness: model.HarnessCodex, Account: accounts.DefaultName}); env != nil {
		t.Errorf("default %v", env)
	}
	if env := f.accountEnvLocked(model.Agent{Harness: model.HarnessOpenCode, Account: "work"}); env != nil {
		t.Errorf("opencode %v", env)
	}
}
