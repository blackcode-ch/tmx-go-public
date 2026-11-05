package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestLoginOutcomeDisabled(t *testing.T) {
	ls := &loginSession{path: `C:\tmx\disabled-cli-prod\codex.cmd`, exitCode: ExitDisabled}
	got := loginOutcome(ls, Account{Harness: "codex", Label: "Codex"})
	if got.State != LoginFailed || !strings.Contains(got.Message, "disabled on this machine") ||
		!strings.Contains(got.Message, ls.path) || !strings.Contains(got.Message, "no other Codex was found") {
		t.Fatalf("%+v", got)
	}
	ls.exitCode = 1
	if got := loginOutcome(ls, Account{Label: "Codex"}); got.Message != "The sign-in ended with exit code 1" {
		t.Fatalf("%+v", got)
	}
}

func TestScopeEnv(t *testing.T) {
	s := New(Options{Dir: t.TempDir(), Every: -1, Env: []string{"CODEX_HOME=/daemon"}})
	defer s.Close()
	plain := &scope{Service: s}
	if h := plain.codexHome(); h != "/daemon" {
		t.Errorf("default account home %q", h)
	}
	acct := &scope{Service: s, home: HomeEnv(Codex, "/acct")}
	if h := acct.codexHome(); h != "/acct" {
		t.Errorf("account home %q", h)
	}
}

// The account list in the service: added accounts show, signed out, with the launch default
// marked; the default account cannot be removed; nothing works without a list.
func TestServiceAccounts(t *testing.T) {
	root := t.TempDir()
	p, _ := OpenProfiles(root)
	s := New(Options{Dir: t.TempDir(), Every: -1, Profiles: p,
		LookPath: func(model.Harness) (string, error) { return "", ErrNotInstalled }})
	defer s.Close()
	if err := s.AddAccount("codex", "work"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAccount("codex", "work"); !errors.Is(err, ErrExists) {
		t.Errorf("dup %v", err)
	}
	if err := s.AddAccount("nope", "x"); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown %v", err)
	}
	var order []string
	for _, a := range s.Payload().Accounts {
		order = append(order, a.Harness+"/"+a.Name)
	}
	if strings.Join(order, " ") != "claude/default codex/default codex/work antigravity/default gemini/default" {
		t.Fatalf("order %v", order)
	}
	if err := s.SetLaunchDefault("codex", "work"); err != nil {
		t.Fatal(err)
	}
	for _, a := range s.Payload().Accounts {
		if a.Harness == "codex" && a.LaunchDefault != (a.Name == "work") {
			t.Errorf("%s launch_default %v", a.Name, a.LaunchDefault)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "homes", "codex", "work")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login("codex", "nope"); !errors.Is(err, ErrUnknown) {
		t.Errorf("login unknown account %v", err)
	}
	if _, err := s.Login("codex", "work"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("login %v", err)
	}
	if err := s.RemoveAccount("codex", DefaultName); !errors.Is(err, ErrIsDefault) {
		t.Errorf("remove default %v", err)
	}
	if err := s.RemoveAccount("codex", "work"); err != nil {
		t.Fatal(err)
	}
	if len(s.Payload().Accounts) != 4 || p.Default(Codex) != DefaultName {
		t.Fatalf("after remove %v", s.Payload().Accounts)
	}

	bare := New(Options{Dir: t.TempDir(), Every: -1})
	defer bare.Close()
	if err := bare.AddAccount("codex", "x"); !errors.Is(err, ErrNoProfiles) {
		t.Errorf("no list %v", err)
	}
}

// A second Codex account signs in with its own CODEX_HOME, never the default one (fakes: Unix).
func TestAccountLoginUsesItsHome(t *testing.T) {
	root := t.TempDir()
	p, _ := OpenProfiles(root)
	fx := newFixture(t, func(o *Options) { o.Profiles = p }, Codex)
	if err := fx.s.AddAccount("codex", "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.s.Login("codex", "work"); err != nil {
		t.Fatal(err)
	}
	sub, _ := fx.s.AttachLogin("codex", "work")
	var seen strings.Builder
	readUntil(t, sub, &seen, "localhost:1455")
	_ = fx.s.CancelLogin("codex", "work")
	homes := strings.Fields(string(must(os.ReadFile(filepath.Join(fx.f.Dir, "codex.home")))))
	want := p.Home(Codex, "work")
	if len(homes) == 0 || homes[len(homes)-1] != want {
		t.Fatalf("codex ran with CODEX_HOME %v, want %s last", homes, want)
	}
	for _, a := range fx.s.Payload().Accounts {
		if a.Harness == "codex" && a.Name == DefaultName && a.Login.State != LoginIdle {
			t.Errorf("the default account's sign-in moved: %+v", a.Login)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
