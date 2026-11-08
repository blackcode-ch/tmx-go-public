// Package fleet is the daemon's orchestrator: the in-memory registry of agents, projects
// and approvals (backed by the store), process lifecycle through injected runners,
// hierarchy and policy, mail, approvals, and the event bus. See DESIGN.md.
//
// Concurrency: one mutex (Fleet.mu) guards all in-memory state and is held while writing
// the store and publishing on the bus (both quick, neither blocks). It is never held while
// calling into a runner.Process or runner.Runner, which may block on a child process.
package fleet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/discover"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/processes"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// Paths returns the state directory paths of this fleet.
func (f *Fleet) Paths() config.Paths { return f.opts.Paths }

// Options configures a Fleet. Store, Paths and Runners are required.
type Options struct {
	Tasks      *config.TaskConfig
	Supervisor *string // trusted config: nil = architect, empty = unique-head fallback
	AutoShelve *bool   // nil = enabled; trusted config, never agent input
	Store      *store.Store
	Paths      config.Paths
	Runners    map[model.Mode]runner.Runner
	// Elevated is a trusted host capability, never a request/config field. Nil
	// uses the real process token; fake-runner tests supply their simulated host.
	Elevated func() bool

	Machine       string
	Version       string
	Workspace     string // project folders live here; also the head's cwd
	URL           string // daemon base URL given to agents as TMXG_URL
	ClaudeCommand string // "" = claude on PATH
	HeadHarness   string // "" = claude; chat-capable harness for the head agent
	Executable    string // tmx binary for agents' mcp.json; "" = os.Executable()
	// HostExecutable is the tmx that terminal agents' session hosts run (`tmx host`): they
	// outlive the daemon (hosting.go). "" = no hosts: agents end with the daemon.
	HostExecutable string
	// Policy seeds the built-in profiles when the store has none yet (config.json "policy";
	// zero = model.DefaultPolicy()). Afterwards the profiles in the store are authoritative.
	Policy    model.Policy
	Guard     *guard.Settings // nil = guard.DefaultSettings() (on for everyone)
	SaveGuard func(guard.Settings) error
	P2P       p2p.Node // the peer-to-peer node (DESIGN.md "Peer-to-peer network"); nil = off
	Logger    *slog.Logger
	// Resources is resource control: the launch queue's limits and the brakes (nil = none).
	Resources Resources
	// Suspend pauses (stop) or continues an agent's process tree; nil = sysinfo.SuspendTree.
	Suspend func(pid int, stop bool) ([]sysinfo.Proc, error)
	// Discover finds agents started outside tmx (discover/adopt); nil = the real machine.
	Discover *discover.Scanner
	// AgentBypass: Claude agents with the guard hook run in bypass mode (guard.go); nil = on.
	AgentBypass *bool
	// FleetAuthorities are "name@host" senders whose peer mail counts as the owner's standing
	// order (authority.go); nil = DefaultFleetAuthorities.
	FleetAuthorities []string
	// Accounts are the harnesses' named accounts (internal/accounts); nil = one account each.
	Accounts AccountHomes
}

// Fleet is the orchestrator. Create with New, then Start; Close on shutdown.
type Fleet struct {
	hostRetry       time.Duration                  // hosting.go
	stopHost        func(dir string) (bool, error) // hosting.go
	authorities     map[string]bool                // authority.go
	processes       *processes.Registry
	processRequests map[string]ownedProcessRequest
	opts            Options
	st              *store.Store
	log             *slog.Logger
	bus             *Bus

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	cardRouteWarnings map[string]bool // once per orphaned card per daemon run; guarded by mu

	mu                sync.Mutex
	closed            bool
	shelveLoopStarted bool
	profiles          map[string]*model.Profile // by slug
	agents            map[string]*agentRec      // by ID
	projects          map[string]*model.Project
	tokens            map[string]string      // agent token -> agent ID
	perms             map[string]permRequest // pending permission approval ID -> harness request
	res               resState               // launch queue and brakes (resources.go)
	safety            *safetyState           // guard settings and holds (guard.go)

	companion    CompanionDecider   // decides approvals of kind companion (companion.go); nil = none
	companionMsg CompanionMessenger // sends the head's messages to Companion (companion.go); nil = none
	whatsapp     CompanionDecider   // decides approvals of kind whatsapp (whatsapp.go); nil = none

	rateLimited func(model.Harness, string, runner.RateLimit) // ratelimit.go; nil = none

	peer peerState // mail to and from the fleet's other machines (peermail.go)
}

