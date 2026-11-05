package accounts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A second Gemini CLI account signs in inside the app with its own GEMINI_CLI_HOME and the token
// kept in that folder (not the shared keychain); tmx stops the app once the account shows the
// sign-in. Fakes: Unix.
func TestGeminiAccountSignIn(t *testing.T) {
	old := signedInPoll
	signedInPoll = 50 * time.Millisecond
	t.Cleanup(func() { signedInPoll = old })
	p, _ := OpenProfiles(t.TempDir())
	fx := newFixture(t, func(o *Options) { o.Profiles = p }, Gemini)
	if err := fx.s.AddAccount("gemini", "work"); err != nil {
		t.Fatal(err)
	}
	home := p.Home(Gemini, "work")
	if _, err := fx.s.Login("gemini", "work"); err != nil {
		t.Fatal(err)
	}
	sub, _ := fx.s.AttachLogin("gemini", "work")
	var seen strings.Builder
	readUntil(t, sub, &seen, "accounts.google.com")
	if err := fx.s.LoginInput("gemini", "work", []byte("ok\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "signed in", func() bool {
		for _, a := range fx.s.Payload().Accounts {
			if a.Harness == "gemini" && a.Name == "work" {
				return a.Login.State == LoginSucceeded
			}
		}
		return false
	})
	for _, a := range fx.s.Payload().Accounts {
		if a.Harness != "gemini" {
			continue
		}
		switch a.Name {
		case "work":
			if !a.Connected || a.Account.Email != "owner@example.com" || a.Login.Message != "Signed in as owner@example.com" {
				t.Errorf("work: %+v", a)
			}
		case DefaultName:
			if a.Connected {
				t.Errorf("the default account got the sign-in: %+v", a)
			}
		}
	}
	env := strings.TrimSpace(string(must(os.ReadFile(filepath.Join(fx.f.Dir, "gemini.env")))))
	if env != home+" true true" {
		t.Errorf("gemini ran with %q, want %q", env, home+" true true")
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "google_accounts.json")); err != nil {
		t.Errorf("not in the account's folder: %v", err)
	}
}

// Gemini CLI has no sign-out command: Logout says so and runs nothing.
func TestGeminiNoSignOutCommand(t *testing.T) {
	fx := newFixture(t, nil, Gemini)
	if _, err := fx.s.Logout(t.Context(), "gemini", ""); err == nil || !strings.Contains(err.Error(), "/signout") {
		t.Fatalf("logout: %v", err)
	}
	if fx.f.Log("gemini") != "" {
		t.Fatalf("ran %q", fx.f.Log("gemini"))
	}
}

// The check reads only the signed-in address, from the account's own folder.
func TestGeminiCheck(t *testing.T) {
	home := t.TempDir()
	s := New(Options{Dir: t.TempDir(), Every: -1})
	defer s.Close()
	sc := &scope{Service: s, home: HomeEnv(Gemini, home)}
	if r := geminiCheck(t.Context(), sc, ""); !r.known || r.connected {
		t.Fatalf("no file: %+v", r)
	}
	os.MkdirAll(filepath.Join(home, ".gemini"), 0o700)
	os.WriteFile(filepath.Join(home, ".gemini", "google_accounts.json"), []byte(`{"active":null,"old":["a@b.c"]}`), 0o600)
	if r := geminiCheck(t.Context(), sc, ""); !r.known || r.connected {
		t.Fatalf("signed out: %+v", r)
	}
	os.WriteFile(filepath.Join(home, ".gemini", "google_accounts.json"), []byte(`{"active":"a@b.c","old":[]}`), 0o600)
	if r := geminiCheck(t.Context(), sc, ""); !r.connected || r.id.Email != "a@b.c" || r.id.Method != MethodSubscription {
		t.Fatalf("signed in: %+v", r)
	}
	os.WriteFile(filepath.Join(home, ".gemini", "google_accounts.json"), []byte(`not json`), 0o600)
	if r := geminiCheck(t.Context(), sc, ""); r.known || r.err == "" {
		t.Fatalf("damaged: %+v", r)
	}
}
