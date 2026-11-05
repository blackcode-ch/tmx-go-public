package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/accounts/accountstest"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
)

type events struct {
	mu  sync.Mutex
	all []Payload
}

func (e *events) publish(ev model.BusEvent) {
	if ev.Type != EvUpdated {
		panic("event " + ev.Type)
	}
	e.mu.Lock()
	e.all = append(e.all, ev.Data.(Payload))
	e.mu.Unlock()
}

// last is h's account in the newest event.
func (e *events) last(h string) Account {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.all) == 0 {
		return Account{}
	}
	for _, a := range e.all[len(e.all)-1].Accounts {
		if a.Harness == h {
			return a
		}
	}
	return Account{}
}

func (e *events) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.all)
}

// seen reports whether any event so far had an account matching fn.
func (e *events) seen(h string, fn func(Account) bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range e.all {
		for _, a := range p.Accounts {
			if a.Harness == h && fn(a) {
				return true
			}
		}
	}
	return false
}

type fixture struct {
	s         *Service
	f         *accountstest.Fakes
	ev        *events
	codexHome string
}

func newFixture(t *testing.T, mod func(*Options), harnesses ...model.Harness) fixture {
	t.Helper()
	fx := fixture{f: accountstest.Install(t, t.TempDir(), harnesses...), ev: &events{}, codexHome: t.TempDir()}
	o := Options{Dir: t.TempDir(), LookPath: fx.f.LookPath, Publish: fx.ev.publish, Every: -1,
		CheckTimeout: 5 * time.Second, UsageTimeout: 5 * time.Second, StopGrace: 500 * time.Millisecond,
		Env: []string{"CODEX_HOME=" + fx.codexHome, "CLAUDE_CONFIG_DIR=" + t.TempDir(), "GEMINI_CLI_HOME=" + t.TempDir()}}
	if mod != nil {
		mod(&o)
	}
	fx.s = New(o)
	t.Cleanup(fx.s.Close)
	return fx
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (fx fixture) acct(h string) Account {
	for _, a := range fx.s.Payload().Accounts {
		if a.Harness == h {
			return a
		}
	}
	panic("no account " + h)
}

// refresh checks h and waits for the result.
func (fx fixture) refresh(t *testing.T, h string) Account {
	t.Helper()
	if err := fx.s.Refresh(h, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, h+" refreshed", func() bool { return !fx.acct(h).Refreshing && !fx.ev.last(h).Refreshing })
	return fx.acct(h)
}

func TestClaudeAccount(t *testing.T) {
	fx := newFixture(t, nil, Claude)
	if err := fx.s.Refresh("nope", ""); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown harness: %v", err)
	}
	a := fx.refresh(t, "claude")
	if !a.Installed || !a.Connected || a.Error != "" || a.LimitsNote != "" || a.CheckedAt == 0 || a.Label != "Claude" {
		t.Fatalf("%+v", a)
	}
	if a.Account != (Identity{Email: "me@example.com", Plan: "Max", Method: MethodSubscription, Org: "Example Org"}) {
		t.Fatalf("account %+v", a.Account)
	}
	if len(a.Limits) != 3 || a.Limits[0].ID != "session" || a.Limits[0].UsedPercent != 8 || a.Limits[0].ResetsText != "Oct 29 at 9:10pm" ||
		a.Limits[1].ID != "week" || a.Limits[1].UsedPercent != 54 {
		t.Fatalf("limits %+v", a.Limits)
	}
	if a.Login != (Login{State: LoginIdle}) || !strings.Contains(a.Install, "claude") {
		t.Fatalf("login %+v install %q", a.Login, a.Install)
	}
	log := fx.f.Log("claude")
	if !strings.Contains(log, "auth status --json\n") || !strings.Contains(log, "-p /usage --output-format json --no-session-persistence --strict-mcp-config\n") {
		t.Fatalf("claude ran: %q", log)
	}
	if !fx.ev.seen("claude", func(a Account) bool { return a.Refreshing }) || !fx.ev.seen("claude", func(a Account) bool { return !a.Refreshing && a.Connected }) {
		t.Fatal("no accounts.updated for the start and the end of the refresh")
	}
	p := fx.s.Payload()
	if len(p.Accounts) != 4 || p.Accounts[3].Harness != "gemini" || p.Accounts[0].Harness != "claude" || p.Accounts[1].Harness != "codex" || p.Accounts[2].Harness != "antigravity" || p.UpdatedAt == 0 {
		t.Fatalf("payload %+v", p)
	}

	// An API key: no plan limits, and /usage is not run.
	fx.f.Set("claude-status.json", accountstest.ClaudeStatusAPIKey)
	runs := strings.Count(fx.f.Log("claude"), "-p /usage")
	a = fx.refresh(t, "claude")
	if !a.Connected || a.Account.Method != MethodAPIKey || len(a.Limits) != 0 || a.LimitsNote != "Signed in with an API key: no plan limits" {
		t.Fatalf("api key: %+v", a)
	}
	if strings.Count(fx.f.Log("claude"), "-p /usage") != runs {
		t.Fatal("/usage ran for an API key")
	}

	// /usage that says it is for subscriptions only.
	fx.f.Set("claude-status.json", accountstest.ClaudeStatusMax)
	fx.f.Set("claude-usage.json", accountstest.ClaudeUsage("You are currently using API usage billing"))
	if a = fx.refresh(t, "claude"); len(a.Limits) != 0 || a.LimitsNote != "Signed in with an API key: no plan limits" {
		t.Fatalf("api usage text: %+v", a)
	}

	// Garbage from /usage keeps the limits shown and says why they are old.
	fx.f.Set("claude-usage.json", accountstest.ClaudeUsage(accountstest.ClaudeUsageText))
	fx.refresh(t, "claude")
	fx.f.Set("claude-usage.json", "Segmentation fault")
	if a = fx.refresh(t, "claude"); len(a.Limits) != 3 || a.LimitsNote != "Couldn't read usage: Segmentation fault" {
		t.Fatalf("usage garbage: %+v", a)
	}
	// Garbage from the status keeps everything and reports the error.
	fx.f.Set("claude-status.json", "boom")
	if a = fx.refresh(t, "claude"); !a.Connected || len(a.Limits) != 3 || a.Error != "Couldn't read claude auth status: boom" {
		t.Fatalf("status garbage: %+v", a)
	}
	// Signed out.
	fx.f.Set("claude-status.json", accountstest.ClaudeStatusOut)
	fx.f.Set("claude-status.exit", "1")
	if a = fx.refresh(t, "claude"); a.Connected || a.Error != "" || len(a.Limits) != 0 || a.Account != (Identity{Method: MethodUnknown}) {
		t.Fatalf("signed out: %+v", a)
	}
}

