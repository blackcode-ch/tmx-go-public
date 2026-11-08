// Package update keeps tmx current from its GitHub releases (DESIGN.md "Updates"): it checks
// the release channel, downloads this machine's file, verifies the release signature and the
// file's checksum, and swaps it in once no agent is in the middle of something, then restarts
// the daemon on the new binary.
package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/rollback"
)

// EvStatus is the bus event that carries Status whenever it changes.
const EvStatus = "update.status"

// State of the updater.
type State string

const (
	StateOff         State = "off"  // this build does not update itself (Reason says why)
	StateIdle        State = "idle" // nothing known yet, or up to date
	StateChecking    State = "checking"
	StateAvailable   State = "available"   // a newer release exists (auto off, or not started yet)
	StateDownloading State = "downloading" // fetching and verifying it
	StateReady       State = "ready"       // staged; installs once no agent is busy (Waiting)
	StateInstalling  State = "installing"  // swapping and restarting
	StateError       State = "error"
)

// Status is GET /api/update.
type Status struct {
	Current    string   `json:"current"`
	State      State    `json:"state"`
	Reason     string   `json:"reason,omitempty"` // StateOff: why
	Auto       bool     `json:"auto"`
	Channel    string   `json:"channel"`
	Repo       string   `json:"repo"`
	Latest     string   `json:"latest,omitempty"` // newest release of the channel
	LatestURL  string   `json:"latest_url,omitempty"`
	Available  bool     `json:"available"`         // Latest is newer than Current
	Waiting    []string `json:"waiting,omitempty"` // StateReady: agents it waits for
	Error      string   `json:"error,omitempty"`
	NeedsToken bool     `json:"needs_token,omitempty"` // the releases are not visible without a GitHub token
	HasToken   bool     `json:"has_token"`
	CheckedAt  int64    `json:"checked_at,omitempty"`
	// Probation: the installed version is still on its post-swap health probation (DESIGN.md
	// "Updates"); LastUpdate: how the last probation ended (confirmed, rolled back and why).
	Probation  string                    `json:"probation,omitempty"`
	DrillArmed bool                      `json:"drill_armed,omitempty"` // the next install fails its probation on purpose
	LastUpdate *rollback.ProbationResult `json:"last_update,omitempty"`
}

// Options configures an Updater.
type Options struct {
	Paths   config.Paths
	Version string // the running version (config.Version)
	// Settings returns config.json "update" (resolved); Save writes it back.
	Settings func() config.UpdateConfig
	Save     func(config.UpdateConfig) error
	// Executable is the running binary (os.Executable); Desktop: this build has the window.
	Executable string
	Desktop    bool
	// Busy names what an install must wait for (agents in a turn, approvals open); empty = go.
	Busy func() []string
	// Restart stops the daemon gracefully and starts it again from exe.
	Restart func(exe string)
	Publish func(model.BusEvent)
	Logger  *slog.Logger

	HTTP    *http.Client
	APIBase string   // default https://api.github.com
	Keys    []string // test-injected fleet keys (base64 Ed25519); nil = update.fleet_keys / fleet_public_key
	// Probe runs "<exe> version"; VerifyBundle checks a .app's code signature (tests replace them).
	Probe        func(ctx context.Context, exe string) (string, error)
	VerifyBundle func(ctx context.Context, bundle string) error
	GOOS, GOARCH string
	// FirstCheck is how long after start the first check runs (default 1 min); IdlePoll how often
	// a staged update looks for a quiet moment (default 30 s).
	FirstCheck, IdlePoll time.Duration
	// GH finds `gh auth token` (nil = the gh on PATH or in the usual places).
	GH func() string
}

// Updater is the update service. New, then Run in a goroutine.
type Updater struct {
	o Options

	mu       sync.Mutex
	st       Status
	rel      release
	ver      Version // of rel, for this channel
	staged   *staged
	busy     bool // a check or download is running
	installs chan bool
	target   target
	tgtErr   error

	// keysMu orders every writer of the update config (a rotation statement, the owner's re-pin,
	// Configure): each re-reads what it replaces under it, so none saves over a newer re-pin.
	keysMu      sync.Mutex
	keysFetched func() // test hook: after KEYS is fetched, before the pins are re-read

	want string // InstallTag: the one release checks and installs look for (under mu)

	tokMu  sync.Mutex
	ghTok  string
	ghWhen time.Time
}