// agentRec is the live state of one agent. Guarded by Fleet.mu.
type agentRec struct {
	recovery crashRecovery
	a        model.Agent
	proc     runner.Process
	done     chan struct{} // closed when the current process's event loop ends
	starting bool          // a runner.Start is in flight
	stopping bool          // a stop was requested; the exit becomes "stopped"
	// detaching: the daemon is stopping and left the process running in its session host
	// (runner.Detacher): its end is not an exit.
	detaching bool
	// hostUnknown: whether its session host still runs could not be told (hosting.go); it is
	// not started again (two copies on one conversation) until that is settled.
	hostUnknown  bool
	hostRetrying bool // a retryHost loop is waiting
	hostStopping bool // stopUnknownHost runs: no start until it is done
	token        string
	seq          int64
	// sessionConfirmed: the harness reported SessionID, so the conversation exists and is
	// resumed; otherwise a pre-assigned SessionID is passed as a new conversation's id.
	sessionConfirmed bool
	waiting          int // pending permission approvals
	terms            map[*TerminalSub]struct{}
	humanInputAt     time.Time // the owner last typed into its terminal
	retryMail        bool      // a delivery retry (after human typing) is scheduled
	// instructionsDue: a harness that takes no system prompt (opencode, pi) gets tmx's
	// instructions again with its next prompt or mail: after a resume (its conversation
	// carries them only from its first message), and every instructionsEvery of use (these
	// harnesses don't tell tmx when they compact). instructionsAt: when it last got them.
	instructionsDue bool
	instructionsAt  time.Time
	// The head's board (board.go): the thread of the current turn, the seq it started at,
	// and whether its thread was chosen (else an unfiled General turn may move when it ends).
	thread       string
	turnFrom     int64
	turnExplicit bool
	// Live mode switch (mode.go): when it last became idle, whether a switch is scheduled or
	// running, and whether its process ended by itself while switched (it then goes home).
	idleAt          time.Time
	switchScheduled bool
	switching       bool
	goHome          bool
}

type permRequest struct {
	agentID   string
	requestID string
	proc      runner.Process
}

// New loads the fleet from the store. It starts nothing; call Start.
func New(opts Options) (*Fleet, error) {
	if opts.Elevated == nil {
		opts.Elevated = runner.Elevated
	}
	if opts.Store == nil || opts.Paths.Home == "" {
		return nil, errors.New("fleet: Store and Paths are required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Policy.Levels == nil {
		opts.Policy = model.DefaultPolicy()
	}
	if opts.Executable == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("fleet: locate tmx binary: %w", err)
		}
		opts.Executable = exe
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &Fleet{
		hostRetry: hostRetry, stopHost: stopHost, // hosting.go
		authorities: normalizeAuthorities(opts.FleetAuthorities),
		processes:   processes.New(),
		opts:        opts, st: opts.Store, log: opts.Logger, bus: newBus(),
		ctx: ctx, cancel: cancel,
		profiles: map[string]*model.Profile{},
		agents:   map[string]*agentRec{},
		projects: map[string]*model.Project{},
		tokens:   map[string]string{},
		perms:    map[string]permRequest{},
	}
	if opts.HostExecutable != "" { // agents' tools outlive the daemon too (processes/hosted.go)
		f.processes.EnableHosts(opts.HostExecutable, filepath.Join(opts.Paths.Home, "processes"))
	}
	agents, err := f.st.ListAgents()
	if err != nil {
		return nil, err
	}
	if err := f.st.RecoverMailReceipts(); err != nil {
		return nil, err
	}
	if err := f.st.RecoverTasks(); err != nil {
		return nil, err
	}
	for _, a := range agents {
		seq, err := f.st.LastSeq(a.ID)
		if err != nil {
			return nil, err
		}
		n, err := f.st.CountQueued(a.ID)
		if err != nil {
			return nil, err
		}
		a.Queued = n
		talked, err := f.st.HasConversation(a.ID)
		if err != nil {
			return nil, err
		}
		f.agents[a.ID] = &agentRec{a: a, seq: seq, sessionConfirmed: a.SessionID != "" && talked}
	}
	projects, err := f.st.ListProjects()
	if err != nil {
		return nil, err
	}
	for i := range projects {
		f.projects[projects[i].ID] = &projects[i]
	}
	if h := f.headLocked(); h != nil {
		f.loadBoardLocked(h)
	}
	if err := f.loadProfiles(opts.Policy); err != nil {
		return nil, err
	}
	return f, nil
}

// Machine, Version and Workspace describe this daemon.
func (f *Fleet) Machine() string   { return f.opts.Machine }
func (f *Fleet) Version() string   { return f.opts.Version }
func (f *Fleet) Workspace() string { return f.opts.Workspace }

// P2P is the peer-to-peer node, or nil when it is off.
func (f *Fleet) P2P() p2p.Node { return f.opts.P2P }

// Subscribe returns a bus subscription; pair with Unsubscribe.
func (f *Fleet) Subscribe() *Sub    { return f.bus.Subscribe() }
func (f *Fleet) Unsubscribe(s *Sub) { f.bus.Unsubscribe(s) }

