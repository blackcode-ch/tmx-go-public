// Package accountstest installs fake claude, codex and agy executables for tests of the AI
// accounts (internal/accounts, the API, the CLI). They are shell scripts (Unix only) driven by
// files next to them, and append their arguments to <name>.log there:
//
//   - claude: `auth status` prints claude-status.json; `-p …` (the /usage run) prints
//     claude-usage.json after claude-usage.delay seconds; `auth login` prints a line, reads one
//     from the terminal, prints "got:<line>", and "ok" signs in (claude-status-in.json), anything
//     else exits 1; `auth logout` signs out (claude-status-out.json).
//   - codex: `app-server` answers initialize (then a notification), account/read with
//     codex-account.json and account/rateLimits/read with codex-limits.json, or the
//     not-signed-in error when that file is missing; codex-broken makes it exit at once. `login
//     status` prints codex-login-status.txt (exit codex-login-status.exit); `login` is like
//     claude's (codex-account-in.json); `logout` copies codex-account-out.json. Each run appends its
//     CODEX_HOME to codex.home.
//   - agy: with agy-has-auth, `auth --help` lists login/status/logout, `auth status` prints
//     agy-status.txt, `auth login` is like claude's (agy-status-in.txt) and `auth logout` copies
//     agy-status-out.txt; without it `auth …` is an unknown command. `-p …` prints agy-usage.txt.
package accountstest

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

const claudeScript = `#!/bin/sh
d=$(dirname "$0")
printf '%s\n' "$*" >> "$d/claude.log"
case "$1" in
auth)
  case "$2" in
  status) cat "$d/claude-status.json"; exit $(cat "$d/claude-status.exit" 2>/dev/null || echo 0) ;;
  login)
    echo "Opening your browser to sign in to Claude"
    IFS= read -r line
    echo "got:$line"
    [ "$line" = "ok" ] || exit 1
    cp "$d/claude-status-in.json" "$d/claude-status.json"; exit 0 ;;
  logout) cp "$d/claude-status-out.json" "$d/claude-status.json"; echo "Successfully logged out"; exit 0 ;;
  esac ;;
-p)
  sleep "$(cat "$d/claude-usage.delay" 2>/dev/null || echo 0)"
  cat "$d/claude-usage.json"; exit 0 ;;
esac
echo "error: unknown command $*" >&2
exit 2
`

const codexScript = `#!/bin/sh
d=$(dirname "$0")
printf '%s\n' "$*" >> "$d/codex.log"
printf '%s\n' "$CODEX_HOME" >> "$d/codex.home"
case "$1" in
app-server)
  if [ -f "$d/codex-stderr-after-eof" ]; then
    IFS= read -r line
    exec 1>&-
    sleep 0.1
    echo "app-server: late diagnostic" >&2
    exit 1
  fi
  [ -f "$d/codex-broken" ] && { echo "app-server: boom" >&2; exit 1; }
  while IFS= read -r line; do
    id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
    case "$line" in
    *'"method":"initialize"'*)
      printf '{"id":"%s","result":{"userAgent":"fake"}}\n' "$id"
      printf '{"method":"remoteControl/status/changed","params":{"status":"disabled"}}\n' ;;
    *'"method":"account/read"'*) printf '{"id":"%s","result":%s}\n' "$id" "$(cat "$d/codex-account.json")" ;;
    *'"method":"account/rateLimits/read"'*)
      if [ -f "$d/codex-limits.json" ]; then printf '{"id":"%s","result":%s}\n' "$id" "$(cat "$d/codex-limits.json")"
      else printf '{"error":{"code":-32600,"message":"codex account authentication required to read rate limits"},"id":"%s"}\n' "$id"; fi ;;
    esac
  done ;;
login)
  if [ "$2" = status ]; then cat "$d/codex-login-status.txt"; exit $(cat "$d/codex-login-status.exit" 2>/dev/null || echo 0); fi
  echo "Starting local login server on http://localhost:1455"
  IFS= read -r line
  echo "got:$line"
  [ "$line" = "ok" ] || exit 1
  cp "$d/codex-account-in.json" "$d/codex-account.json"; exit 0 ;;
logout) cp "$d/codex-account-out.json" "$d/codex-account.json"; echo "Successfully logged out"; exit 0 ;;
esac
exit 2
`