func New(o Options) *Updater {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 10 * time.Minute}
	}
	if o.APIBase == "" {
		o.APIBase = "https://api.github.com"
	}
	if o.Probe == nil {
		o.Probe = probe
	}
	if o.VerifyBundle == nil {
		o.VerifyBundle = codesignVerify
	}
	if o.GOOS == "" {
		o.GOOS, o.GOARCH = runtime.GOOS, runtime.GOARCH
	}
	if o.FirstCheck <= 0 {
		o.FirstCheck = time.Minute
	}
	if o.IdlePoll <= 0 {
		o.IdlePoll = 30 * time.Second
	}
	if o.Publish == nil {
		o.Publish = func(model.BusEvent) {}
	}
	if o.Busy == nil {
		o.Busy = func() []string { return nil }
	}
	u := &Updater{o: o, installs: make(chan bool, 1)}
	u.target, u.tgtErr = detectTarget(o.Executable, o.Desktop, o.GOOS, o.GOARCH)
	u.st = Status{Current: o.Version, State: StateIdle}
	if reason := u.offReason(); reason != "" {
		u.st.State, u.st.Reason = StateOff, reason
		if strings.HasPrefix(reason, "update keys invalid") {
			u.o.Logger.Warn("update: off", "reason", reason)
		}
	}
	u.refreshSettingsLocked()
	return u
}

// offReason says why this build cannot update itself ("" = it can).
func (u *Updater) offReason() string {
	if _, ok := ParseVersion(u.o.Version); !ok {
		return "this is a development build (" + u.o.Version + "); it updates through make"
	}
	keys, err := u.trustedKeys()
	if err != nil {
		return err.Error()
	}
	if keys.empty() {
		return "this build has no release key, so it cannot check downloads; install a newer release by hand once"
	}
	if u.tgtErr != nil {
		return u.tgtErr.Error()
	}
	return ""
}

func (u *Updater) refreshSettingsLocked() {
	s := u.settings()
	u.st.Auto, u.st.Channel, u.st.Repo = *s.Auto, s.Channel, s.Repo
	u.st.HasToken = u.fileToken() != ""
}

func (u *Updater) settings() config.UpdateConfig {
	if u.o.Settings == nil {
		return (*config.UpdateConfig)(nil).Resolved()
	}
	s := u.o.Settings()
	return (&s).Resolved()
}

// Status returns the current status.
func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.refreshSettingsLocked()
	return u.copyLocked()
}

func (u *Updater) copyLocked() Status {
	s := u.st
	s.Waiting = append([]string(nil), s.Waiting...)
	dir := u.checkpoint()
	if p, ok, _ := rollback.OnProbation(dir); ok {
		s.Probation = p.To
	}
	if r, ok := rollback.LastProbation(dir); ok {
		s.LastUpdate = &r
	}
	s.DrillArmed = rollback.DrillArmed(dir)
	return s
}

// ArmDrill arms (or takes back) the forced-rollback drill: the next install by this updater is
// rolled back once its new binary is healthy (the API gates it).
func (u *Updater) ArmDrill(on bool) (Status, error) {
	var err error
	if on {
		err = rollback.ArmDrill(u.checkpoint())
	} else {
		err = rollback.DisarmDrill(u.checkpoint())
	}
	if err != nil {
		return u.Status(), err
	}
	u.mu.Lock()
	u.publishLocked()
	u.mu.Unlock()
	return u.Status(), nil
}

func (u *Updater) checkpoint() string { return filepath.Join(u.o.Paths.Home, "rollback") }

func (u *Updater) publishLocked() {
	u.o.Publish(model.BusEvent{Type: EvStatus, Data: u.copyLocked()})
}

func (u *Updater) set(f func(s *Status)) {
	u.mu.Lock()
	f(&u.st)
	u.publishLocked()
	u.mu.Unlock()
}

