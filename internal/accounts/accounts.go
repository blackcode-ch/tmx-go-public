// Package accounts knows which AI accounts the owner has connected to the harnesses tmx runs
// (Claude Code, Codex, Antigravity, Gemini CLI) and how much of each plan's limits is used, as
// the harnesses themselves report it. It also runs each harness's own sign-in in a terminal and
// its own sign-out. It never reads a credential file or the keychain, and never keeps a token
// (Gemini CLI's google_accounts.json, which it reads, holds only addresses).
// See DESIGN.md "AI accounts and usage".
package accounts

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// EvUpdated is the bus event carrying the full Payload after every change: a refresh started
// or ended, a sign-in started, ended or was cancelled.
const EvUpdated = "accounts.updated"

// The harnesses, in display order.
const (
	Claude      model.Harness = model.HarnessClaude
	Codex       model.Harness = model.HarnessCodex
	Antigravity model.Harness = "antigravity"
	Gemini      model.Harness = model.HarnessGemini
)

// Sign-in states.
const (
	LoginIdle      = "idle"
	LoginRunning   = "running"
	LoginSucceeded = "succeeded"
	LoginFailed    = "failed"
)

// How the account is signed in.
const (
	MethodSubscription = "subscription"
	MethodAPIKey       = "api_key"
	MethodUnknown      = "unknown"
)

// Payload is GET /api/accounts and the accounts.updated event.
type Payload struct {
	Accounts  []Account `json:"accounts"`
	UpdatedAt int64     `json:"updated_at"`
}

// Account is one of a harness's accounts.
type Account struct {
	Harness string `json:"harness"`
	// Name is the account's name among the harness's ("default" = the harness's usual home).
	Name string `json:"name"`
	// LaunchDefault: agents of this harness launched without an account use this one.
	LaunchDefault bool     `json:"launch_default"`
	Label         string   `json:"label"`
	Installed     bool     `json:"installed"`
	Connected     bool     `json:"connected"`
	Account       Identity `json:"account"`
	// Limits are the plan's limits, as the harness reports them; empty with LimitsNote saying why.
	Limits     []Limit `json:"limits"`
	LimitsNote string  `json:"limits_note"`
	CheckedAt  int64   `json:"checked_at"` // Unix ms of the last check; 0 = never
	Refreshing bool    `json:"refreshing"`
	Error      string  `json:"error"` // the last check failed (what is shown is from before)
	Login      Login   `json:"login"`
	Install    string  `json:"install"` // how to install the harness
}

// Identity is who is signed in. Every field may be empty.
type Identity struct {
	Email  string `json:"email"`
	Plan   string `json:"plan"`   // human-cased: Max, Pro, Plus, Team, …
	Method string `json:"method"` // subscription | api_key | unknown
	Org    string `json:"org"`
}

// Limit is one quota window.
type Limit struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	UsedPercent float64 `json:"used_percent"` // 0–100
	ResetsAt    int64   `json:"resets_at"`    // Unix ms; 0 = unknown
	ResetsText  string  `json:"resets_text"`  // as the harness wrote it, if it did
}

// Login is the state of a harness's sign-in.
type Login struct {
	State   string `json:"state"` // idle | running | succeeded | failed
	Message string `json:"message"`
}

// Errors (the API maps them: unknown 404, not installed 409, no sign-in 404).
var (
	ErrUnknown      = errors.New("no such account: the harnesses are claude, codex, antigravity and gemini")
	ErrNotInstalled = errors.New("not installed")
	ErrNoLogin      = errors.New("no sign-in is running")
)

// Options configures the service.
type Options struct {
	// Dir is the neutral working folder the harnesses run in (created): never a project, so
	// no session files and no trust questions.
	Dir     string
	Publish func(model.BusEvent) // the accounts.updated event; nil = none
	Logger  *slog.Logger
	// Commands overrides a harness's executable (config.json claude_command).
	Commands map[model.Harness]string
	// LookPath finds a harness's executable; nil = harness.LookPath with Commands.
	LookPath func(h model.Harness) (string, error)
	// Env is added to the harnesses' environment (the daemon's CLAUDE_CONFIG_DIR; tests).
	Env []string
	// Profiles lists each harness's named accounts; nil = only "default".
	Profiles *Profiles

	Every        time.Duration // the periodic refresh (default 5 min; <0 = none)
	CheckTimeout time.Duration // each status command (default 20 s)
	UsageTimeout time.Duration // Claude's /usage (default 30 s)
	LoginTimeout time.Duration // a sign-in ends after this (default 10 min)
	StopGrace    time.Duration // a cancelled sign-in is killed after this (default 3 s)
	Now          func() time.Time
}