// geminiScript is Gemini CLI's first run: it signs in inside its app, which stays open after.
// It appends its GEMINI_CLI_HOME, GEMINI_FORCE_FILE_STORAGE and GOOGLE_GENAI_USE_GCA to
// gemini.env; "ok" writes <home>/.gemini/google_accounts.json, anything else exits 1.
const geminiScript = `#!/bin/sh
d=$(dirname "$0")
printf '%s\n' "$*" >> "$d/gemini.log"
printf '%s %s %s\n' "$GEMINI_CLI_HOME" "$GEMINI_FORCE_FILE_STORAGE" "$GOOGLE_GENAI_USE_GCA" >> "$d/gemini.env"
home=${GEMINI_CLI_HOME:-$HOME}
echo "Please visit the following URL to authorize the application:"
echo "https://accounts.google.com/o/oauth2/v2/auth?client_id=x&state=y"
IFS= read -r line
[ "$line" = "ok" ] || exit 1
mkdir -p "$home/.gemini"
printf '{"active":"%s","old":[]}' "$(cat "$d/gemini-email" 2>/dev/null || echo owner@example.com)" > "$home/.gemini/google_accounts.json"
echo "Type your message or @path/to/file"
sleep 600
`

const agyScript = `#!/bin/sh
d=$(dirname "$0")
printf '%s\n' "$*" >> "$d/agy.log"
if [ "$1" = auth ]; then
  [ -f "$d/agy-has-auth" ] || { echo "error: unknown command \"auth\"" >&2; exit 2; }
  case "$2" in
  --help) printf 'Manage authentication\n\nCommands:\n  login   Sign in\n  status  Show status\n  logout  Sign out\n' ;;
  status) cat "$d/agy-status.txt" ;;
  login)
    echo "Opening your browser to sign in with Google"
    IFS= read -r line
    echo "got:$line"
    [ "$line" = "ok" ] || exit 1
    cp "$d/agy-status-in.txt" "$d/agy-status.txt" ;;
  logout) cp "$d/agy-status-out.txt" "$d/agy-status.txt" ;;
  esac
  exit 0
fi
if [ "$1" = -p ]; then cat "$d/agy-usage.txt"; exit 0; fi
echo "Sign in with Google to start"
IFS= read -r line
[ "$line" = "ok" ] || exit 1
`

// Sample answers.
const (
	ClaudeStatusMax = `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"me@example.com",` +
		`"orgId":"0000-org","orgName":"Example Org","subscriptionType":"max"}`
	ClaudeStatusAPIKey = `{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty","apiKeySource":"ANTHROPIC_API_KEY"}`
	ClaudeStatusOut    = `{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`
	ClaudeUsageText    = "You are currently using your subscription to power your Claude Code usage\n\n" +
		"Current session: 8% used · resets Oct 29 at 9:10pm (Asia/Karachi)\n" +
		"Current week (all models): 54% used · resets Nov 3 at 6am (Asia/Karachi)\n" +
		"Current week (Fable): 6% used · resets Nov 3 at 6am (Asia/Karachi)\n\n" +
		"What's contributing to your limits usage? …"
	CodexAccountChatGPT = `{"account":{"type":"chatgpt","email":"me@example.com","planType":"plus"},"requiresOpenaiAuth":true}`
	CodexAccountAPIKey  = `{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}`
	CodexAccountNone    = `{"account":null,"requiresOpenaiAuth":true}`
	CodexLimits         = `{"ordinaryUsageAllowed":true,"rateLimits":{"limitId":"codex","limitName":null,"normalModelSlug":null,` +
		`"primary":{"usedPercent":12.5,"windowDurationMins":300,"resetsAt":1730000000},` +
		`"secondary":{"usedPercent":40,"windowDurationMins":10080,"resetsAt":1730500000},` +
		`"credits":null,"individualLimit":null,"spendControlReached":null,"planType":"plus","rateLimitReachedType":null},` +
		`"rateLimitsByLimitId":null,"rateLimitResetCredits":null,"accountId":null,"rateLimitUpsell":null}`
	AgyStatusIn  = "Signed in as me@example.com\nPlan: pro\n"
	AgyStatusOut = "Not signed in. Run agy to sign in.\n"
	AgyQuota     = `{"type":"result","quota":{"gemini-3-pro":{"remaining_fraction":0.94,"reset_time":"2030-01-02T03:04:05Z","reset_in_seconds":3600},` +
		`"claude-sonnet":{"remaining_fraction":0.5,"reset_in_seconds":7200}}}`
)

