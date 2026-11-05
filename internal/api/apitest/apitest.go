// Package apitest runs a real daemon (fleet + API) on fake runners behind httptest, in a
// throwaway TMXG_HOME, for tests of the API and of its clients (CLI, MCP).
package apitest

import (
	"context"
	"github.com/blackcode-ch/tmx-go/internal/uploads"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/accounts"
	"github.com/blackcode-ch/tmx-go/internal/accounts/accountstest"
	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/chat"
	"github.com/blackcode-ch/tmx-go/internal/companion"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/peers"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
	"github.com/blackcode-ch/tmx-go/internal/recworker"
	"github.com/blackcode-ch/tmx-go/internal/resources"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/fake"
	"github.com/blackcode-ch/tmx-go/internal/slots"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/summarize"
	"github.com/blackcode-ch/tmx-go/internal/summarize/summarizetest"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
	"github.com/blackcode-ch/tmx-go/internal/update"
	"github.com/blackcode-ch/tmx-go/internal/whatsapp"
	"github.com/blackcode-ch/tmx-go/internal/whatsapp/watest"
)

type Daemon struct {
	URL        string
	OwnerToken string
	Fleet      *fleet.Fleet
	Chat, Term *fake.Runner
	Workspace  string
	Server     *httptest.Server
	// Resource control is wired for its API but never samples (fake PIDs are not real).
	Resources *resources.Manager
	Slots     *slots.Manager
	Gate      *gate.Gate // off until a test turns it on; prompts go to GateAuth. Its grace window is off (every proof is for one request) until a test sets it
	GateAuth  *gate.Fake
	Home      string
	Chats     *chat.Service // the chat layer; without a node (New) peer-to-peer is off
	// Companion is the Companion service; it finds Companion through config.json companion.url
	// (a test points it at a fake) and installs into AIOSHome (a temp dir, never ~/.aios).
	Companion *companion.Service
	AIOSHome  string
	Uploads   *uploads.Store
	// Voice is voice transcription; it takes recordings from <home>/recordings and keeps its
	// settings in memory. Nothing is installed: transcribetest.Install adds a fake.
	Voice    *transcribe.Service
	voiceMu  sync.Mutex
	voice    config.VoiceConfig
	Recorder *recorder.Service // recordings; its clock and disk are real (StaleAfter as usual)
	// Summaries of recordings; Claude is summarizetest.Path(Home), which summarizetest.Install
	// fills with a fake (never the real claude). Automatic summaries and automatic transcription
	// start off here; SetSummaries and PUT /api/voice turn them on.
	Summaries *summarize.Service
	sumMu     sync.Mutex
	sum       config.SummaryConfig
	// WhatsApp runs on a fake connection (nothing linked: WaFake.Scan links it, LinkWhatsApp
	// does both); its settings live in memory.
	WhatsApp *whatsapp.Service
	WaFake   *watest.Fake
	waMu     sync.Mutex
	waCfg    config.WhatsAppConfig

	// Accounts finds the harnesses only in AccountsBin (none at first: accountstest.Install
	// puts fakes there), never the real ones; it does not refresh by itself, and Codex's home is
	// <home>/codex.
	Accounts    *accounts.Service
	AccountsBin string

	// Links the daemon would open on this machine (Companion's connect link) are recorded, never
	// opened: Opened lists them, OnOpen plays the app that would answer.
	openMu sync.Mutex
	opened []string
	onOpen func(url string) error

	apiOpts api.Options // what Server serves; WithUpdater serves it once more with an updater
	updMu   sync.Mutex
	upd     config.UpdateConfig
}

// WithUpdater serves this daemon's API once more, now with an update service (Server has none:
// its update routes answer 501), and returns that server's URL and the updater. The updater is
// a development build, so it never checks GitHub or installs anything (state off); it keeps
// config.json "update" in memory (UpdateSettings) and its token in this daemon's secrets.
func (d *Daemon) WithUpdater(t testing.TB) (string, *update.Updater) {
	t.Helper()
	u := update.New(update.Options{Paths: config.Paths{Home: d.Home}, Version: "test", APIBase: "http://127.0.0.1:1",
		Settings: func() config.UpdateConfig { d.updMu.Lock(); defer d.updMu.Unlock(); return d.upd },
		Save:     func(c config.UpdateConfig) error { d.updMu.Lock(); defer d.updMu.Unlock(); d.upd = c; return nil },
		Publish:  d.Fleet.Publish})
	o := d.apiOpts
	o.Updater = u
	srv := httptest.NewServer(api.New(o).Handler())
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	return srv.URL, u
}

