package accounts

import (
	"context"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// TestLiveLoginURL runs each installed harness's real sign-in, in a throwaway account home,
// only until it shows the provider's sign-in URL, then cancels it: nobody signs in, and only the
// URL's host is printed (its state and code challenge are never shown).
//
//	TMXG_LIVE_LOGIN=codex,antigravity go test -run TestLiveLoginURL -v ./internal/accounts
//
// TMXG_LIVE_LOGIN lists the harnesses (claude, codex, antigravity, gemini). Codex's browser flow may
// open a browser tab on the machine it runs on: run it in Linux CI, or for codex add
// TMXG_LIVE_DEVICE=1 to use `codex login --device-auth`, which only prints a URL and a code.
func TestLiveLoginURL(t *testing.T) {
	list := os.Getenv("TMXG_LIVE_LOGIN")
	if list == "" {
		t.Skip("set TMXG_LIVE_LOGIN=claude,codex,antigravity to check the real sign-ins")
	}
	hosts := map[model.Harness]*regexp.Regexp{
		Claude:      regexp.MustCompile(`^(claude\.ai|console\.anthropic\.com|platform\.claude\.com)$`),
		Codex:       regexp.MustCompile(`^auth\.openai\.com$`),
		Antigravity: regexp.MustCompile(`^(accounts\.google\.com|antigravity\.google)$`),
		Gemini:      regexp.MustCompile(`^accounts\.google\.com$`),
	}
	urlRx := regexp.MustCompile(`https://[^\s"'\x1b\a]+`)
	for _, name := range strings.Split(list, ",") {
		h := model.Harness(strings.TrimSpace(name))
		want, ok := hosts[h]
		if !ok {
			t.Errorf("unknown harness %q", h)
			continue
		}
		t.Run(string(h), func(t *testing.T) {
			p, err := OpenProfiles(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Add(h, "live-check"); err != nil {
				t.Fatal(err)
			}
			s := New(Options{Dir: t.TempDir(), Every: -1, Profiles: p, StopGrace: 2 * time.Second})
			defer s.Close()
			if _, err := s.lookPath(h); err != nil {
				t.Skipf("%s is not installed", h)
			}
			if h == Codex && os.Getenv("TMXG_LIVE_DEVICE") == "1" {
				orig := s.entry(string(h), "live-check").def
				d := *orig
				d.login = func(context.Context, *scope, string) []string { return []string{"login", "--device-auth"} }
				s.entry(string(h), "live-check").def = &d
			}
			if _, err := s.Login(string(h), "live-check"); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.CancelLogin(string(h), "live-check") }()
			sub, err := s.AttachLogin(string(h), "live-check")
			if err != nil {
				t.Fatal(err)
			}
			defer s.DetachLogin(sub)
			var out strings.Builder
			out.Write(sub.Replay)
			deadline := time.After(45 * time.Second)
			for {
				for _, m := range urlRx.FindAllString(out.String(), -1) {
					if u, err := url.Parse(m); err == nil && want.MatchString(u.Host) {
						t.Logf("%s's sign-in reached https://%s%s (stopped there, nobody signed in)", h, u.Host, u.Path)
						return
					}
				}
				select {
				case b, ok := <-sub.C():
					if !ok {
						t.Fatalf("%s's sign-in ended before a sign-in URL (state %s)", h, loginOf(s.Payload(), h).State)
					}
					out.Write(b)
				case <-deadline:
					t.Fatalf("no %s sign-in URL within 45 s", h)
				}
			}
		})
	}
}

func loginOf(p Payload, h model.Harness) Login {
	for _, a := range p.Accounts {
		if a.Harness == string(h) && a.Name == "live-check" {
			return a.Login
		}
	}
	return Login{}
}