// Configure changes auto and/or the channel (config.json "update").
func (u *Updater) Configure(auto *bool, channel *string) (Status, error) {
	u.keysMu.Lock() // it saves the whole update config: never the pins from before a re-pin
	defer u.keysMu.Unlock()
	s := u.settings()
	if auto != nil {
		s.Auto = auto
	}
	if channel != nil {
		switch *channel {
		case config.ChannelStable, config.ChannelLatest:
			s.Channel = *channel
		default:
			return u.Status(), errors.New(`the channel is "stable" or "latest"`)
		}
	}
	if u.o.Save != nil {
		if err := u.o.Save(s); err != nil {
			return u.Status(), err
		}
	}
	u.mu.Lock()
	changedChannel := channel != nil && *channel != u.st.Channel
	u.refreshSettingsLocked()
	if changedChannel { // what was found or staged belongs to the other channel
		u.rel, u.ver = release{}, Version{}
		u.st.Latest, u.st.LatestURL, u.st.Available = "", "", false
		if u.staged != nil {
			os.RemoveAll(u.staged.cleanup)
			u.staged = nil
		}
		if u.st.State == StateAvailable || u.st.State == StateReady {
			u.st.State = StateIdle
		}
	}
	u.publishLocked()
	st := u.copyLocked()
	u.mu.Unlock()
	if changedChannel || (auto != nil && *auto) {
		go u.checkRelease(context.Background(), true)
	}
	return st, nil
}

// Check discovers releases only. The public check endpoint cannot initiate installs.
func (u *Updater) Check(ctx context.Context) Status { return u.checkRelease(ctx, false) }

// checkRelease may enqueue installation only from the scheduler or owner configuration.
func (u *Updater) checkRelease(ctx context.Context, allowAutoInstall bool) Status {
	u.mu.Lock()
	if u.st.State == StateOff {
		st := u.copyLocked()
		u.mu.Unlock()
		return st
	}
	if u.busy || u.st.State == StateInstalling {
		st := u.copyLocked()
		u.mu.Unlock()
		return st
	}
	u.busy = true
	prev := u.st.State
	u.st.State, u.st.Error = StateChecking, ""
	u.publishLocked()
	u.mu.Unlock()

	s := u.settings()
	u.mu.Lock()
	want := u.want
	u.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	r, err := u.findRelease(ctx, s.Repo, s.Channel, want)
	cancel()
	var ver Version
	if err == nil {
		ver, err = u.releaseVersion(r)
	}

	u.mu.Lock()
	u.busy = false
	u.st.CheckedAt = model.Now()
	u.st.NeedsToken = errors.Is(err, errNoAccess)
	if err != nil {
		u.st.State, u.st.Error = StateError, u.explain(err)
		u.o.Logger.Info("update: check failed", "err", err)
		u.publishLocked()
		st := u.copyLocked()
		u.mu.Unlock()
		return st
	}
	cur, _ := ParseVersion(u.o.Version)
	u.rel, u.ver = r, ver
	u.st.Latest, u.st.LatestURL = ver.String(), r.HTMLURL
	u.st.Available = cur.Less(ver)
	switch {
	case !u.st.Available:
		u.st.State = StateIdle
	case u.staged != nil && u.staged.version == ver.String():
		u.st.State = StateReady
	case prev == StateReady || prev == StateDownloading:
		u.st.State = prev
	default:
		u.st.State = StateAvailable
	}
	auto := allowAutoInstall && u.st.Auto && *s.Auto && u.st.Available && u.st.State == StateAvailable
	u.publishLocked()
	st := u.copyLocked()
	u.mu.Unlock()
	if st.Available {
		u.o.Logger.Info("update: a newer release", "current", u.o.Version, "latest", ver.String())
	}
	if auto {
		u.Install(false)
		return u.Status()
	}
	return st
}

// releaseVersion accepts only fleet tags, never versions inferred from filenames.
func (u *Updater) releaseVersion(r release) (Version, error) {
	if v, ok := ParseVersion(r.TagName); ok && !r.Draft {
		return v, nil
	}
	return Version{}, errors.New("release " + r.TagName + " is not a fleet release")
}

// explain turns an error into the sentence the owner sees.
func (u *Updater) explain(err error) string {
	if errors.Is(err, errNoAccess) {
		return "tmx cannot see the releases of " + u.settings().Repo + " (a private repository): sign in with the GitHub CLI (gh auth login) or give tmx a token (tmx update token)"
	}
	return err.Error()
}

// Install downloads and verifies the newest release (if not done yet) and installs it: now =
// at once, else once nothing is busy. It returns at once; Status follows the progress.
func (u *Updater) Install(now bool) {
	select {
	case u.installs <- now:
	default:
		if now { // a waiting install becomes immediate
			select {
			case <-u.installs:
			default:
			}
			u.installs <- now
		}
	}
}