// Close stops every process (keeping desired=running, so the next daemon resumes them) and
// waits for their event loops. A process in a session host (runner.Detacher) is left
// running instead: the next daemon reattaches to it (DESIGN.md "Restarts").
func (f *Fleet) Close(ctx context.Context) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	var ids []string
	var detach []runner.Detacher
	for id, r := range f.agents {
		if r.proc == nil {
			continue
		}
		if d, ok := r.proc.(runner.Detacher); ok && f.hostedLocked(r.a) {
			r.detaching = true
			detach = append(detach, d)
			continue
		}
		ids = append(ids, id)
	}
	f.mu.Unlock()
	for _, d := range detach {
		d.Detach()
	}
	var wg sync.WaitGroup
	f.processes.Detach() // agents' tools in session hosts run on (processes/hosted.go)
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.stopProcess(ctx, id, true)
		}()
	}
	wg.Wait()
	f.cancel()
	done := make(chan struct{})
	go func() { f.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Snapshot is the whole fleet as the UI and agents see it.
func (f *Fleet) Snapshot() (model.Snapshot, error) {
	pending, err := f.st.ListApprovals(model.ApprovalPending)
	if err != nil {
		return model.Snapshot{}, err
	}
	if pending == nil {
		pending = []model.Approval{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := model.Snapshot{
		RollbackWarning: f.st.RollbackWarning(),
		Machine:         f.opts.Machine, Version: f.opts.Version, Workspace: f.opts.Workspace,
		Policy: f.policyLocked(), Profiles: f.profileListLocked(), Approvals: pending,
		Agents: f.agentsLocked(), Projects: f.projectsLocked(),
	}
	for i := range s.Agents {
		if s.Agents[i].Level == model.LevelHead {
			h := s.Agents[i]
			s.Head = &h
			break
		}
	}
	return s, nil
}

func (f *Fleet) agentsLocked() []model.Agent {
	out := make([]model.Agent, 0, len(f.agents))
	for _, r := range f.agents {
		out = append(out, r.a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (f *Fleet) projectsLocked() []model.Project {
	out := make([]model.Project, 0, len(f.projects))
	for _, p := range f.projects {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Agent returns an agent by ID or name.
func (f *Fleet) Agent(idOrName string) (model.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.resolveLocked(idOrName)
	if err != nil {
		return model.Agent{}, err
	}
	return r.a, nil
}

// Project returns a project by ID or slug.
func (f *Fleet) Project(idOrSlug string) (model.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.projectLocked(idOrSlug)
	if err != nil {
		return model.Project{}, err
	}
	return *p, nil
}

// Timeline returns an agent's entries (see store.Timeline for after/limit).
func (f *Fleet) Timeline(idOrName string, after int64, limit int) ([]model.TimelineEntry, error) {
	a, err := f.Agent(idOrName)
	if err != nil {
		return nil, err
	}
	if limit > 1000 {
		limit = 1000
	}
	return f.st.Timeline(a.ID, after, limit)
}

func (f *Fleet) resolveLocked(idOrName string) (*agentRec, error) {
	name, host, qualified, err := DecodeAgentAddress(idOrName)
	if err != nil {
		return nil, err
	}
	if qualified && host != f.selfLocked() {
		return nil, errf(KindNotFound, "agent %q belongs to another machine", idOrName)
	}
	idOrName = name
	if r, ok := f.agents[idOrName]; ok {
		return r, nil
	}
	for _, r := range f.agents {
		if r.a.Name == idOrName {
			return r, nil
		}
	}
	return nil, errf(KindNotFound, "no agent named %q", idOrName)
}

func (f *Fleet) projectLocked(idOrSlug string) (*model.Project, error) {
	if p, ok := f.projects[idOrSlug]; ok {
		return p, nil
	}
	for _, p := range f.projects {
		if p.Slug == idOrSlug || p.Name == idOrSlug {
			return p, nil
		}
	}
	return nil, errf(KindNotFound, "no project %q", idOrSlug)
}

func (f *Fleet) headLocked() *agentRec {
	for _, r := range f.agents {
		if r.a.Level == model.LevelHead {
			return r
		}
	}
	return nil
}

// saveAgentLocked persists an agent and publishes agent.updated. A live mode switch is not
// stored (mode.go): the store keeps the launch mode, which a daemon restart brings back.
func (f *Fleet) saveAgentLocked(r *agentRec) {
	r.a.UpdatedAt = model.Now()
	stored := r.a
	if stored.HomeMode != "" {
		stored.Mode = stored.HomeMode
	}
	if err := f.st.PutAgent(stored); err != nil {
		f.log.Error("store agent", "agent", r.a.Name, "err", err)
	}
	f.bus.Publish(model.BusEvent{Type: model.EvAgentUpdated, AgentID: r.a.ID, ProjectID: r.a.ProjectID, Data: r.a})
}

func (f *Fleet) saveProjectLocked(p *model.Project) error {
	p.UpdatedAt = model.Now()
	if err := f.st.PutProject(*p); err != nil {
		return err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvProjectUpdated, ProjectID: p.ID, Data: *p})
	return nil
}

func (f *Fleet) nameOfLocked(id string) string {
	if id == model.Owner {
		return model.Owner
	}
	if IsExternal(id) {
		return "chat"
	}
	if id == model.CompanionParty {
		return "Companion"
	}
	if id == model.WhatsAppParty {
		return "WhatsApp"
	}
	if name, host, ok := SplitPeer(id); ok {
		return name + "@" + host
	}
	if r, ok := f.agents[id]; ok {
		return r.a.Name
	}
	return id
}

// go runs fn on a goroutine tracked by Close.
func (f *Fleet) goTracked(fn func()) {
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		fn()
	}()
}