// WithRecWorker serves this daemon's API once more with the recorder's report worker (internal/recworker) as the
// daemon wires it: the recorder's Finished hook launches the worker. It returns that server's URL and the service.
func (d *Daemon) WithRecWorker(t testing.TB, o recworker.Options) (string, *recworker.Service) {
	t.Helper()
	o.Fleet, o.Summaries, o.Recordings = d.Fleet, d.Summaries, d.Recorder
	if o.Exe == "" {
		o.Exe = "tmx"
	}
	if o.Dir == "" {
		o.Dir = filepath.Join(d.Home, "rec-workers")
	}
	w := recworker.New(o)
	w.Start()
	d.Recorder.SetFinished(w.Finished)
	a := d.apiOpts
	a.RecWorker = w
	srv := httptest.NewServer(api.New(a).Handler())
	t.Cleanup(func() {
		w.Close()
		d.Recorder.SetFinished(d.Summaries.Finished)
		srv.CloseClientConnections()
		srv.Close()
	})
	return srv.URL, w
}

// WithPeers serves this daemon's API once more with the Python fleet interop (peers.go), the
// fleet sending peer mail through pc, and returns that server's URL.
func (d *Daemon) WithPeers(t testing.TB, pc *peers.Client) string {
	t.Helper()
	d.Fleet.SetPeerSender(pc, pc.Machine())
	o := d.apiOpts
	o.Peers = pc
	srv := httptest.NewServer(api.New(o).Handler())
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	return srv.URL
}

// UpdateSettings is config.json "update" as the updater of WithUpdater saved it.
func (d *Daemon) UpdateSettings() config.UpdateConfig {
	d.updMu.Lock()
	defer d.updMu.Unlock()
	return d.upd
}

// SetUpdateSettings replaces config.json "update" as the updater of WithUpdater reads it.
func (d *Daemon) SetUpdateSettings(c config.UpdateConfig) {
	d.updMu.Lock()
	defer d.updMu.Unlock()
	d.upd = c
}

// New starts a daemon; everything is torn down with the test. opts adjust the fleet's
// options before it is created (e.g. a fake process table for discover/adopt).
func New(t testing.TB, opts ...func(*fleet.Options)) *Daemon { return NewWithNode(t, nil, opts...) }

