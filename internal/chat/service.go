// Package chat is the chat layer between tmx installs (DESIGN.md "Chats"): contacts made
// by invite, DMs and groups, messages with delivery receipts and an offline outbox, and the
// participation of a local agent in each chat. It builds only on p2p.Node, so it is tested
// with p2ptest's in-memory network.
//
// Trust: everything that arrives from another install is data. It can only become a chat
// message (stored, shown, and given to the chat's participating agent as mail labelled as
// external); it can never start an agent, answer an approval or pass the guard.
package chat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/jobs"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// Limits (DESIGN.md "Chats").
const (
	MaxBodyBytes    = 32 << 10
	MaxGroupMembers = 20
	// MaxAgentRun: after this many agent messages in a row (no person in between) agents
	// get no more chat mail and cannot send in that chat until a person writes.
	MaxAgentRun = 20
	// Incoming envelopes per peer: a bucket of rateBurst refilled at ratePerSec.
	rateBurst  = 120
	ratePerSec = 2.0
)

// ErrOff is returned by every method while peer-to-peer is off (no node).
var ErrOff = errors.New("peer-to-peer is off")

// Agents is what the chat layer needs from the fleet (*fleet.Fleet implements it).
type Agents interface {
	Agent(idOrName string) (model.Agent, error)
	HeadID() string
	DeliverExternal(agentID, from, text string) (model.Message, error)
	Publish(ev model.BusEvent)
}

// Options configures a Service. Store and Agents are required; Node nil = p2p off.
type Options struct {
	Jobs   *jobs.Manager // daemon's bounded audit worker; nil retains compatibility for embedders
	Node   p2p.Node
	Store  *store.Store
	Agents Agents
	// Name returns this owner's display name, sent with messages as a hint ("" = none;
	// receivers then use the name they know).
	Name   func() string
	Logger *slog.Logger
	// FilesDir stores chat files (config.Paths.ChatFilesDir); "" = files are refused.
	FilesDir string
	// FileMaxBytes may lower the 25 MiB protocol ceiling; zero uses that ceiling.
	FileMaxBytes int64

	RetryMin    time.Duration // first retry after a failed send (default 2 s), doubling ...
	RetryMax    time.Duration // ... up to this (default 5 min)
	OutboxTTL   time.Duration // queued envelopes older than this fail (default 7 days)
	SendTimeout time.Duration // one Node.Send (default 20 s)
	SendWait    time.Duration // how long a send waits for its first attempt (default 3 s)
}

// Service is the chat layer. Create with New, then Start; Close on shutdown.
type Service struct {
	o Options
	// boardChanges: when each agent changed a chat board lately (topics.go; guarded by mu).
	boardChanges map[string][]int64
	node         p2p.Node
	st           *store.Store
	ag           Agents
	log          *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex // serializes changes to chats, members and messages
	selfID  string
	workers map[string]*worker
	buckets map[string]*bucket
	held    map[string][]heldEnv // group messages from peers not (yet) members, by chat
	// collected under mu, acted on by unlock
	pendingPeers []string
	pendingMail  []mailItem
	fetchKick    chan struct{} // wakes the file fetcher
	adm          adminMem      // admin handovers (admin.go)
}

func New(o Options) (*Service, error) {
	if o.Store == nil || o.Agents == nil {
		return nil, errors.New("chat: Store and Agents are required")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Name == nil {
		o.Name = func() string { return "" }
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&o.RetryMin, 2*time.Second)
	def(&o.RetryMax, 5*time.Minute)
	def(&o.OutboxTTL, 7*24*time.Hour)
	def(&o.SendTimeout, 20*time.Second)
	def(&o.SendWait, 3*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{o: o, node: o.Node, st: o.Store, ag: o.Agents, log: o.Logger, ctx: ctx, cancel: cancel,
		workers: map[string]*worker{}, buckets: map[string]*bucket{}, held: map[string][]heldEnv{},
		fetchKick: make(chan struct{}, 1)}, nil
}

// Enabled reports whether peer-to-peer is on.
func (s *Service) Enabled() bool { return s != nil && s.node != nil }

// Status is the node's status (zero when off).
func (s *Service) Status() p2p.Status {
	if !s.Enabled() {
		return p2p.Status{}
	}
	return s.node.Status()
}

// Start begins reading pairings, envelopes and peer events, and retries queued envelopes.
func (s *Service) Start() error {
	if !s.Enabled() {
		return nil
	}
	s.self()
	s.mu.Lock()
	s.retrustLocked()
	s.mu.Unlock()
	s.goTracked(s.pairedLoop)
	s.goTracked(s.incomingLoop)
	s.goTracked(s.eventsLoop)
	s.node.ServeFiles(s.serveFile)
	s.pruneUnsentFiles()
	s.goTracked(s.fetchLoop)
	peers, err := s.st.OutboxPeers()
	if err != nil {
		return err
	}
	for _, p := range peers {
		s.kick(p, true)
	}
	return nil
}

// Close stops the loops and workers (queued envelopes stay in the outbox).
func (s *Service) Close() {
	s.mu.Lock() // kick checks the context under mu, so no worker starts after this
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Service) goTracked(fn func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
}

func (s *Service) self() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selfLocked()
}

func (s *Service) selfLocked() string {
	if s.selfID == "" && s.node != nil {
		s.selfID = s.node.Status().PeerID
	}
	return s.selfID
}

func (s *Service) myName() string { return s.o.Name() }

func (s *Service) publish(typ string, data any) {
	s.ag.Publish(model.BusEvent{Type: typ, Data: data})
}

func (s *Service) pairedLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case p, ok := <-s.node.Paired():
			if !ok {
				return
			}
			if err := s.onPaired(p, false); err != nil {
				s.log.Warn("chat: pairing", "peer", p.PeerID, "err", err)
			}
		}
	}
}

func (s *Service) incomingLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case env, ok := <-s.node.Incoming():
			if !ok {
				return
			}
			if err := s.onEnvelope(env); err != nil {
				s.log.Info("chat: dropped envelope", "from", env.From, "type", env.Type, "chat", env.ChatID, "err", err)
			}
		}
	}
}

func (s *Service) eventsLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case ev, ok := <-s.node.PeerEvents():
			if !ok {
				return
			}
			if ev.Reachability != p2p.Offline {
				s.kick(ev.PeerID, true)
				s.peerOnlineForFiles(ev.PeerID)
			}
			if c, err := s.st.GetContact(ev.PeerID); err == nil {
				c.Reachability = string(ev.Reachability)
				s.publish(model.EvContactUpdated, c)
			}
		}
	}
}

// ---- errors (fleet errors, so the API maps them to statuses)

func errf(k fleet.Kind, format string, args ...any) error {
	return &fleet.Error{Kind: k, Msg: fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...any) error { return errf(fleet.KindNotFound, format, args...) }
func invalid(format string, args ...any) error  { return errf(fleet.KindInvalid, format, args...) }
func forbidden(format string, args ...any) error {
	return errf(fleet.KindForbidden, format, args...)
}

// offlineError: the other side could not be reached now (the API answers 503).
type offlineError struct{ msg string }

func (e offlineError) Error() string { return e.msg }
func (e offlineError) Unwrap() error { return p2p.ErrUnreachable }

// ---- rate limit per peer

type bucket struct {
	tokens float64
	last   time.Time
}

func (s *Service) allow(peer string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	b := s.buckets[peer]
	if b == nil {
		b = &bucket{tokens: rateBurst, last: now}
		s.buckets[peer] = b
	}
	b.tokens = min(rateBurst, b.tokens+now.Sub(b.last).Seconds()*ratePerSec)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