// InstallTag installs exactly the release tag (a published fleet release on this channel: an rc
// only on "latest"), verified like any other, and only when it is newer than the running version
// (no downgrade). It checks at once, so an unknown, unpublished or older tag is refused here.
func (u *Updater) InstallTag(ctx context.Context, tag string, now bool) (Status, error) {
	if v, ok := ParseVersion(tag); !ok || v.Ahead != 0 {
		return u.Status(), fmt.Errorf("%q is not a fleet release tag (fleet-vX.Y.Z or fleet-vX.Y.Z-rcN)", tag)
	}
	u.mu.Lock()
	if u.want != "" && u.want != tag {
		// One pinned tag at a time: an owner's approval for one tag never installs another.
		pending := u.want
		u.mu.Unlock()
		return u.Status(), fmt.Errorf("an install of %s is already pending", pending)
	}
	u.want = tag
	u.mu.Unlock()
	st := u.checkRelease(ctx, false)
	switch {
	case st.State == StateOff:
		u.clearWant()
		return st, fmt.Errorf("updates are off: %s", st.Reason)
	case st.State == StateError:
		u.clearWant()
		return st, errors.New(st.Error)
	case st.Latest != tag:
		u.clearWant()
		return st, fmt.Errorf("%s is not the release found (%s)", tag, st.Latest)
	case !st.Available:
		u.clearWant()
		return st, fmt.Errorf("%s is not newer than the running %s", tag, st.Current)
	}
	u.Install(now)
	return st, nil
}

func (u *Updater) clearWant() {
	u.mu.Lock()
	u.want = ""
	u.mu.Unlock()
}

// Run checks at start (after FirstCheck) and every CheckHours, and carries out installs.
func (u *Updater) Run(ctx context.Context) {
	u.mu.Lock()
	off := u.st.State == StateOff
	u.mu.Unlock()
	if !off {
		cleanup(u.target)
	}
	next := time.NewTimer(u.o.FirstCheck)
	defer next.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-next.C:
			if *u.settings().Auto {
				u.checkRelease(ctx, true)
			}
			next.Reset(time.Duration(u.settings().CheckHours) * time.Hour)
		case now := <-u.installs:
			u.install(ctx, now)
		}
	}
}

// install stages the release (downloading and verifying), waits for a quiet moment unless now,
// then swaps it in and restarts.
func (u *Updater) install(ctx context.Context, now bool) {
	defer u.clearWant() // an InstallTag pin lasts for one install
	u.mu.Lock()
	if u.st.State == StateOff {
		u.mu.Unlock()
		return
	}
	if u.rel.TagName == "" || !u.st.Available {
		u.mu.Unlock()
		if u.Check(ctx).State == StateError {
			return
		}
		u.mu.Lock()
		if !u.st.Available {
			u.mu.Unlock()
			return
		}
	}
	r, ver, s := u.rel, u.ver, u.staged
	u.mu.Unlock()

	if s == nil || s.version != ver.String() {
		u.set(func(st *Status) { st.State, st.Error = StateDownloading, "" })
		dctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		got, err := u.stage(dctx, u.target, r, ver.String())
		cancel()
		if err != nil {
			u.o.Logger.Warn("update: download failed", "release", ver.String(), "err", err)
			u.set(func(st *Status) { st.State, st.Error = StateError, u.explain(err) })
			return
		}
		u.mu.Lock()
		if u.staged != nil && u.staged.cleanup != got.cleanup {
			os.RemoveAll(u.staged.cleanup)
		}
		u.staged = &got
		u.mu.Unlock()
		s = &got
		u.o.Logger.Info("update: downloaded and verified", "release", ver.String())
	}

	// Wait for a quiet moment: twice in a row nothing busy, IdlePoll/6 apart.
	for !now {
		busy := u.o.Busy()
		if len(busy) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(u.o.IdlePoll / 6):
			}
			if busy = u.o.Busy(); len(busy) == 0 {
				break
			}
		}
		u.set(func(st *Status) { st.State, st.Waiting = StateReady, busy })
		select {
		case <-ctx.Done():
			return
		case n := <-u.installs:
			now = now || n
		case <-time.After(u.o.IdlePoll):
		}
		u.mu.Lock()
		gone := u.staged == nil || u.staged.version != s.version
		u.mu.Unlock()
		if gone { // the channel changed meanwhile
			return
		}
	}

	u.set(func(st *Status) { st.State, st.Waiting = StateInstalling, nil })
	checkpoint := u.checkpoint()
	if err := rollback.Prepare(ctx, checkpoint, u.target.path, u.o.Paths.DB()); err != nil {
		u.set(func(st *Status) { st.State, st.Error = StateError, "cannot prepare rollback: "+err.Error() })
		return
	}
	if err := rollback.MarkUpdate(checkpoint); err != nil {
		u.set(func(st *Status) { st.State, st.Error = StateError, err.Error() })
		return
	}
	// The new binary must confirm itself healthy within rollback.ProbationWindow (cli/daemon.go).
	if err := rollback.BeginProbation(checkpoint, u.o.Version, s.version, time.Now()); err != nil {
		rollback.CompleteUpdate(checkpoint)
		u.set(func(st *Status) { st.State, st.Error = StateError, "cannot start the update's probation: "+err.Error() })
		return
	}
	exe, err := swap(u.target, *s)
	if err != nil {
		rollback.EndProbation(checkpoint)
		rollback.CompleteUpdate(checkpoint)
		u.o.Logger.Error("update: install failed", "release", s.version, "err", err)
		u.set(func(st *Status) { st.State, st.Error = StateError, err.Error() })
		return
	}
	u.mu.Lock()
	u.staged = nil
	u.mu.Unlock()
	u.o.Logger.Info("update: installed; restarting", "from", u.o.Version, "to", s.version, "exe", exe)
	if u.o.Restart != nil {
		u.o.Restart(exe)
	}
}