// NewWithNode starts a daemon whose chat layer uses node (e.g. a p2ptest.Fake; nil = off).
func NewWithNode(t testing.TB, node p2p.Node, opts ...func(*fleet.Options)) *Daemon {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	paths := config.Paths{Home: home}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	token, err := config.OwnerToken(paths)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{OwnerToken: token, Chat: fake.New(), Term: fake.New(), Workspace: filepath.Join(home, "Projects"),
		GateAuth: &gate.Fake{}, Home: home}
	exe := filepath.Join(home, "bin", "tmx") // absolute on every platform
	d.Gate = gate.New(paths, d.GateAuth, nil, gate.FileMarker(paths.Home+"/test-marker"))
	// Most API fixtures deliberately start with an explicitly disabled gate.
	if err := d.Gate.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := d.Gate.SetGrace(0); err != nil {
		t.Fatal(err)
	}
	d.Slots = slots.New(slots.Options{})
	d.Resources = resources.New(resources.Options{Paths: paths, Slots: d.Slots, Workspace: d.Workspace})
	srv := httptest.NewUnstartedServer(nil) // the listener exists already; start once wired
	d.Server, d.URL = srv, "http://"+srv.Listener.Addr().String()
	fo := fleet.Options{Elevated: func() bool { return false },
		Store: st, Paths: paths,
		Runners: map[model.Mode]runner.Runner{model.ModeChat: d.Chat, model.ModeTerminal: d.Term},
		Machine: "testbox", Version: "test", Workspace: d.Workspace, URL: d.URL, Executable: exe,
		Resources: d.Resources,
	}
	for _, o := range opts {
		o(&fo)
	}
	f, err := fleet.New(fo)
	if err != nil {
		t.Fatal(err)
	}
	d.Fleet = f
	d.Resources.Attach(f)
	d.Chats, err = chat.New(chat.Options{Node: node, Store: st, Agents: f, Name: func() string { return "testbox" }, FilesDir: paths.ChatFilesDir(),
		RetryMin: 30 * time.Millisecond, RetryMax: 150 * time.Millisecond, SendWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	d.AIOSHome = filepath.Join(home, "aios")
	t.Setenv(companion.EnvAIOSHome, d.AIOSHome)
	t.Setenv("TMXG_COMPANION_DATA", filepath.Join(home, "companion-data")) // never the real Companion's files
	d.Companion = companion.New(companion.Options{HTTP: http.DefaultClient, Store: st, Fleet: f, Paths: paths, Version: "test",
		Install: companion.InstallOptions{AIOSHome: d.AIOSHome, Executable: exe, URL: d.URL, StateHome: home},
		Open:    d.open,
		Poll:    20 * time.Millisecond, RetryMin: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond})
	f.SetCompanion(d.Companion)
	f.SetCompanionMessenger(d.Companion.SendHeadMessage)
	d.Uploads = uploads.New(paths)
	d.Recorder = recorder.New(recorder.Options{Store: st, Dir: paths.RecordingsDir(), Publish: f.Publish,
		DiskFree: func(string) (uint64, error) { return 1 << 40, nil }})
	if err := d.Recorder.Start(); err != nil {
		t.Fatal(err)
	}
	off := false
	d.voice = config.VoiceConfig{AutoTranscribe: &off}
	d.sum = config.SummaryConfig{Auto: &off}
	d.Voice = transcribe.New(transcribe.Options{Paths: paths, Store: st, Slots: d.Slots, Publish: f.Publish, NoLowPriority: true,
		RecordingsDir: paths.RecordingsDir(), Version: "test",
		Settings: func() config.VoiceConfig { d.voiceMu.Lock(); defer d.voiceMu.Unlock(); return d.voice },
		Save:     func(c config.VoiceConfig) error { d.voiceMu.Lock(); defer d.voiceMu.Unlock(); d.voice = c; return nil }})
	d.Recorder.SetTranscriber(d.Voice)
	d.Summaries = summarize.New(summarize.Options{Store: st, Recordings: d.Recorder, Voice: d.Voice, Bus: f, Publish: f.Publish,
		Claude: summarizetest.Path(home), ClaudeConfigDir: paths.ClaudeConfigDir(), NoLowPriority: true, CheckEvery: 50 * time.Millisecond,
		Settings: func() config.SummaryConfig {
			d.sumMu.Lock()
			defer d.sumMu.Unlock()
			c := d.sum
			if c.Backend == "" { // the test daemons summarise with the fake claude (summarizetest); production defaults to local
				c.Backend = "claude"
			}
			return c
		},
		Save: func(c config.SummaryConfig) error { d.sumMu.Lock(); defer d.sumMu.Unlock(); d.sum = c; return nil }})
	d.Recorder.SetFinished(d.Summaries.Finished)
	d.WaFake = watest.New()
	d.WhatsApp = whatsapp.New(whatsapp.Options{Store: st, Client: d.WaFake, MediaDir: paths.WhatsAppMediaDir(), Publish: f.Publish,
		Fleet: f, ChatGap: 10 * time.Millisecond, NoticeQuiet: 30 * time.Millisecond,
		Config: func() *config.WhatsAppConfig { d.waMu.Lock(); defer d.waMu.Unlock(); c := d.waCfg; return &c },
		SaveConfig: func(fn func(*config.WhatsAppConfig)) error {
			d.waMu.Lock()
			defer d.waMu.Unlock()
			fn(&d.waCfg)
			return nil
		}})
	f.SetWhatsApp(d.WhatsApp)
	d.AccountsBin = filepath.Join(home, "accounts-bin")
	d.Accounts = accounts.New(accounts.Options{Dir: filepath.Join(home, "accounts"), Publish: f.Publish,
		LookPath: accountstest.LookPathIn(d.AccountsBin), Every: -1, CheckTimeout: 5 * time.Second, UsageTimeout: 5 * time.Second,
		StopGrace: 500 * time.Millisecond, Env: []string{"CODEX_HOME=" + filepath.Join(home, "codex"), "CLAUDE_CONFIG_DIR=" + filepath.Join(home, "claude-config"), "GEMINI_CLI_HOME=" + filepath.Join(home, "gemini")}})
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	d.apiOpts = api.Options{Fleet: f, OwnerToken: token, Port: port, Gate: d.Gate,
		Resources: d.Resources, Slots: d.Slots, Chats: d.Chats, Companion: d.Companion, Uploads: d.Uploads, P2P: node, Voice: d.Voice, Recorder: d.Recorder,
		Summaries: d.Summaries, WhatsApp: d.WhatsApp, Accounts: d.Accounts}
	srv.Config.Handler = api.New(d.apiOpts).Handler()
	srv.Start()
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	if err := d.Chats.Start(); err != nil {
		t.Fatal(err)
	}
	if err := d.Companion.Start(); err != nil {
		t.Fatal(err)
	}
	if err := d.Voice.Start(); err != nil {
		t.Fatal(err)
	}
	if err := d.Summaries.Start(); err != nil {
		t.Fatal(err)
	}
	if err := d.WhatsApp.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Accounts.Close()
		d.Summaries.Close()
		d.WhatsApp.Close()
		d.Voice.Close()
		d.Companion.Close()
		d.Recorder.Close()
		srv.CloseClientConnections()
		srv.Close()
		d.Chats.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		f.Close(ctx)
		st.Close()
	})
	return d
}