func TestNotInstalled(t *testing.T) {
	fx := newFixture(t, nil)
	if err := fx.s.Refresh("", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "all refreshed", func() bool {
		for _, a := range fx.s.Payload().Accounts {
			if a.Refreshing || a.CheckedAt == 0 {
				return false
			}
		}
		return true
	})
	for _, a := range fx.s.Payload().Accounts {
		if a.Installed || a.Connected || a.Install == "" || a.Limits == nil || len(a.Limits) != 0 {
			t.Errorf("%+v", a)
		}
	}
	if _, err := fx.s.Login("codex", ""); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("login: %v", err)
	}
	if _, err := fx.s.Login("nope", ""); !errors.Is(err, ErrUnknown) {
		t.Fatalf("login unknown: %v", err)
	}
	if err := fx.s.LoginInput("claude", "", []byte("x")); !errors.Is(err, ErrNoLogin) {
		t.Fatalf("input: %v", err)
	}
}

func TestRefreshOneAtATime(t *testing.T) {
	fx := newFixture(t, nil, Claude)
	fx.f.Set("claude-usage.delay", "0.4")
	for range 4 {
		if err := fx.s.Refresh("claude", ""); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "refreshed", func() bool { return !fx.acct("claude").Refreshing })
	// The first, and one more for the three asked for while it ran.
	if n := strings.Count(fx.f.Log("claude"), "auth status"); n != 2 {
		t.Fatalf("%d checks: %q", n, fx.f.Log("claude"))
	}
}

