package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/desktop"
	"github.com/blackcode-ch/tmx-go/internal/transfers"
	"github.com/blackcode-ch/tmx-go/internal/update"
	"github.com/blackcode-ch/tmx-go/internal/uploads"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/accounts"
	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/chat"
	"github.com/blackcode-ch/tmx-go/internal/companion"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/jobs"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/peers"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
	"github.com/blackcode-ch/tmx-go/internal/recworker"
	"github.com/blackcode-ch/tmx-go/internal/rollback"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// cmdDaemon runs the daemon in the foreground until SIGINT/SIGTERM or POST /api/shutdown.
func cmdDaemon(e *env, args []string) (err error) {
	if _, err := parse(newFlags("daemon"), args); err != nil {
		return err
	}
	if err := e.load(); err != nil {
		return err
	}
	// After an update the daemon starts its successor from the new binary. This deferred call runs
	// last: once the agents are stopped, the store is closed and the lock is released.
	var restartExe string
	defer func() {
		if restartExe != "" && err == nil {
			err = respawnDaemon(e, restartExe)
		}
	}()
	lock, err := config.WaitLock(e.paths, 15*time.Second) // a predecessor may still be stopping its agents
	if err != nil {
		return err
	}
	defer lock.Release()

	logFile, err := os.OpenFile(e.paths.DaemonLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(io.MultiWriter(e.errw, logFile), &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
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

	paths := e.paths
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
	// Terminal agents run in session hosts that outlive this daemon (fleet/hosting.go), from a
	// copy of this tmx so an update can replace the installed one while they run.
	var hostExe string
	if self, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(self); err == nil {
			self = real
		}
		if hostExe, err = hostExecutable(paths.Home, self); err != nil {
			log.Warn("session hosts: no copy of tmx for them; terminal agents end with the daemon", "err", err)
			hostExe = ""
		}
	}
	f, err := fleet.New(fleet.Options{
		Tasks:          e.cfg.Tasks,
		HostExecutable: hostExe,
		Accounts:       profiles,
		AutoShelve:     e.cfg.AutoShelve,
		Supervisor:     e.cfg.Supervisor,
		Store:          st, Paths: paths, Runners: runners(),
		Machine: e.cfg.Machine, Version: config.Version, Workspace: e.cfg.Workspace,
		FleetAuthorities: e.cfg.FleetAuthorities, Teammates: e.cfg.Teammates, AgentBypass: e.cfg.AgentBypass,
		URL: e.cfg.URL(), ClaudeCommand: e.cfg.ClaudeCommand, HeadHarness: e.cfg.HeadHarness,
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
		Jobs: jobWorkers, FileMaxBytes: e.cfg.P2P.FileLimit(),
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
	exe, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	upd := update.New(update.Options{
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
	// Trial has no updater loop (manual endpoints remain owner-controlled). Production runs it for
	// the owner's installs only: config forces automatic checks off in v1 (no signed release yet).
	if e.cfg.Profile != config.ProfileTrial {
		go upd.Run(ctx)
	}
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
	wa := newWhatsApp(paths, st, f, log) // wa.go: the owner's WhatsApp, linked as a companion device
	f.SetWhatsApp(wa)
	ups := uploads.New(paths)
	ups.Prune(func(id string) bool { _, err := f.Agent(id); return err == nil }) // agents removed while down
	// The AI accounts the harnesses are signed in to, and their plans' usage (DESIGN.md "AI
	// accounts and usage"). The harnesses run in a folder of their own, never a project.
	accts := accounts.New(accounts.Options{Dir: filepath.Join(paths.Home, "accounts"), Publish: f.Publish, Logger: log,
		Commands: accountCommands(e.cfg),
		// The sign-in shown and made here (`claude auth login`) is the agents' own.
		Env:      []string{claude.EnvConfigDir + "=" + claudeDir},
		Profiles: profiles})
	f.SetRateLimitHook(func(h model.Harness, account string, rl runner.RateLimit) {
		accts.RateLimited(fleet.AccountHarness(h), account, rl.Type, rl.ResetsAt)
	})
	// peers: the Python fleet's other machines (production only; DESIGN.md "Python fleet interop").
	var fleetPeers *peers.Client
	if e.cfg.Production() && len(e.cfg.Peers) > 0 {
		fleetPeers = peers.New(e.cfg, paths, log)
		voice.SetPeer(fleetPeers)
		f.SetPeerSender(fleetPeers, fleetPeers.Machine())
		sum.SetPeer(fleetPeers) // summaries.via_peer: a peer's local model writes this machine's summaries
		go fleetPeers.Run(ctx)
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
	srv := api.New(api.Options{SystemWarnings: e.cfg.Warnings, Fleet: f, OwnerToken: token, Port: e.cfg.Port, TrustedHosts: e.cfg.API.TrustedHosts, Shutdown: stop, Restart: func() { restartExe = currentExe; stop() }, Gate: g, Logger: log,
		Jobs: jobWorkers, Transfers: copies,
		Resources: rm, Slots: sl, Chats: chats, P2P: node, P2PLog: netLog, Companion: comp, Uploads: ups, Updater: upd, Recorder: rec,
		Voice: voice, Summaries: sum, RecWorker: recw, WhatsApp: wa, Accounts: accts, Peers: fleetPeers,
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
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx, ln) }()
	// Post-swap watchdog: a new binary on probation confirms itself healthy or, at the deadline,
	// restarts so that its next start restores the previous pair (rollback.StartProbation).
	go watchProbation(ctx, log, checkpoint, e.cfg.URL()+"/api/health", time.Now, probationPoll, func() {
		restartExe = currentExe
		stop()
	})

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
	if err := f.Start(); err != nil {
		log.Error("fleet start", "err", err)
		stop()
	}
	go rm.Run(ctx)
	go rm.Watchdog(ctx)
	go sl.Run(ctx)
	if err := chats.Start(); err != nil {
		log.Error("chats start", "err", err)
	}
	if comp != nil {
		if err := comp.Start(); err != nil {
			log.Error("companion start", "err", err)
		}
	}
	if err := voice.Start(); err != nil {
		log.Error("voice start", "err", err)
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
	select {
	case <-ctx.Done():
	case serveErr = <-served:
		served = nil // the result is already consumed, even when Serve returns nil
		stop()
	}
	log.Info("tmx daemon stopping")
	accts.Close()
	recw.Close()
	sum.Close()
	wa.Close()
	voice.Close()
	if comp != nil {
		comp.Close()
	}
	chats.Close()
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := f.Close(cctx); err != nil {
		log.Warn("fleet close", "err", err)
	}
	rm.Close()
	if served != nil {
		serveErr = <-served
	}
	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		return serveErr
	}
	log.Info("tmx daemon stopped")
	return nil
}

// busyAgents names what an update must not interrupt: agents in a turn, starting, waiting for an
// answer or paused by a brake, and open approvals.
func busyAgents(f *fleet.Fleet) []string {
	snap, err := f.Snapshot()
	if err != nil {
		return []string{"the fleet (" + err.Error() + ")"}
	}
	var out []string
	for _, a := range snap.Agents {
		switch a.State {
		case model.StateWorking, model.StateWaiting, model.StateStarting, model.StatePaused:
			out = append(out, a.Name)
		}
	}
	if n := len(snap.Approvals); n > 0 {
		out = append(out, fmt.Sprintf("%d open approvals", n))
	}
	return out
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
	cmd.Env = os.Environ()
	detach(cmd)
	return cmd.Start()
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