func (d *Daemon) open(_ context.Context, url string) error {
	d.openMu.Lock()
	d.opened = append(d.opened, url)
	fn := d.onOpen
	d.openMu.Unlock()
	if fn != nil {
		return fn(url)
	}
	return nil
}

// Opened lists the links the daemon handed to the OS, oldest first (none was opened).
func (d *Daemon) Opened() []string {
	d.openMu.Lock()
	defer d.openMu.Unlock()
	return append([]string(nil), d.opened...)
}

// OnOpen runs fn for every link the daemon hands to the OS (an error: the link did not open).
func (d *Daemon) OnOpen(fn func(url string) error) {
	d.openMu.Lock()
	d.onOpen = fn
	d.openMu.Unlock()
}

// SetSummaries sets config.json "summaries" of this daemon.
func (d *Daemon) SetSummaries(c config.SummaryConfig) {
	d.sumMu.Lock()
	d.sum = c
	d.sumMu.Unlock()
}

// AgentToken returns the token the named agent's current process was given.
func (d *Daemon) AgentToken(t testing.TB, name string) string {
	t.Helper()
	a, err := d.Fleet.Agent(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*fake.Runner{d.Chat, d.Term} {
		if p := r.Last(a.ID); p != nil {
			for _, kv := range p.Spec.Env {
				if tok, ok := strings.CutPrefix(kv, fleet.EnvToken+"="); ok {
					return tok
				}
			}
		}
	}
	t.Fatalf("no process/token for %s", name)
	return ""
}

// Proc returns the named agent's newest fake process.
func (d *Daemon) Proc(t testing.TB, name string) *fake.Process {
	t.Helper()
	a, err := d.Fleet.Agent(name)
	if err != nil {
		t.Fatal(err)
	}
	if p := d.Chat.Last(a.ID); p != nil {
		return p
	}
	if p := d.Term.Last(a.ID); p != nil {
		return p
	}
	t.Fatalf("no process for %s", name)
	return nil
}

// EnableWhatsApp explicitly opts a test into the dormant WhatsApp protocol using
// the fake client only. It changes fixture memory, never production config;
// config.Load continues to force WhatsApp off in the trial profile.
func (d *Daemon) EnableWhatsApp(t testing.TB) {
	t.Helper()
	d.waMu.Lock()
	on := true
	d.waCfg.Enabled = &on
	d.waMu.Unlock()
	if err := d.WhatsApp.Start(); err != nil {
		t.Fatal(err)
	}
}

// LinkWhatsApp links the fake WhatsApp account (watest.Self) and waits until it is connected.
func (d *Daemon) LinkWhatsApp(t testing.TB) {
	t.Helper()
	if err := d.WhatsApp.Link("qr", ""); err != nil {
		t.Fatal(err)
	}
	d.WaFake.Scan(watest.Self)
	deadline := time.Now().Add(5 * time.Second)
	for d.WhatsApp.Status().State != model.WaConnected {
		if time.Now().After(deadline) {
			t.Fatal("WhatsApp did not connect")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
