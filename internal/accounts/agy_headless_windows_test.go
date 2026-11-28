package accounts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"golang.org/x/sys/windows"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "auth":
			os.Exit(2)
		case "agy-quota-child":
			if err := RunAgyQuotaChild(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			os.Exit(0)
		case "-p":
			if len(os.Args) != 5 || os.Getenv("AGY_CLI_DISABLE_AUTO_UPDATE") != "true" ||
				os.Getenv(quotaPipe) != "" || os.Getenv(quotaProof) != "" || os.Getenv("TMX_TEST_AGY_CONTEXT") != "present" {
				os.Exit(3)
			}
			if cp, _ := windows.GetConsoleCP(); cp == 0 {
				os.Exit(4)
			}
			if os.Getenv("TMX_TEST_AGY_BLOCK") == "1" {
				time.Sleep(time.Minute)
				os.Exit(5)
			}
			cmd := hiddenexec.CommandInheritConsole(os.Args[0], "agy-quota-grandchild")
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				os.Exit(6)
			}
			fmt.Fprint(os.Stderr, "root diagnostic")
			os.Exit(23)
		case "agy-quota-grandchild":
			if cp, _ := windows.GetConsoleCP(); cp == 0 {
				os.Exit(7)
			}
			fmt.Fprint(os.Stdout, "{\"quota\":{\"fixture\":{\"remaining_fraction\":0.5}}}")
			fmt.Fprint(os.Stderr, "nested diagnostic\n")
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func TestWindowsHeadlessAgyQuotaPreservesStreamsAndExit(t *testing.T) {
	s := &scope{Service: &Service{o: Options{Dir: t.TempDir(), CheckTimeout: 10 * time.Second, Env: []string{"TMX_TEST_AGY_CONTEXT=present"}}}}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got := s.runHeadlessAgyQuota(context.Background(), exe)
	if got.err != nil || got.exitCode != 23 || string(got.out) != `{"quota":{"fixture":{"remaining_fraction":0.5}}}` || got.errText != "nested diagnostic\nroot diagnostic" {
		t.Fatalf("headless fixture: code=%d stdout=%q stderr=%q err=%v", got.exitCode, got.out, got.errText, got.err)
	}
}

func TestWindowsHeadlessAgyQuotaTimesOut(t *testing.T) {
	s := &scope{Service: &Service{o: Options{Dir: t.TempDir(), CheckTimeout: time.Second, Env: []string{"TMX_TEST_AGY_CONTEXT=present", "TMX_TEST_AGY_BLOCK=1"}}}}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got := s.runHeadlessAgyQuota(context.Background(), exe)
	if got.exitCode != -1 || got.err != context.DeadlineExceeded {
		t.Fatalf("timeout: %+v", got)
	}
}

func TestWindowsAgyTimeoutPreservesSignedInAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	accounts := filepath.Join(home, ".gemini", "google_accounts.json")
	if err := os.MkdirAll(filepath.Dir(accounts), 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fx := newFixture(t, func(o *Options) {
		o.LookPath = func(model.Harness) (string, error) { return exe, nil }
		o.CheckTimeout = 2 * time.Second
		o.Env = append(o.Env, "TMX_TEST_AGY_CONTEXT=present", "TMX_TEST_AGY_BLOCK=1")
	}, Antigravity)
	e := fx.s.entry(string(Antigravity), "")
	for _, tc := range []struct {
		name, active string
		wantLimits   int
	}{
		{"same identity", "first@example.com", 1},
		{"different identity", "second@example.com", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(accounts, []byte(fmt.Sprintf(`{"active":%q}`, tc.active)), 0o600); err != nil {
				t.Fatal(err)
			}
			fx.s.mu.Lock()
			e.acct.Connected = true
			e.acct.Account.Email = "first@example.com"
			e.acct.Limits = []Limit{{ID: "weekly", UsedPercent: 40}}
			fx.s.mu.Unlock()
			got := fx.refresh(t, string(Antigravity))
			if !got.Connected || got.Account.Email != tc.active || got.Error != "" ||
				!strings.HasPrefix(got.LimitsNote, "Quota unavailable:") || len(got.Limits) != tc.wantLimits {
				t.Fatalf("after quota timeout: %+v", got)
			}
			if tc.wantLimits > 0 && got.Limits[0].UsedPercent != 40 {
				t.Fatalf("cached quota changed: %+v", got.Limits)
			}
		})
	}
}