func TestStartRefreshesInTheBackground(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.Every = 50 * time.Millisecond }, Claude)
	fx.f.Set("claude-usage.delay", "0.3")
	begin := time.Now()
	fx.s.Start()
	if time.Since(begin) > 200*time.Millisecond {
		t.Fatal("Start waited for the check")
	}
	waitFor(t, "two periodic checks", func() bool { return strings.Count(fx.f.Log("claude"), "auth status") >= 2 })
	if st, err := os.Stat(fx.s.o.Dir); err != nil || !st.IsDir() {
		t.Fatalf("working folder: %v", err)
	}
}

func TestCodexAccount(t *testing.T) {
	fx := newFixture(t, nil, Codex)
	a := fx.refresh(t, "codex")
	if !a.Connected || a.Error != "" || a.Account != (Identity{Email: "me@example.com", Plan: "Plus", Method: MethodSubscription}) {
		t.Fatalf("%+v", a)
	}
	if len(a.Limits) != 2 || a.Limits[0] != (Limit{ID: "5h", Label: "5-hour limit", UsedPercent: 12.5, ResetsAt: 1730000000000}) ||
		a.Limits[1].ID != "week" || a.LimitsNote != "" {
		t.Fatalf("limits %+v %q", a.Limits, a.LimitsNote)
	}
	if log := fx.f.Log("codex"); !strings.HasPrefix(log, "app-server -c check_for_update_on_startup=false\n") {
		t.Fatalf("codex ran: %q", log)
	}

	// Codex cannot say its limits: the last session's.
	fx.f.Remove("codex-limits.json")
	if a = fx.refresh(t, "codex"); !a.Connected || len(a.Limits) != 2 ||
		a.LimitsNote != "Couldn't read usage: codex account authentication required to read rate limits" {
		t.Fatalf("no limits, no rollout: %+v", a)
	}
	ts := time.Now().Add(-10 * time.Minute).UTC()
	rollout(t, fx.codexHome, ts.Format("2006/01/02"), "rollout-x.jsonl", ts,
		`{"timestamp":"`+ts.Format(time.RFC3339)+`","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":33,"window_minutes":300,"resets_in_seconds":3600}}}}`)
	if a = fx.refresh(t, "codex"); len(a.Limits) != 1 || a.Limits[0].UsedPercent != 33 || a.LimitsNote != "From your last Codex session, 10 min ago" {
		t.Fatalf("rollout: %+v", a)
	}

	// An app-server that fails: `codex login status` and the rollout.
	fx.f.Set("codex-broken", "")
	if a = fx.refresh(t, "codex"); !a.Connected || a.Account.Method != MethodSubscription || a.Account.Email != "" || len(a.Limits) != 1 {
		t.Fatalf("broken app-server: %+v", a)
	}
	fx.f.Set("codex-login-status.txt", "Logged in using an API key - sk-proj-***ABCD")
	if a = fx.refresh(t, "codex"); a.Account.Method != MethodAPIKey || len(a.Limits) != 0 || strings.Contains(a.LimitsNote, "sk-") {
		t.Fatalf("api key status: %+v", a)
	}
	fx.f.Set("codex-login-status.txt", "garbage")
	if a = fx.refresh(t, "codex"); !strings.HasPrefix(a.Error, "Couldn't read Codex's sign-in: codex app-server ended: app-server: boom") {
		t.Fatalf("garbage status: %+v", a)
	}
	fx.f.Remove("codex-broken")

	fx.f.Set("codex-account.json", accountstest.CodexAccountAPIKey)
	if a = fx.refresh(t, "codex"); !a.Connected || a.Account.Method != MethodAPIKey || a.LimitsNote != "Signed in with an API key: no plan limits" {
		t.Fatalf("api key: %+v", a)
	}
	fx.f.Set("codex-account.json", accountstest.CodexAccountNone)
	if a = fx.refresh(t, "codex"); a.Connected || a.Error != "" {
		t.Fatalf("signed out: %+v", a)
	}
}