// Service holds the accounts' state.
type Service struct {
	o   Options
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	entries   []*entry
	updatedAt int64
	closed    bool
	started   bool

	agyMu sync.Mutex
	agy   agyProbe // what this agy is known to support (antigravity.go)
}

// entry is one account's state.
type entry struct {
	def  *def
	name string   // the account's name
	home []string // its HomeEnv
	acct Account  // the last check's result (Login, Refreshing filled in by Payload)

	refreshing bool
	again      bool      // a refresh was asked for while one ran: run once more
	applied    time.Time // when the check now shown started (an older one never overwrites it)

	loginMu sync.Mutex // one Login/Logout at a time
	login   Login
	sess    *loginSession // the running sign-in, or the last one (its replay)
}

// def is what tmx knows about one harness's account.
type def struct {
	h      model.Harness
	label  string
	check  func(ctx context.Context, s *scope, path string) result
	login  func(ctx context.Context, s *scope, path string) []string
	logout func(ctx context.Context, s *scope, path string) []string // nil args: no sign-out command
	// signedIn, if set, is polled while a sign-in runs: the sign-in stays open in the harness's
	// app after it (Gemini CLI), so it is stopped once this reports the account signed in.
	signedIn func(s *scope) bool
	// loginEnv is added to the sign-in's environment only.
	loginEnv []string
	install  string
}

// result is what a check found out.
type result struct {
	known     bool // the sign-in state was read; false = keep what was shown, with err
	connected bool
	id        Identity
	limits    []Limit
	note      string
	err       string
	// keepLimits: the limits could not be read this time; keep the ones shown for this account.
	keepLimits bool
}

// New returns the service; Start begins the refreshes.
func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Every == 0 {
		// Each check starts claude and a codex app-server: rare in the background; the Usage page
		// asks for a fresh one when its data is older than 5 minutes.
		o.Every = 15 * time.Minute
	}
	if o.CheckTimeout <= 0 {
		o.CheckTimeout = 20 * time.Second
	}
	if o.UsageTimeout <= 0 {
		o.UsageTimeout = 30 * time.Second
	}
	if o.LoginTimeout <= 0 {
		o.LoginTimeout = 10 * time.Minute
	}
	if o.StopGrace <= 0 {
		o.StopGrace = 3 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Dir == "" {
		o.Dir = os.TempDir()
	}
	s := &Service{o: o, log: o.Logger}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		s.log.Warn("accounts: working folder", "err", err)
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	for _, d := range defs() {
		for _, n := range s.names(d.h) {
			s.entries = append(s.entries, s.newEntry(d, n))
		}
	}
	s.updatedAt = s.now()
	return s
}

func (s *Service) names(h model.Harness) []string {
	if s.o.Profiles == nil {
		return []string{DefaultName}
	}
	return s.o.Profiles.Names(h)
}

func (s *Service) newEntry(d *def, name string) *entry {
	var home []string
	if s.o.Profiles != nil {
		home = s.o.Profiles.Env(d.h, name)
	}
	return &entry{def: d, name: name, home: home, login: Login{State: LoginIdle},
		acct: Account{Harness: string(d.h), Name: name, Label: d.label, Install: d.install, Account: Identity{Method: MethodUnknown}}}
}

// scope is e's view of the service.
func (s *Service) scope(e *entry) *scope { return &scope{Service: s, home: e.home} }

func defs() []*def {
	return []*def{
		{h: Claude, label: "Claude", check: claudeCheck, login: claudeLogin, logout: claudeLogout, install: installHint(Claude)},
		{h: Codex, label: "Codex", check: codexCheck, login: codexLogin, logout: codexLogout, install: installHint(Codex)},
		{h: Antigravity, label: "Antigravity", check: agyCheck, login: agyLogin, logout: agyLogout, install: installHint(Antigravity)},
		{h: Gemini, label: "Gemini CLI", check: geminiCheck, login: geminiLogin, logout: geminiLogout, install: installHint(Gemini),
			signedIn: geminiSignedIn, loginEnv: geminiLoginEnv},
	}
}

