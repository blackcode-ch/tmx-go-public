package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/desktop"
	"github.com/blackcode-ch/tmx-go/internal/transfers"
	"github.com/blackcode-ch/tmx-go/internal/update"
	"github.com/blackcode-ch/tmx-go/internal/uploads"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/accounts"
	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/chat"
	"github.com/blackcode-ch/tmx-go/internal/claudeext"
	"github.com/blackcode-ch/tmx-go/internal/companion"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/jobs"
	"github.com/blackcode-ch/tmx-go/internal/machinehealth"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/peers"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
	"github.com/blackcode-ch/tmx-go/internal/recworker"
	"github.com/blackcode-ch/tmx-go/internal/reshistory"
	"github.com/blackcode-ch/tmx-go/internal/resources"
	"github.com/blackcode-ch/tmx-go/internal/restartguard"
	"github.com/blackcode-ch/tmx-go/internal/rollback"
	"github.com/blackcode-ch/tmx-go/internal/rules"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
	"github.com/blackcode-ch/tmx-go/internal/teamstore"
)

// cmdDaemon runs the daemon in the foreground until SIGINT/SIGTERM or POST /api/shutdown.
func cmdDaemon(e *env, args []string) (err error) {
	flags := newFlags("daemon")
	home := flags.String("home", "", "existing daemon data directory")
	if _, err := parse(flags, args); err != nil {
		return err
	}
	paths := e.paths
	if !e.loaded {
		if *home != "" {
			if !filepath.IsAbs(*home) {
				return errors.New("daemon --home requires an absolute existing data directory")
			}
			st, statErr := os.Stat(*home)
			if statErr != nil || !st.IsDir() {
				return fmt.Errorf("daemon data directory %q is unavailable", *home)
			}
			paths = config.Paths{Home: filepath.Clean(*home)}
			if err := paths.Ensure(); err != nil {
				return err
			}
		} else {
			paths, err = config.Resolve()
			if err != nil {
				return err
			}
		}
	}
	// After an update the daemon starts its successor from the new binary. This deferred call runs
	// last: once the agents are stopped, the store is closed and the lock is released.
	var restartExe, hostExe string
	defer func() {
		if restartExe != "" && err == nil {
			err = handoffDaemon(e, restartExe, hostExe)
		}
	}()
	if *home != "" {
		previous, inherited := os.LookupEnv(config.EnvHome)
		if err := os.Setenv(config.EnvHome, paths.Home); err != nil {
			return err
		}
		defer func() {
			if inherited {
				_ = os.Setenv(config.EnvHome, previous)
			} else {
				_ = os.Unsetenv(config.EnvHome)
			}
		}()
	}
	logFile, err := os.OpenFile(paths.DaemonLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(&daemonLogWriter{file: logFile, stderr: e.errw}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	defer func() {
		if err != nil {
			log.Error("tmx daemon exited with an error", "pid", os.Getpid(), "err", err)
		}
	}()
	log.Info("tmx daemon starting", "pid", os.Getpid(), "home", paths.Home, "version", config.Version)
	lock, err := config.WaitLock(paths, 15*time.Second)
	if err != nil {
		return err
	}
	defer lock.Release()
	if !e.loaded {
		if err := e.loadPaths(paths); err != nil {
			return err
		}
	}
	for _, warning := range e.cfg.Warnings {
		log.Warn(warning)
	}

	token, err := config.OwnerToken(e.paths)
	if err != nil {
		return err
	}
	currentExe, err := os.Executable()
	if err != nil {
		return err
	}
	// Session hosts and the Windows restart helper need an executable that
	// survives replacement or rollback of the installed app.
	hostSource := currentExe
	if real, err := filepath.EvalSymlinks(currentExe); err == nil {
		hostSource = real
	}
	hostExe, err = hostExecutable(e.paths.Home, hostSource)
	if err != nil {
		log.Warn("session hosts and restart helper: cannot copy executable", "err", err)
	}
	checkpoint := filepath.Join(e.paths.Home, "rollback")
	st, err := store.OpenRecoverable(e.paths.DB(), installationPath(currentExe), checkpoint)
	if errors.Is(err, rollback.ErrRolledBack) {
		// The update failed its probation and the previous binary is back in place: start it.
		log.Error("tmx: update rolled back; starting the previous version", "err", err)
		restartExe = currentExe
		return nil
	}
	if err != nil {
		return err
	}
	defer st.Close()
	ln, err := api.Listen(e.cfg.Port) // before anything starts, so a busy port fails fast
	if err != nil {
		return fmt.Errorf("listen on 127.0.0.1:%d: %w", e.cfg.Port, err)
	}

	sl, rm := newResourceControl(e, log)
	var node p2p.Node // stays a nil interface when p2p is off
	host := startP2P(paths, e.cfg, log)
	if host != nil {
		node = host
		defer host.Close()
	}
	// Each harness's named accounts, with their private homes (internal/accounts profiles.go):
	// an agent runs as one of them. Never in a project or in git.
	profiles, err := accounts.OpenProfiles(filepath.Join(paths.Home, "ai-accounts"))
	if err != nil {
		return err
	}
	if !e.cfg.UseUserAccounts(paths.Home) {
		profiles.IsolateDefaults(paths.Home)
	}
	// The owner's Claude Code MCP servers and plugins, for profiles that add some.
	cext := claudeext.New(claudeext.Options{Env: claude.BaseEnv(), Claude: func() (string, error) {
		return claude.LookPath(runner.Spec{Command: e.cfg.ClaudeCommand})
	}})
	// The fleet's rules source (internal/rules): made before the fleet, which opens every agent's
	// instructions with it; the head's line and the GitHub token are bound once those exist.
	var rulesNotify func(rules.Event)
	var rulesToken func() string
	rulesSvc := rules.New(rules.Options{Dir: filepath.Join(paths.Home, "rules"), Logger: log,
		Settings: rulesSettings(paths, e.cfg),
		Token: func() string {
			if rulesToken == nil {
				return ""
			}
			return rulesToken()
		},
		Notify: func(ev rules.Event) {
			if rulesNotify != nil {
				rulesNotify(ev)
			}
		}})
	defer rulesSvc.Close()
	var accts *accounts.Service // assigned before the updater starts
	f, err := fleet.New(fleet.Options{
		ClaudeExt:      cext,
		Rules:          rulesSvc,
		Tasks:          e.cfg.Tasks,
		HostExecutable: hostExe,
		Accounts:       profiles,
		ResetInFlight: func(now time.Time) bool {
			return accts != nil && accts.ResetInFlight(now)
		},
		AutoShelve:           e.cfg.AutoShelve,
		Supervisor:           e.cfg.Supervisor,
		Watch:                e.cfg.Watch,
		FaeRuntimeContextCap: e.cfg.FaeRuntimeContextCap,
		Store:                st, Paths: paths, Runners: runners(),
		Machine: e.cfg.Machine, Version: config.Version, Workspace: e.cfg.Workspace,
		FleetAuthorities: e.cfg.FleetAuthorities, FleetLead: e.cfg.FleetLead, Teammates: e.cfg.Teammates, AgentBypass: e.cfg.AgentBypass, AgentMachine: e.cfg.AgentMachine,
		URL: e.cfg.URL(), ClaudeCommand: e.cfg.ClaudeCommand, HeadHarness: e.cfg.HeadHarness, UIProfile: e.cfg.UIProfile(),
		Policy: e.cfg.EffectivePolicy(), // seeds the built-in profiles on first start only
		Guard:  e.cfg.Guard,
		SaveGuard: func(g guard.Settings) error {
			c, err := config.Load(paths)
			if err != nil {
				return err
			}
			c.Guard = &g
			return config.Save(paths, c)
		},
		P2P:    node,
		Logger: log,

		Resources: rm, // resource control: launch queue, priority, brakes
	})
	if err != nil {
		ln.Close()
		return err
	}
	rulesNotify = f.RulesEvent
	attachResourceControl(f, sl, rm)
	jobWorkers, jobErr := jobs.OpenFailSafe(jobs.Options{Path: filepath.Join(paths.Home, "jobs.json"), Fresh: rm.Fresh})
	if jobErr != nil {
		log.Error("job workers disabled; control plane remains available", "err", jobErr)
	}
	defer jobWorkers.Close()

	// Chats with other tmx installs (DESIGN.md "Chats"). Without a node peer-to-peer is off
	// and the chat routes answer 503.
	// p2p.debug_drain reads the node's channels itself (testing the network layer alone), so
	// the chat layer must not: chats then run without a node.
	drain := node != nil && e.cfg.P2P != nil && e.cfg.P2P.DebugDrain
	chatNode := node
	if drain {
		log.Warn("p2p.debug_drain is on: chats are off while the network log reads the node")
		chatNode = nil
	}
	machine := e.cfg.Machine
	chats, err := chat.New(chat.Options{Node: chatNode, Store: st, Agents: f, Logger: log, FilesDir: paths.ChatFilesDir(),
		TeamStore: teamstore.New(paths), // team status: each computer's report to its group's admin, after its owner opted in
		Jobs:      jobWorkers, FileMaxBytes: e.cfg.P2P.FileLimit(),
		Name: func() string { // the name people see: the node's display name, else the machine
			if chatNode != nil {
				if n := chatNode.DisplayName(); n != "" {
					return n
				}
			}
			return machine
		}})
	if err != nil {
		ln.Close()
		return err
	}

	// Companion, the owner's desktop agent app (DESIGN.md "Companion").
	var comp *companion.Service
	if e.cfg.Companion.On() {
		inst, err := e.installOptions()
		if err != nil {
			ln.Close()
			return err
		}
		if e.cfg.Companion != nil { // the section is absent until something is set
			companion.SetTrustedPrograms(e.cfg.Companion.TrustedPrograms) // the owner's, for the listener check
		}
		comp = companion.New(companion.Options{Store: st, Fleet: f, Paths: paths, Version: config.Version, Logger: log, Install: inst})
		f.SetCompanion(comp)
		f.SetCompanionMessenger(comp.SendHeadMessage)
	}

	// The owner's screen and microphone recordings (DESIGN.md "Recorder"): the window captures,
	// the daemon only stores what it is sent.
	rec := recorder.New(recorder.Options{Store: st, Dir: paths.RecordingsDir(), Publish: f.Publish, Logger: log,
		Settings: func() config.RecorderConfig {
			if c, err := config.Load(paths); err == nil && c.Recorder != nil {
				return *c.Recorder
			}
			return config.RecorderConfig{}
		}})
	if err := rec.Start(); err != nil {
		log.Error("recorder start", "err", err)
	}
	defer rec.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	auth := gate.OS()
	if auth.Method() == gate.MethodSimulated {
		log.Warn("GATESIM TEST BUILD: owner confirmations are simulated, never use this binary for a real install", "home", paths.Home, "available", fmt.Sprint(auth.Available()))
	}
	g := gate.New(paths, auth, log)
	defer g.Close()
	var netLog *p2p.Drain
	if drain {
		netLog = p2p.StartDrain(ctx, node, 200) // testing the network layer alone
	}
	const updateSwapBackoffEnv = "TMXG_UPDATE_SWAP_RETRY_AFTER"
	exe, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	audioGuard := &restartguard.Guard{}
	pendingSwap := make(chan func() (string, error), 1)
	var deferSwap func(func() (string, error)) bool
	if runtime.GOOS == "windows" {
		deferSwap = func(apply func() (string, error)) bool {
			select {
			case pendingSwap <- apply:
				stop()
				return true
			default:
				return false
			}
		}
	}
	upd := update.New(update.Options{
		DeferSwap: deferSwap,
		PrepareRestart: func() (func(), error) {
			if retryAt, err := time.Parse(time.RFC3339Nano, os.Getenv(updateSwapBackoffEnv)); runtime.GOOS == "windows" && err == nil && time.Now().Before(retryAt) {
				return nil, fmt.Errorf("Windows update restart deferred after a skipped swap until %s", retryAt.Format(time.RFC3339))
			}
			releaseAudio, err := audioGuard.Reserve(false, func() bool { return len(rec.Busy()) > 0 })
			if err != nil {
				return nil, err
			}
			releaseReset, err := accts.ReserveRestart()
			if err != nil {
				releaseAudio()
				return nil, err
			}
			return func() { releaseReset(); releaseAudio() }, nil
		},
		Paths: paths, Version: config.Version, Executable: exe, Desktop: desktop.Available, Logger: log,
		Settings: func() config.UpdateConfig {
			if c, err := config.Load(paths); err == nil && c.Update != nil {
				return *c.Update
			}
			return config.UpdateConfig{}
		},
		Save: func(u config.UpdateConfig) error {
			c, err := config.Load(paths)
			if err != nil {
				return err
			}
			c.Update = &u
			return config.Save(paths, c)
		},
		Busy:    func() []string { return append(busyAgents(f), rec.Busy()...) },
		Publish: f.Publish,
		Restart: func(newExe string) {
			restartExe = newExe
			stop()
		},
	})
	rulesToken = upd.GitHubToken
	voice := newVoice(paths, st, upd, sl, f, log) // voice.go: local transcription
	rec.SetTranscriber(voice)                     // finished recordings can be transcribed (auto or on request)

	// summary.go: Claude's summaries of finished recordings (transcribed first, then summarized).
	// Every claude this daemon starts (agents, summaries, accounts) keeps its state in the
	// daemon's own CLAUDE_CONFIG_DIR, never the owner's shared ~/.claude.json (7 Oct).
	claudeDir, err := paths.EnsureClaudeConfigDir()
	if err != nil {
		return err
	}
	sum := newSummaries(paths, e.cfg, st, rec, voice, f, log)
	// recworker: on Stop the recorder launches its own short-lived report worker and tracks it (G5, "tool > agent")
	recw := recworker.New(recworker.Options{Fleet: f, Summaries: sum, Recordings: rec, Exe: f.Executable(),
		Dir: filepath.Join(paths.Home, "rec-workers"), Logger: log})
	rec.SetFinished(func(r model.Recording) {
		if voice.ViaPeer() != "" {
			sum.Finished(r)
			return
		}
		recw.Finished(r)
	})
	wa := newWhatsApp(paths, st, f, log, voice) // wa.go: the owner's WhatsApp, linked as a companion device
	f.SetWhatsApp(wa)
	ups := uploads.New(paths)
	ups.Prune(func(id string) bool { _, err := f.Agent(id); return err == nil }) // agents removed while down
	// The AI accounts the harnesses are signed in to, and their plans' usage (DESIGN.md "AI
	// accounts and usage"). The harnesses run in a folder of their own, never a project.
	accts = accounts.New(accounts.Options{Dir: filepath.Join(paths.Home, "accounts"), Publish: f.Publish, Logger: log,
		ClaudeUsageChanged: f.FailoverClaudeAccount,
		UsageObserved:      f.ObserveAccountUsage,
		AgyBackgroundUsage: func() bool {
			c, err := config.Load(paths)
			return err == nil && (c.AgyBackgroundUsage == nil || *c.AgyBackgroundUsage)
		},
		NodeHome: paths.Home, AllowUserLogins: e.cfg.UseUserAccounts(paths.Home),
		Commands: accountCommands(e.cfg),
		// The sign-in shown and made here (`claude auth login`) is the agents' own.
		Env:      []string{claude.EnvConfigDir + "=" + claudeDir},
		Profiles: profiles})
	f.SetClaudeFailoverSelector(accts.ClaudeFailoverAccount)
	// Start only after the reset busy callback has its account service.
	// Both profiles consume manual installs and schedule explicitly enabled auto updates.
	go upd.Run(ctx)
	f.SetRateLimitHook(func(h model.Harness, account string, rl runner.RateLimit) {
		harness := fleet.AccountHarness(h)
		if accts.RateLimited(harness, account, rl.Type, rl.ResetsAt) && harness == model.HarnessClaude {
			f.FailoverClaudeAccount(account)
		}
	})
	// peers: the Python fleet's other machines (production only; DESIGN.md "Python fleet interop").
	var fleetPeers *peers.Client
	if e.cfg.Production() {
		fleetPeers = peers.New(e.cfg, paths, log)
		voice.SetPeer(fleetPeers)
		f.SetPeerSender(fleetPeers, fleetPeers.Machine())
		f.SetPeerHealth(func() []fleet.PeerHealth { // the watch tells the head when a machine stays unreachable
			st := fleetPeers.Statuses()
			out := make([]fleet.PeerHealth, 0, len(st))
			for _, p := range st {
				out = append(out, fleet.PeerHealth{Name: p.Name, OK: p.OK, Seen: p.Seen})
			}
			return out
		})
		f.SetFleetState(func() fleet.FleetStateBlock { // the head's owner turn carries the Python fleet's state block
			st := fleetPeers.FleetState()
			return fleet.FleetStateBlock{From: st.From, At: st.At, Lines: st.Lines}
		})
		sum.SetPeer(fleetPeers) // summaries.via_peer: a peer's local model writes this machine's summaries
		go fleetPeers.Run(ctx)
		go fleetPeers.RunFleetState(ctx)
	}
	var copies *transfers.Service
	if fleetPeers != nil {
		protected := []string{}
		if pythonHome, err := peers.PythonHome(); err == nil {
			protected = append(protected, pythonHome)
		}
		hold := e.cfg.Transfers != nil && e.cfg.Transfers.HoldNeedsHuman
		ownerNames, err := config.InspectionOwnerNames(paths, e.cfg)
		if err != nil {
			log.Warn("inspection owner names: using configured names and generic defaults", "err", err)
		}
		copies, err = transfers.New(transfers.Options{OwnerNames: ownerNames, Home: paths.Home, Install: filepath.Dir(currentExe), Protected: protected,
			SecretFolders: e.cfg.SecretFolders, TrustedBackupRoots: e.cfg.TrustedBackupRoots, HoldNeedsHuman: hold, Peers: fleetPeers, Logger: log})
		if err != nil {
			return err
		}
		defer copies.Close()
		copies.Resume()
	}
	// A machine already in use never gets the first-run setup opening by itself (api/config.go).
	if marked, err := api.MarkOnboardedIfInUse(f); err != nil {
		log.Warn("first-run setup: could not check whether this machine is already in use", "err", err)
	} else if marked {
		log.Info("first-run setup: this machine is already in use; marked as shown")
	}
	hist := reshistory.New(paths, nil, log) // the Watchdog page's resource history
	srv := api.New(api.Options{SystemWarnings: e.cfg.Warnings, Fleet: f, OwnerToken: token, Port: e.cfg.Port, TrustedHosts: e.cfg.API.TrustedHosts, PhoneLock: e.cfg.PhoneLock, LANAccess: e.cfg.API.LANAccess, Shutdown: stop, Restart: func() { restartExe = currentExe; stop() }, Gate: g, Logger: log,
		Jobs: jobWorkers, Transfers: copies, AudioGuard: audioGuard,
		Resources: rm, History: hist, Health: machinehealth.New(), Slots: sl, Chats: chats, P2P: node, P2PLog: netLog, Companion: comp, Uploads: ups, Updater: upd, Recorder: rec,
		Voice: voice, Summaries: sum, RecWorker: recw, WhatsApp: wa, Accounts: accts, ClaudeExt: cext, Peers: fleetPeers,
		SaveP2PName: func(name string) error {
			c, err := config.Load(paths)
			if err != nil {
				return err
			}
			if c.P2P == nil {
				c.P2P = &config.P2PConfig{}
			}
			c.P2P.Name = name
			return config.Save(paths, c)
		}})
	// Voice must be ready before HTTP accepts the first dictation after restart.
	if err := voice.Start(); err != nil {
		log.Error("voice start", "err", err)
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx, ln) }()
	// Post-swap watchdog: a new binary on probation confirms itself healthy or, at the deadline,
	// restarts so that its next start restores the previous pair (rollback.StartProbation).
	go func() {
		watchProbation(ctx, log, checkpoint, e.cfg.URL()+"/api/health", time.Now, probationPoll, func() {
			restartExe = currentExe
			stop()
		})
		// Retry a confirmed notice on later starts too; the mailbox deduplicates it.
		for {
			if result, ok := rollback.LastProbation(checkpoint); !ok || result.Outcome != "confirmed" {
				return
			} else if err := f.UpdateNotice(result); err == nil {
				return
			} else {
				log.Warn("update notice", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(probationPoll):
			}
		}
	}()

	if err := config.WriteDaemonInfo(paths, config.DaemonInfo{
		PID: os.Getpid(), Port: e.cfg.Port, StartedAt: model.Now(), Version: config.Version,
	}); err != nil {
		log.Warn("write daemon.json", "err", err)
	}
	defer config.RemoveDaemonInfo(paths)
	log.Info("tmx daemon started", "url", e.cfg.URL(), "machine", e.cfg.Machine, "version", config.Version, "home", paths.Home)

	if err := rm.Start(); err != nil { // continues processes a crashed daemon left paused
		log.Warn("resource control start", "err", err)
	}
	if err := f.StartContext(ctx); err != nil {
		log.Error("fleet start", "err", err)
		stop()
	}
	f.SetChatNamer(func(id string) string { // the watch names a chat whose agents talk without a person
		if c, err := chats.Chat(id); err == nil {
			return c.Name
		}
		return ""
	})
	go rulesSvc.Run(ctx) // after the fleet's start: there is a head to tell what the first look finds
	go rm.Run(ctx)
	go rm.Watchdog(ctx)
	// The Watchdog page's history: it reads the sampler's latest sample, measures nothing itself.
	go hist.RunWithDisks(ctx, func() resources.Sample { return rm.View().Sample }, sysinfo.Disks, 5*time.Second)
	go sl.Run(ctx)
	if err := chats.Start(); err != nil {
		log.Error("chats start", "err", err)
	}
	if comp != nil {
		if err := comp.Start(); err != nil {
			log.Error("companion start", "err", err)
		}
	}
	recw.Start()
	if err := sum.Start(); err != nil {
		log.Error("summaries start", "err", err)
	}
	if err := wa.Start(); err != nil {
		log.Error("whatsapp start", "err", err)
	}
	accts.Start() // checks in the background; never holds up the start
	var serveErr error
	var stopReason any
	select {
	case <-ctx.Done():
		stopReason = context.Cause(ctx)
	case serveErr = <-served:
		served = nil // the result is already consumed, even when Serve returns nil
		stopReason = serveErr
		stop()
	}
	log.Info("tmx daemon stopping", "reason", stopReason)
	sl.Close()
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type shutdownResult struct {
		serveErr error
		fleetErr error
	}
	closed := make(chan shutdownResult, 1)
	go func(err error, server <-chan error) {
		accts.Close()
		recw.Close()
		sum.Close()
		wa.Close()
		voice.Close()
		if comp != nil {
			comp.Close()
		}
		chats.Close()
		fleetErr := f.Close(cctx)
		if fleetErr != nil {
			log.Warn("fleet close", "err", fleetErr)
		}
		if server != nil {
			err = <-server
		}
		closed <- shutdownResult{serveErr: err, fleetErr: fleetErr}
	}(serveErr, served)
	// Persist state independently of fleet teardown and HTTP drain.
	rm.Close()
	hist.Save()
	var result shutdownResult
	select {
	case result = <-closed:
	case <-cctx.Done():
		result.fleetErr = cctx.Err()
		log.Warn("daemon shutdown deadline reached; exiting", "err", result.fleetErr)
	}
	select {
	case apply := <-pendingSwap:
		restartExe = currentExe
		swapErr := errors.Join(result.fleetErr, cctx.Err())
		if swapErr == nil {
			var next string
			next, swapErr = apply()
			if swapErr == nil {
				restartExe = next
			}
		}
		if swapErr != nil {
			retryAt := time.Now().Add(time.Hour)
			// respawnDaemon inherits the environment, so its first automatic
			// update checks cannot repeat this shutdown.
			_ = os.Setenv(updateSwapBackoffEnv, retryAt.Format(time.RFC3339Nano))
			log.Error("update: restarting previous binary after skipped swap", "err", swapErr, "retry_at", retryAt)
		}
	default:
		if result.serveErr != nil && !errors.Is(result.serveErr, context.Canceled) {
			return result.serveErr
		}
	}
	log.Info("tmx daemon stopped")
	return nil
}

// busyAgents uses live idle/turn evidence and a bounded quiet interval, while
// retaining the independent gate for open approvals.
func busyAgents(f *fleet.Fleet) []string {
	return f.UpdateBusy(time.Now())
}

// respawnDaemon starts the daemon again from exe (after an update), detached like `tmx up` does.
func respawnDaemon(e *env, exe string) error {
	out, err := os.OpenFile(filepath.Join(e.paths.LogsDir(), "daemon.stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	fmt.Fprintf(out, "%s tmx: restarting from %s after an update\n", time.Now().Format(time.RFC3339), exe)
	cmd := hiddenexec.Command(exe, "daemon")
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = append(os.Environ(), config.EnvHome+"="+e.paths.Home)
	detach(cmd)
	dir := filepath.Join(e.paths.Home, "rollback")
	p, pending, _ := rollback.OnProbation(dir)
	err = cmd.Start()
	if !pending || p.DesktopSHA256 == "" {
		return err
	}
	if err == nil {
		if reason := watchDesktopStart(cmd, dir, p); reason == "" {
			return nil
		} else {
			err = errors.New(reason)
		}
	}
	return restoreFailedDesktopStart(e, exe, dir, p, err)
}

// accountCommands are the executables the AI accounts run: claude_command, and
// sign_in_commands for codex and agy (only known harnesses, only absolute paths).
func accountCommands(cfg config.Config) map[model.Harness]string {
	m := map[model.Harness]string{model.HarnessClaude: cfg.ClaudeCommand}
	for h, c := range cfg.SignInCommands {
		if (h == string(model.HarnessCodex) || h == "antigravity") && filepath.IsAbs(c) {
			m[model.Harness(h)] = c
		}
	}
	return m
}