func TestCodexAppServerDrainsStderr(t *testing.T) {
	fx := newFixture(t, nil, Codex)
	fx.f.Set("codex-stderr-after-eof", "")
	fx.f.Set("codex-login-status.txt", "garbage")
	a := fx.refresh(t, "codex")
	if a.Error != "Couldn't read Codex's sign-in: codex app-server ended: app-server: late diagnostic" {
		t.Fatalf("stderr diagnostic lost after stdout EOF: %s", a.Error)
	}
}

func TestAntigravityAccount(t *testing.T) {
	fx := newFixture(t, nil, Antigravity)
	a := fx.refresh(t, "antigravity")
	if !a.Connected || a.Account != (Identity{Email: "me@example.com", Plan: "Pro", Method: MethodSubscription}) || len(a.Limits) != 2 || a.Limits[1].UsedPercent != 6 {
		t.Fatalf("%+v", a)
	}
	log := fx.f.Log("agy")
	for _, want := range []string{"auth --help\n", "auth status\n", "-p /usage --output-format json\n"} {
		if !strings.Contains(log, want) {
			t.Fatalf("agy ran %q, not %q", log, want)
		}
	}
	// Signed out: /usage is not asked.
	fx.f.Set("agy-status.txt", accountstest.AgyStatusOut)
	runs := strings.Count(fx.f.Log("agy"), "-p /usage")
	if a = fx.refresh(t, "antigravity"); a.Connected || strings.Count(fx.f.Log("agy"), "-p /usage") != runs {
		t.Fatalf("signed out: %+v", a)
	}
	// An answer that is not a quota: noted, and not asked again.
	fx.f.Set("agy-status.txt", accountstest.AgyStatusIn)
	fx.f.Set("agy-usage.txt", `{"result":"Sure! Usage is a word."}`)
	if a = fx.refresh(t, "antigravity"); !a.Connected || len(a.Limits) != 0 || a.LimitsNote != agyNoQuota {
		t.Fatalf("no quota: %+v", a)
	}
	runs = strings.Count(fx.f.Log("agy"), "-p /usage")
	fx.f.Set("agy-usage.txt", accountstest.AgyQuota)
	if a = fx.refresh(t, "antigravity"); strings.Count(fx.f.Log("agy"), "-p /usage") != runs || a.LimitsNote != agyNoQuota {
		t.Fatalf("asked again: %+v", a)
	}
}

func TestAntigravityWithoutAuthCommand(t *testing.T) {
	fx := newFixture(t, nil, Antigravity)
	fx.f.Remove("agy-has-auth")
	a := fx.refresh(t, "antigravity")
	if !a.Connected || len(a.Limits) != 2 || a.Account.Method != MethodSubscription || a.Account.Email != "" {
		t.Fatalf("%+v", a)
	}
	if strings.Contains(fx.f.Log("agy"), "auth status") {
		t.Fatal("asked an agy without auth for its status")
	}
	if args := agyLogin(fx.s.ctx, &scope{Service: fx.s}, filepath.Join(fx.f.Dir, "agy")); args != nil {
		t.Fatalf("login args %q", args)
	}
	if args := agyLogout(fx.s.ctx, &scope{Service: fx.s}, filepath.Join(fx.f.Dir, "agy")); strings.Join(args, " ") != "-p /logout" {
		t.Fatalf("logout args %q", args)
	}
	fx.f.Set("agy-usage.txt", "Please sign in to continue")
	if a = fx.refresh(t, "antigravity"); a.Connected || a.Error != "" {
		t.Fatalf("signed out: %+v", a)
	}
}

// attach watches h's sign-in until want was printed.
func readUntil(t *testing.T, sub *LoginSub, seen *strings.Builder, want string) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for !strings.Contains(seen.String(), want) {
		select {
		case b, ok := <-sub.C():
			if !ok {
				t.Fatalf("ended before %q: %q", want, seen.String())
			}
			seen.Write(b)
		case <-timeout:
			t.Fatalf("no %q in %q", want, seen.String())
		}
	}
}