func (s *Service) now() int64 { return s.o.Now().UnixMilli() }

// Start refreshes every account in the background now and then every Options.Every. It never
// blocks.
func (s *Service) Start() {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		s.refreshAll()
		if s.o.Every < 0 {
			return
		}
		t := time.NewTicker(s.o.Every)
		defer t.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-t.C:
				s.refreshAll()
			}
		}
	}()
}

// Close stops the refreshes and any sign-in, and waits for them.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	var running []*loginSession
	for _, e := range s.entries {
		if e.sess != nil && !e.sess.ended() {
			running = append(running, e.sess)
		}
	}
	s.mu.Unlock()
	s.cancel()
	for _, ls := range running {
		ls.stop(s.o.StopGrace)
	}
	s.wg.Wait()
}

// entry is h's account name ("" = "default"; h "" = claude), or nil.
func (s *Service) entry(h, name string) *entry {
	if h == "" {
		h = string(Claude)
	}
	if name == "" {
		name = DefaultName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if string(e.def.h) == h && e.name == name {
			return e
		}
	}
	return nil
}

// Known reports whether h is one of the accounts' harnesses.
func Known(h string) bool {
	for _, d := range defs() {
		if string(d.h) == h {
			return true
		}
	}
	return false
}

// Payload is the accounts as they are now.
func (s *Service) Payload() Payload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.payloadLocked()
}

func (s *Service) payloadLocked() Payload {
	p := Payload{Accounts: make([]Account, 0, len(s.entries)), UpdatedAt: s.updatedAt}
	for _, e := range s.entries {
		a := e.acct
		a.Limits = append(make([]Limit, 0, len(e.acct.Limits)), e.acct.Limits...)
		a.Refreshing = e.refreshing
		a.Login = e.login
		a.Name = e.name
		a.LaunchDefault = e.name == s.launchDefault(e.def.h)
		p.Accounts = append(p.Accounts, a)
	}
	return p
}

// changedLocked notes a change; publish (without s.mu) sends it.
func (s *Service) changedLocked() { s.updatedAt = max(s.now(), s.updatedAt+1) }

func (s *Service) publish() {
	if s.o.Publish == nil {
		return
	}
	p := s.Payload()
	s.o.Publish(model.BusEvent{Type: EvUpdated, Data: p, At: model.Now()})
}

// lookPath finds h's executable: installed or not.
func (s *Service) lookPath(h model.Harness) (string, error) {
	if s.o.LookPath != nil {
		return s.o.LookPath(h)
	}
	if c := s.o.Commands[h]; c != "" {
		if st, err := os.Stat(c); err != nil || st.IsDir() {
			return "", ErrNotInstalled
		}
		return c, nil
	}
	return harness.SignInPath(h) // past a production stub (signin.go)
}

// Refresh checks h's account name again in the background (h "" = every account; name "" =
// every account of h). A refresh asked for while one runs for that account runs once more
// after it.
func (s *Service) Refresh(h, name string) error {
	if h == "" {
		s.refreshAll()
		return nil
	}
	if name == "" {
		if !Known(h) {
			return ErrUnknown
		}
		for _, e := range s.snapshot() {
			if string(e.def.h) == h {
				s.refresh(e)
			}
		}
		return nil
	}
	e := s.entry(h, name)
	if e == nil {
		return ErrUnknown
	}
	s.refresh(e)
	return nil
}

func (s *Service) refreshAll() {
	for _, e := range s.snapshot() {
		s.refresh(e)
	}
}

// snapshot is the entries now (Add and Remove replace the slice).
func (s *Service) snapshot() []*entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*entry(nil), s.entries...)
}

func (s *Service) refresh(e *entry) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if e.refreshing {
		e.again = true
		s.mu.Unlock()
		return
	}
	e.refreshing = true
	s.changedLocked()
	s.wg.Add(1)
	s.mu.Unlock()
	s.publish()
	go func() {
		defer s.wg.Done()
		for {
			s.checkAndApply(e)
			s.mu.Lock()
			if e.again && !s.closed {
				e.again = false
				s.mu.Unlock()
				continue
			}
			e.refreshing, e.again = false, false
			s.changedLocked()
			s.mu.Unlock()
			s.publish()
			return
		}
	}()
}