// ---- GitHub token (only for private repositories)

func (u *Updater) fileToken() string {
	b, err := os.ReadFile(u.o.Paths.GitHubTokenFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SetToken stores (or with "" removes) the GitHub token the updater uses.
func (u *Updater) SetToken(tok string) error {
	tok = strings.TrimSpace(tok)
	p := u.o.Paths.GitHubTokenFile()
	if tok == "" {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		if strings.ContainsAny(tok, " \t\r\n") || len(tok) > 512 {
			return errors.New("that does not look like a GitHub token")
		}
		// Not stored if it cannot be kept private; updates then use the env vars or gh instead.
		if err := config.WriteSecretFile(p, []byte(tok+"\n")); err != nil {
			return err
		}
	}
	u.mu.Lock()
	u.refreshSettingsLocked()
	u.st.NeedsToken = false
	u.publishLocked()
	u.mu.Unlock()
	return nil
}

// token: the stored token, else TMX_GITHUB_TOKEN / GH_TOKEN / GITHUB_TOKEN, else the GitHub
// CLI's (`gh auth token`, asked at most every 10 minutes).
func (u *Updater) token() string {
	// The public fleet source must also work on machines with stale private-repo
	// credentials; do not discover or transmit credentials for this source.
	if u.settings().Repo == config.PublicUpdateRepo {
		return ""
	}
	if t := u.fileToken(); t != "" {
		return t
	}
	for _, k := range []string{"TMX_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(k)); t != "" {
			return t
		}
	}
	u.tokMu.Lock()
	defer u.tokMu.Unlock()
	if time.Since(u.ghWhen) < 10*time.Minute {
		return u.ghTok
	}
	u.ghWhen = time.Now()
	if u.o.GH != nil {
		u.ghTok = u.o.GH()
	} else {
		u.ghTok = ghToken()
	}
	return u.ghTok
}

func ghToken() string {
	cands := []string{"gh"}
	switch runtime.GOOS {
	case "darwin":
		cands = append(cands, "/opt/homebrew/bin/gh", "/usr/local/bin/gh")
	case "windows":
		cands = append(cands, filepath.Join(os.Getenv("ProgramFiles"), "GitHub CLI", "gh.exe"))
	default:
		cands = append(cands, "/usr/bin/gh", "/usr/local/bin/gh", "/home/linuxbrew/.linuxbrew/bin/gh")
	}
	for _, c := range cands {
		p, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := hiddenexec.CommandContext(ctx, p, "auth", "token", "--hostname", "github.com")
		hideWindow(cmd)
		out, err := cmd.Output()
		cancel()
		if err == nil {
			if t := strings.TrimSpace(string(out)); t != "" && !strings.ContainsAny(t, " \n") {
				return t
			}
		}
	}
	return ""
}