func TestLoginInATerminal(t *testing.T) {
	fx := newFixture(t, nil, Claude)
	fx.f.Set("claude-status.json", accountstest.ClaudeStatusOut)
	if a := fx.refresh(t, "claude"); a.Connected {
		t.Fatal("signed in already")
	}
	id, err := fx.s.Login("claude", "")
	if err != nil || id == "" {
		t.Fatal(err)
	}
	if again, err := fx.s.Login("claude", ""); err != nil || again != id {
		t.Fatalf("a second sign-in: %q %v", again, err)
	}
	if l := fx.acct("claude").Login; l.State != LoginRunning || l.Message == "" {
		t.Fatalf("login %+v", l)
	}
	sub, err := fx.s.AttachLogin("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	seen.Write(sub.Replay)
	readUntil(t, sub, &seen, "Opening your browser")
	if err := fx.s.LoginResize("claude", "", 80, 24); err != nil {
		t.Fatal(err)
	}
	if err := fx.s.LoginResize("claude", "", 0, 24); err == nil {
		t.Fatal("size 0 accepted")
	}
	if err := fx.s.LoginInput("claude", "", []byte("ok\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, sub, &seen, "got:ok")
	for range sub.C() { // closed when the sign-in ends
	}
	waitFor(t, "signed in", func() bool { return fx.acct("claude").Login.State == LoginSucceeded })
	a := fx.acct("claude")
	if !a.Connected || a.Login.Message != "Signed in as me@example.com" || len(a.Limits) != 3 {
		t.Fatalf("%+v", a)
	}
	if !fx.ev.seen("claude", func(a Account) bool { return a.Login.State == LoginRunning }) ||
		!fx.ev.seen("claude", func(a Account) bool { return a.Login.State == LoginSucceeded }) {
		t.Fatal("no events for the sign-in")
	}
	if !strings.Contains(fx.f.Log("claude"), "auth login\n") {
		t.Fatalf("claude ran %q", fx.f.Log("claude"))
	}
	// The last sign-in's screen can still be seen; it is over.
	late, err := fx.s.AttachLogin("claude", "")
	if err != nil || !strings.Contains(string(late.Replay), "got:ok") {
		t.Fatalf("late attach: %v %q", err, late.Replay)
	}
	if _, ok := <-late.C(); ok {
		t.Fatal("a finished sign-in still streams")
	}
	if err := fx.s.LoginInput("claude", "", []byte("x")); !errors.Is(err, ErrNoLogin) {
		t.Fatalf("input after the end: %v", err)
	}
	// A new sign-in is a new session.
	id2, err := fx.s.Login("claude", "")
	if err != nil || id2 == id {
		t.Fatalf("new session %q %v", id2, err)
	}
	if err := fx.s.CancelLogin("claude", ""); err != nil {
		t.Fatal(err)
	}
}

func TestLoginCancelFailTimeout(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.LoginTimeout = 700 * time.Millisecond }, Codex, Antigravity)
	if _, err := fx.s.Login("codex", ""); err != nil {
		t.Fatal(err)
	}
	if err := fx.s.CancelLogin("codex", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cancelled", func() bool { return fx.acct("codex").Login == Login{State: LoginIdle, Message: "Sign-in cancelled"} })

	if _, err := fx.s.Login("codex", ""); err != nil {
		t.Fatal(err)
	}
	sub, _ := fx.s.AttachLogin("codex", "")
	var seen strings.Builder
	readUntil(t, sub, &seen, "localhost:1455")
	if err := fx.s.LoginInput("codex", "", []byte("nope\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed", func() bool { return fx.acct("codex").Login.State == LoginFailed })
	if m := fx.acct("codex").Login.Message; m != "The sign-in ended with exit code 1" {
		t.Fatalf("message %q", m)
	}

	if _, err := fx.s.Login("antigravity", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "timed out", func() bool { return fx.acct("antigravity").Login.State == LoginFailed })
	if m := fx.acct("antigravity").Login.Message; !strings.Contains(m, "longer than 10 minutes") {
		t.Fatalf("message %q", m)
	}
	if !strings.Contains(fx.f.Log("agy"), "auth login\n") {
		t.Fatalf("agy ran %q", fx.f.Log("agy"))
	}
}

func TestLogout(t *testing.T) {
	fx := newFixture(t, nil, Claude, Codex, Antigravity)
	for _, h := range []string{"claude", "codex", "antigravity"} {
		if a := fx.refresh(t, h); !a.Connected {
			t.Fatalf("%s: %+v", h, a)
		}
		p, err := fx.s.Logout(t.Context(), h, "")
		if err != nil {
			t.Fatalf("%s: %v", h, err)
		}
		for _, a := range p.Accounts {
			if a.Harness == h && (a.Connected || a.Login.State != LoginIdle) {
				t.Fatalf("%s after sign-out: %+v", h, a)
			}
		}
	}
	for name, want := range map[string]string{"claude": "auth logout\n", "codex": "logout\n", "agy": "auth logout\n"} {
		if !strings.Contains(fx.f.Log(name), want) {
			t.Errorf("%s ran %q", name, fx.f.Log(name))
		}
	}
	if _, err := fx.s.Logout(t.Context(), "nope", ""); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
}

func TestRateLimited(t *testing.T) {
	fx := newFixture(t, nil, Claude)
	fx.refresh(t, "claude")
	n := fx.ev.count()
	fx.s.RateLimited(Claude, "", "five_hour", 1800000000)
	a := fx.acct("claude")
	if a.Limits[0].ID != "session" || a.Limits[0].UsedPercent != 100 || a.Limits[0].ResetsAt != 1800000000000 {
		t.Fatalf("%+v", a.Limits)
	}
	fx.s.RateLimited(Claude, "", "seven_day_opus", 0)
	a = fx.acct("claude")
	if l := a.Limits[len(a.Limits)-1]; l != (Limit{ID: "week_opus", Label: "Current week (Opus)", UsedPercent: 100}) {
		t.Fatalf("%+v", l)
	}
	if got := fx.ev.count() - n; got != 2 {
		t.Fatalf("%d events", got)
	}
	// Just checked: no new check for each event.
	if c := strings.Count(fx.f.Log("claude"), "auth status"); c != 1 {
		t.Fatalf("%d checks", c)
	}
}

// `claude auth login` keeps claude's start-queue turn until its process exits (it may write
// .claude.json until then), past the settle delay and the queue's own MaxHold; a sign-in past
// LoginTimeout is stopped first and then the queue moves on.
func TestClaudeLoginHoldsTheStartQueue(t *testing.T) {
	cfg := t.TempDir()
	q := claude.QueueFor(cfg)
	q.Settle, q.MaxHold = 20*time.Millisecond, 250*time.Millisecond
	fx := newFixture(t, func(o *Options) {
		o.Env = append(o.Env, claude.EnvConfigDir+"="+cfg)
		o.LoginTimeout = 1500 * time.Millisecond
	}, Claude)
	fx.f.Set("claude-status.json", accountstest.ClaudeStatusOut)
	blocked := func() bool { // a concurrent claude start waits for the turn
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		tk, err := q.Begin(ctx)
		if err != nil {
			return true
		}
		tk.Release()
		return false
	}

	if _, err := fx.s.Login("claude", ""); err != nil {
		t.Fatal(err)
	}
	sub, err := fx.s.AttachLogin("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	seen.Write(sub.Replay)
	readUntil(t, sub, &seen, "Opening your browser")
	time.Sleep(400 * time.Millisecond) // past Settle and MaxHold
	if !blocked() {
		t.Fatal("a running sign-in gave up its start-queue turn")
	}
	if err := fx.s.LoginInput("claude", "", []byte("ok\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "signed in", func() bool { return fx.acct("claude").Login.State == LoginSucceeded })
	if blocked() {
		t.Fatal("the turn was not released after the sign-in exited")
	}

	// Past LoginTimeout: stopped (fail closed), reported, and the queue moves on.
	if _, err := fx.s.Login("claude", ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if !blocked() {
		t.Fatal("a running sign-in gave up its start-queue turn")
	}
	waitFor(t, "timed out", func() bool { return fx.acct("claude").Login.State == LoginFailed })
	if m := fx.acct("claude").Login.Message; !strings.Contains(m, "longer than") {
		t.Fatalf("message %q", m)
	}
	if blocked() {
		t.Fatal("the queue is stuck behind a stopped sign-in")
	}
}