// checkAndApply runs h's check and shows its result, unless a check that started later was
// shown already. It does not publish.
func (s *Service) checkAndApply(e *entry) {
	start := s.o.Now()
	a := s.check(e)
	s.mu.Lock()
	defer s.mu.Unlock()
	if start.Before(e.applied) {
		return
	}
	e.applied = start
	e.acct = a
	s.changedLocked()
}

// check reads h's account now.
func (s *Service) check(e *entry) Account {
	s.mu.Lock()
	prev := e.acct
	s.mu.Unlock()
	a := Account{Harness: string(e.def.h), Name: e.name, Label: e.def.label, Install: e.def.install,
		Account: Identity{Method: MethodUnknown}, Limits: []Limit{}}
	path, err := s.lookPath(e.def.h)
	if err != nil {
		a.CheckedAt = s.now()
		return a
	}
	a.Installed = true
	if s.ctx.Err() != nil {
		return prev
	}
	r := e.def.check(s.ctx, s.scope(e), path)
	a.CheckedAt = s.now()
	if !r.known {
		// Keep what was shown, but say the check failed.
		prev.Installed, prev.CheckedAt, prev.Error = true, a.CheckedAt, r.err
		if prev.Limits == nil {
			prev.Limits = []Limit{}
		}
		return prev
	}
	a.Connected, a.Error = r.connected, r.err
	if r.connected {
		a.Account = r.id
		if a.Account.Method == "" {
			a.Account.Method = MethodUnknown
		}
		if r.limits != nil {
			a.Limits = r.limits
		} else if r.keepLimits && prev.Connected && prev.Account.Email == r.id.Email {
			a.Limits = append([]Limit{}, prev.Limits...)
		}
		a.LimitsNote = r.note
	}
	return a
}

// RateLimited marks a limit of h's account name ("" = "default") as reached at once (Claude's rate_limit_event with
// status "rejected" in a chat agent's stream), then checks the account again. kind is the
// harness's name for the window (five_hour, seven_day, …; "" = unknown); resetsAt is Unix
// seconds (0 = unknown). Extra usage ("overage") is not a plan limit and only triggers the check.
func (s *Service) RateLimited(h model.Harness, name, kind string, resetsAt int64) {
	e := s.entry(string(h), name)
	if e == nil {
		return
	}
	id, label := rateLimitWindow(kind)
	s.mu.Lock()
	// Agents repeat the event every turn while the limit holds: check at most once a minute.
	recent := e.refreshing || s.now()-e.acct.CheckedAt < time.Minute.Milliseconds()
	if id == "" || !e.acct.Connected || s.closed {
		s.mu.Unlock()
		if !recent {
			s.refresh(e)
		}
		return
	}
	limits := append([]Limit(nil), e.acct.Limits...)
	found, changed := false, false
	for i := range limits {
		if limits[i].ID != id {
			continue
		}
		found = true
		if limits[i].UsedPercent != 100 || (resetsAt > 0 && limits[i].ResetsAt != resetsAt*1000) {
			limits[i].UsedPercent, changed = 100, true
			if resetsAt > 0 {
				limits[i].ResetsAt = resetsAt * 1000
			}
		}
	}
	if !found {
		l := Limit{ID: id, Label: label, UsedPercent: 100}
		if resetsAt > 0 {
			l.ResetsAt = resetsAt * 1000
		}
		limits, changed = append(limits, l), true
	}
	if changed {
		e.acct.Limits = limits
		s.changedLocked()
	}
	s.mu.Unlock()
	if changed {
		s.publish()
	}
	if !recent {
		s.refresh(e)
	}
}

// rateLimitWindow names a Claude rate-limit type as /usage names its limits ("" = none).
func rateLimitWindow(kind string) (id, label string) {
	switch kind {
	case "five_hour", "":
		return "session", "Current session"
	case "seven_day":
		return "week", "Current week (all models)"
	case "seven_day_opus":
		return "week_opus", "Current week (Opus)"
	case "seven_day_sonnet":
		return "week_sonnet", "Current week (Sonnet)"
	case "overage":
		return "", ""
	}
	return slug(kind), kind
}