// ClaudeUsage is `claude -p /usage --output-format json`'s answer around text.
func ClaudeUsage(text string) string {
	b := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(text)
	return `{"type":"result","subtype":"success","is_error":false,"duration_ms":4600,"num_turns":0,"result":"` + b +
		`","session_id":"00000000-0000-0000-0000-000000000000","total_cost_usd":0}`
}

// Fakes is a folder of fake harnesses.
type Fakes struct {
	Dir string
	t   testing.TB
}

// Supported reports whether the fakes run here (they are shell scripts).
func Supported() bool { return runtime.GOOS != "windows" }

// Install puts the fakes for harnesses (claude, codex, antigravity) into dir, signed in with
// the sample answers. It skips the test on Windows.
func Install(t testing.TB, dir string, harnesses ...model.Harness) *Fakes {
	t.Helper()
	if !Supported() {
		t.Skip("the fake harnesses are shell scripts")
	}
	f := &Fakes{Dir: dir, t: t}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, h := range harnesses {
		switch h {
		case model.HarnessClaude:
			f.Set("claude-status.json", ClaudeStatusMax)
			f.Set("claude-status-in.json", ClaudeStatusMax)
			f.Set("claude-status-out.json", ClaudeStatusOut)
			f.Set("claude-usage.json", ClaudeUsage(ClaudeUsageText))
			f.write("claude", claudeScript, 0o755)
		case model.HarnessCodex:
			f.Set("codex-account.json", CodexAccountChatGPT)
			f.Set("codex-account-in.json", CodexAccountChatGPT)
			f.Set("codex-account-out.json", CodexAccountNone)
			f.Set("codex-limits.json", CodexLimits)
			f.Set("codex-login-status.txt", "Logged in using ChatGPT")
			f.write("codex", codexScript, 0o755)
		case "antigravity":
			f.Set("agy-has-auth", "")
			f.Set("agy-status.txt", AgyStatusIn)
			f.Set("agy-status-in.txt", AgyStatusIn)
			f.Set("agy-status-out.txt", AgyStatusOut)
			f.Set("agy-usage.txt", AgyQuota)
			f.write("agy", agyScript, 0o755)
		case model.HarnessGemini:
			f.write("gemini", geminiScript, 0o755)
		default:
			t.Fatalf("no fake for %q", h)
		}
	}
	return f
}

func (f *Fakes) write(name, content string, mode os.FileMode) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.Dir, name), []byte(content), mode); err != nil {
		f.t.Fatal(err)
	}
}

// Set writes one of the files the fakes read.
func (f *Fakes) Set(name, content string) { f.t.Helper(); f.write(name, content, 0o600) }

// Remove deletes one of the files the fakes read.
func (f *Fakes) Remove(name string) {
	f.t.Helper()
	if err := os.Remove(filepath.Join(f.Dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal(err)
	}
}

// Log is what the fake name (claude, codex, agy) was started with, one line per run.
func (f *Fakes) Log(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.Dir, name+".log"))
	return string(b)
}

// LookPath finds a harness's fake in the folder only (never a real harness).
func (f *Fakes) LookPath(h model.Harness) (string, error) { return LookPathIn(f.Dir)(h) }

// LookPathIn finds harnesses in dir only (accounts.Options.LookPath for a test daemon, whose
// accounts must never reach the real harnesses).
func LookPathIn(dir string) func(model.Harness) (string, error) {
	return func(h model.Harness) (string, error) {
		name := string(h)
		if h == "antigravity" {
			name = "agy"
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return "", errors.New("not installed")
		}
		return p, nil
	}
}
