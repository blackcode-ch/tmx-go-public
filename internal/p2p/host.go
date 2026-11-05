package p2p

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	noise "github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// Protocol IDs (DESIGN.md "Peer-to-peer network").
const (
	ProtoPair = "/tmx/pair/1.0.0"
	ProtoMsg  = "/tmx/msg/1.0.0"
	ProtoFile = "/tmx/file/1.0.0"
)

const (
	mdnsService     = "_tmx-p2p._udp"
	contactTag      = "tmx-peer"
	dialTimeout     = 15 * time.Second
	findPeerTimeout = 30 * time.Second
	defaultDMTTL    = 24 * time.Hour
	defaultGroupTTL = 24 * time.Hour
	maxTTL          = 90 * 24 * time.Hour
)

// Options configures a Host. Zero values are the daemon defaults except where noted.
type Options struct {
	Key       crypto.PrivKey // required: the install's identity (LoadOrCreateKey)
	StateFile string         // persisted name, port, invites, peers; "" = memory only (tests)
	Name      string         // display name when the state file has none yet
	Version   string         // shown to other peers as the identify user agent tmx/<version>

	// ListenAddrs replaces the default listeners (QUIC + TCP on loopback, IPv4 and IPv6,
	// on the remembered port). Tests pass loopback addresses.
	ListenAddrs []string
	Port        int // fixed port for the default listeners; 0 = remembered, else random

	DHT        bool     // join the public IPFS DHT (client mode) for relays and FindPeer
	MDNS       bool     // find paired peers on the same LAN (runs only while there is one)
	NATPortMap bool     // ask the router for a port mapping (UPnP / NAT-PMP)
	Relays     []string // static relay multiaddrs (with /p2p/<id>), used besides DHT-found ones
	Bootstrap  []string // extra DHT bootstrap peers (added to the IPFS defaults)

	// AllowLoopback lets invites carry 127.0.0.1 / ::1 addresses (tests, two daemons on one machine).
	AllowLoopback bool
	// RetryInterval is how often offline paired peers are dialled (default 1 min, backing off to 10).
	RetryInterval time.Duration
	Logger        *slog.Logger

	// newMDNS builds the mDNS service (default mdns.NewMdnsService); tests inject a fake so
	// they never announce on the real LAN.
	newMDNS func(host.Host, string, mdns.Notifee) mdns.Service
}

// Host is the daemon's Node: a libp2p host with tmx's protocols.
type Host struct {
	opts Options
	h    host.Host
	cm   *connmgr.BasicConnMgr
	kad  *dht.IpfsDHT
	log  *slog.Logger
	self peer.ID
	key  crypto.PrivKey

	// hostReady is set once h is assigned: the connection gater may run before (libp2p.New
	// starts the listeners) and must not touch h until then.
	hostReady atomic.Bool
	// strangerOut holds the non-peers sweepStrangers has logged an outbound connection to
	// (at most maxStrangerOut; cleared when full, so a peer may be logged again).
	strangerOutMu sync.Mutex
	strangerOut   map[peer.ID]struct{}

	// mDNS runs only while there is a paired peer (syncMDNS); mdnsMu orders starts and stops.
	mdnsMu sync.Mutex
	md     mdns.Service

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	st        *state
	reach     network.Reachability
	limiter   *rateLimiter
	closed    bool
	pairReady map[peer.ID]*pairProgress // Join and proof-checked inbound pair streams
	pairing   map[peer.ID]int           // proof-checked peers awaiting pairing commit (gater.go)
	lastConn  map[peer.ID]Reachability
	nextDial  map[peer.ID]time.Time
	backoff   map[peer.ID]time.Duration
	lookedUp  map[peer.ID]time.Time // last background DHT lookup per peer

	files       FileSource      // answers /tmx/file/1.0.0; nil refuses all (file.go)
	filesActive map[peer.ID]int // file streams being served, per peer
	filesTotal  int

	paired   chan Paired
	incoming chan Envelope
	events   chan PeerEvent
}

var _ Node = (*Host)(nil)

// New starts the host. It returns once the listeners are up; DHT bootstrap, relay discovery
// and reconnecting to paired peers continue in the background.
func New(o Options) (*Host, error) {
	if o.Key == nil {
		return nil, errors.New("p2p: Options.Key is required")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.RetryInterval <= 0 {
		o.RetryInterval = time.Minute
	}
	if o.newMDNS == nil {
		o.newMDNS = func(h host.Host, service string, nf mdns.Notifee) mdns.Service {
			return mdns.NewMdnsService(h, service, nf)
		}
	}
	st, err := loadState(o.StateFile)
	if err != nil {
		return nil, err
	}
	if st.Name == "" {
		st.Name = strings.TrimSpace(o.Name)
	}
	self, err := peer.IDFromPrivateKey(o.Key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := &Host{
		opts: o, log: o.Logger, self: self, key: o.Key, st: st,
		ctx: ctx, cancel: cancel,
		limiter:     newRateLimiter(),
		lastConn:    map[peer.ID]Reachability{},
		nextDial:    map[peer.ID]time.Time{},
		backoff:     map[peer.ID]time.Duration{},
		lookedUp:    map[peer.ID]time.Time{},
		filesActive: map[peer.ID]int{},
		paired:      make(chan Paired, 64),
		incoming:    make(chan Envelope, 256),
		events:      make(chan PeerEvent, 256),
	}
	if err := n.start(); err != nil {
		cancel()
		return nil, err
	}
	return n, nil
}

func (n *Host) listenAddrs(port int) []string {
	if len(n.opts.ListenAddrs) > 0 {
		return n.opts.ListenAddrs
	}
	p := fmt.Sprint(port)
	return []string{
		"/ip4/127.0.0.1/udp/" + p + "/quic-v1",
		"/ip4/127.0.0.1/tcp/" + p,
		"/ip6/::1/udp/" + p + "/quic-v1",
		"/ip6/::1/tcp/" + p,
	}
}

func (n *Host) start() error {
	o := n.opts
	// Resource limits: libp2p's defaults scaled down to a small budget (this runs on laptops
	// next to the agents), plus tight limits for tmx's own protocols.
	limits := rcmgr.DefaultLimits
	libp2p.SetDefaultServiceLimits(&limits)
	small := rcmgr.BaseLimit{Streams: 64, StreamsInbound: 32, StreamsOutbound: 32, Memory: 8 << 20}
	perPeer := rcmgr.BaseLimit{Streams: 8, StreamsInbound: 4, StreamsOutbound: 4, Memory: 1 << 20}
	for _, p := range []string{ProtoPair, ProtoMsg, ProtoFile} {
		limits.AddProtocolLimit(protoID(p), small, rcmgr.BaseLimitIncrease{})
		limits.AddProtocolPeerLimit(protoID(p), perPeer, rcmgr.BaseLimitIncrease{})
	}
	rm, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(limits.Scale(96<<20, 256)))
	if err != nil {
		return err
	}
	cm, err := connmgr.NewConnManager(8, 24, connmgr.WithGracePeriod(30*time.Second))
	if err != nil {
		return err
	}
	n.cm = cm

	var static []peer.AddrInfo
	for _, s := range o.Relays {
		ai, err := peer.AddrInfoFromString(s)
		if err != nil {
			return fmt.Errorf("p2p relay %q: %w", s, err)
		}
		static = append(static, *ai)
	}

	opts := []libp2p.Option{
		libp2p.Identity(n.key),
		libp2p.UserAgent("tmx/" + o.Version),
		libp2p.Transport(quic.NewTransport),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		libp2p.ResourceManager(rm),
		libp2p.ConnectionManager(cm),
		libp2p.ConnectionGater(connGater{n}), // strangers cannot connect (gater.go)

	}
	if o.NATPortMap {
		opts = append(opts, libp2p.NATPortMap())
	}
	if o.DHT || len(static) > 0 {
		opts = append(opts, libp2p.EnableRelay(), libp2p.EnableHolePunching())
		// AutoRelay engages only when AutoNAT finds us unreachable from outside. Candidates:
		// the static relays, then peers from the DHT routing table that offer relay v2 (most
		// public IPFS nodes do, with small per-circuit limits).
		src := func(ctx context.Context, num int) <-chan peer.AddrInfo {
			ch := make(chan peer.AddrInfo, num)
			go func() {
				defer close(ch)
				sent := 0
				for _, ai := range static {
					if sent >= num {
						return
					}
					ch <- ai
					sent++
				}
				n.mu.Lock()
				kad := n.kad
				n.mu.Unlock()
				if kad == nil {
					return
				}
				peers := kad.RoutingTable().ListPeers()
				mrand(peers)
				for _, p := range peers {
					if sent >= num {
						return
					}
					addrs := n.h.Peerstore().Addrs(p)
					if len(addrs) == 0 {
						continue
					}
					select {
					case ch <- peer.AddrInfo{ID: p, Addrs: addrs}:
						sent++
					case <-ctx.Done():
						return
					}
				}
			}()
			return ch
		}
		opts = append(opts, libp2p.EnableAutoRelayWithPeerSource(src), libp2p.EnableAutoNATv2())
	} else {
		opts = append(opts, libp2p.DisableRelay())
	}

	port := o.Port
	if port == 0 && len(o.ListenAddrs) == 0 {
		port = pickPort(n.st.Port)
	}
	h, err := libp2p.New(append(opts, libp2p.ListenAddrStrings(n.listenAddrs(port)...))...)
	if err != nil {
		rm.Close()
		return fmt.Errorf("p2p: start host: %w", err)
	}
	n.h = h
	n.hostReady.Store(true)
	if len(o.ListenAddrs) == 0 && o.Port == 0 {
		if p := listenPort(h); p != 0 && p != n.st.Port {
			n.mu.Lock()
			n.st.Port = p
			n.saveLocked()
			n.mu.Unlock()
		}
	}

	h.SetStreamHandler(protoID(ProtoPair), n.handlePair)
	h.SetStreamHandler(protoID(ProtoMsg), n.handleMsg)
	h.SetStreamHandler(protoID(ProtoFile), n.handleFile)

	n.mu.Lock()
	n.st.pruneInvites(nowMs())
	for id, p := range n.st.Peers {
		pid, err := peer.Decode(id)
		if err != nil {
			continue
		}
		n.rememberAddrsLocked(pid, p.Addrs)
		cm.Protect(pid, contactTag)
	}
	n.mu.Unlock()

	sub, err := h.EventBus().Subscribe([]any{
		new(event.EvtPeerConnectednessChanged),
		new(event.EvtLocalReachabilityChanged),
		new(event.EvtLocalAddressesUpdated),
	})
	if err != nil {
		h.Close()
		return err
	}
	n.wg.Add(1)
	go n.eventLoop(sub)
	n.logAddrs()

	if o.DHT {
		if err := n.startDHT(); err != nil {
			n.log.Warn("p2p: DHT", "err", err)
		}
	}
	n.syncMDNS()
	n.wg.Add(1)
	go n.redialLoop()
	n.log.Info("p2p node started", "peer_id", n.self.String(), "addrs", n.addrStrings(h.Addrs()))
	return nil
}

func (n *Host) startDHT() error {
	boot := dht.GetDefaultBootstrapPeerAddrInfos()
	for _, s := range n.opts.Bootstrap {
		ai, err := peer.AddrInfoFromString(s)
		if err != nil {
			return fmt.Errorf("bootstrap peer %q: %w", s, err)
		}
		boot = append(boot, *ai)
	}
	kad, err := dht.New(n.h, dht.Mode(dht.ModeClient), dht.BootstrapPeers(boot...))
	if err != nil {
		return err
	}
	n.mu.Lock()
	n.kad = kad
	n.mu.Unlock()
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		// Connect to a few bootstrap peers ourselves, then let the DHT fill its table.
		var wg sync.WaitGroup
		for _, ai := range boot {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(n.ctx, dialTimeout)
				defer cancel()
				_ = n.h.Connect(ctx, ai)
			}()
		}
		wg.Wait()
		if err := kad.Bootstrap(n.ctx); err != nil && n.ctx.Err() == nil {
			n.log.Warn("p2p: DHT bootstrap", "err", err)
		}
	}()
	return nil
}

// syncMDNS runs mDNS while there is at least one paired (or trusted) peer and stops it
// when the last one is forgotten: it is only used to find those peers on the LAN again, so
// a node without contacts does not announce itself there. Options.MDNS false keeps it off.
func (n *Host) syncMDNS() {
	if !n.opts.MDNS {
		return
	}
	n.mdnsMu.Lock()
	defer n.mdnsMu.Unlock()
	n.mu.Lock()
	want := len(n.st.Peers) > 0 && !n.closed
	n.mu.Unlock()
	switch {
	case want && n.md == nil:
		md := n.opts.newMDNS(n.h, mdnsService, mdnsNotifee{n})
		if err := md.Start(); err != nil {
			n.log.Warn("p2p: mDNS", "err", err)
			return
		}
		n.md = md
		n.log.Info("p2p: mDNS on (paired peers to find on the LAN)")
	case !want && n.md != nil:
		_ = n.md.Close()
		n.md = nil
		n.log.Info("p2p: mDNS off (no paired peers)")
	}
}

type mdnsNotifee struct{ n *Host }

// HandlePeerFound: a tmx install on the same LAN. Only its addresses are kept; it is dialled
// only when it is a paired peer (mDNS never makes a stranger a contact).
func (m mdnsNotifee) HandlePeerFound(ai peer.AddrInfo) {
	n := m.n
	if ai.ID == n.self {
		return
	}
	n.mu.Lock()
	_, known := n.st.Peers[ai.ID.String()]
	n.mu.Unlock()
	n.h.Peerstore().AddAddrs(ai.ID, ai.Addrs, peerstore.TempAddrTTL)
	if known && n.h.Network().Connectedness(ai.ID) != network.Connected {
		n.goDial(ai.ID)
	}
}

func protoID(s string) protocolID { return protocolID(s) }

func nowMs() int64 { return time.Now().UnixMilli() }

// pickPort returns the remembered port if UDP and TCP are both free on it, else a free
// random one in 20000-59999 (the same for QUIC and TCP, so invites stay short and a router
// mapping covers both), else 0 (let the OS pick).
func pickPort(remembered int) int { return pickPortOn("127.0.0.1", remembered) }

// pickPortProbeCheck, when non-nil, runs on every socket pickPortOn's probe opens, before it
// is closed. Tests set this (see TestPickPort) to assert each probe socket is loopback-only;
// nil in production, so there is no overhead and no behavior change there.
var pickPortProbeCheck func(net.Addr)

// pickPortOn is pickPort with an injectable probe host: the daemon and tests pass "127.0.0.1"
// so the probe itself never binds 0.0.0.0 and never triggers a firewall prompt.
func pickPortOn(host string, remembered int) int {
	free := func(p int) bool {
		l, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, p))
		if err != nil {
			return false
		}
		if pickPortProbeCheck != nil {
			pickPortProbeCheck(l.Addr())
		}
		l.Close()
		u, err := net.ListenPacket("udp", fmt.Sprintf("%s:%d", host, p))
		if err != nil {
			return false
		}
		if pickPortProbeCheck != nil {
			pickPortProbeCheck(u.LocalAddr())
		}
		u.Close()
		return true
	}
	if remembered > 0 && free(remembered) {
		return remembered
	}
	for range 8 {
		var b [2]byte
		_, _ = rand.Read(b[:])
		p := 20000 + (int(b[0])<<8|int(b[1]))%40000
		if free(p) {
			return p
		}
	}
	return 0
}

func listenPort(h host.Host) int {
	for _, a := range h.Network().ListenAddresses() {
		for _, code := range []int{ma.P_UDP, ma.P_TCP} {
			if v, err := a.ValueForProtocol(code); err == nil && v != "0" {
				var p int
				fmt.Sscan(v, &p)
				if p > 0 {
					return p
				}
			}
		}
	}
	return 0
}

func mrand[T any](s []T) {
	for i := len(s) - 1; i > 0; i-- {
		var b [1]byte
		_, _ = rand.Read(b[:])
		j := int(b[0]) % (i + 1)
		s[i], s[j] = s[j], s[i]
	}
}

func (n *Host) saveLocked() {
	if err := n.st.save(); err != nil {
		n.log.Error("p2p: save state", "err", err)
	}
}

func (n *Host) rememberAddrsLocked(p peer.ID, addrs []string) {
	var mas []ma.Multiaddr
	for _, s := range addrs {
		if a, err := ma.NewMultiaddr(s); err == nil {
			mas = append(mas, a)
		}
	}
	if len(mas) > 0 {
		n.h.Peerstore().AddAddrs(p, mas, peerstore.PermanentAddrTTL)
	}
}

// ---------------------------------------------------------------------------------------
// Status and reachability

// Status reports this node: its peer ID, the addresses an invite would carry, and how the
// internet can reach it (DESIGN.md: direct = public or port-mapped address, relayed = only
// through a circuit relay, offline = no usable address or AutoNAT found us unreachable and
// no relay reservation yet).
func (n *Host) Status() Status {
	addrs := n.inviteAddrs()
	return Status{
		Enabled: true, PeerID: n.self.String(), Addrs: n.addrStrings(addrs),
		Reachability: n.selfReachability(), Peers: len(n.h.Network().Peers()),
	}
}

func (n *Host) selfReachability() Reachability {
	n.mu.Lock()
	r := n.reach
	n.mu.Unlock()
	addrs := n.h.Addrs()
	hasRelay, hasLAN, hasPublic := false, false, false
	for _, a := range addrs {
		switch {
		case isRelayAddr(a):
			hasRelay = true
		case manet.IsPublicAddr(a):
			hasPublic = true
		case !manet.IsIPLoopback(a):
			hasLAN = true
		}
	}
	switch {
	case r == network.ReachabilityPublic:
		return Direct
	case hasRelay:
		return Relayed
	case r == network.ReachabilityPrivate:
		return Offline // behind NAT, no relay reservation yet
	case hasPublic:
		return Direct // not determined yet, but a public address is a good sign
	case !n.opts.DHT && (hasLAN || (n.opts.AllowLoopback && len(addrs) > 0)):
		return Direct // LAN-only mode (no DHT/AutoNAT): reachable on the networks we listen on
	}
	return Offline // AutoNAT has not decided yet (the first ~20-60 s), or no usable address
}

// Reachable reports how a peer is connected right now.
func (n *Host) Reachable(peerID string) Reachability {
	p, err := peer.Decode(peerID)
	if err != nil {
		return Offline
	}
	return n.peerReach(p)
}

func (n *Host) peerReach(p peer.ID) Reachability {
	conns := n.h.Network().ConnsToPeer(p)
	if len(conns) == 0 {
		return Offline
	}
	for _, c := range conns {
		if !c.Stat().Limited && !isRelayAddr(c.RemoteMultiaddr()) {
			return Direct
		}
	}
	return Relayed
}

func isRelayAddr(a ma.Multiaddr) bool {
	_, err := a.ValueForProtocol(ma.P_CIRCUIT)
	return err == nil
}

// logAddrs reports the address set an invite would carry right now, so a relay reservation
// (a /p2p-circuit address, once AutoRelay engages) shows up in the log the moment it appears -
// the thing to grep for when checking whether a machine behind NAT has become reachable at all.
func (n *Host) logAddrs() {
	addrs := n.addrStrings(n.inviteAddrs())
	n.log.Info("p2p addrs", "addrs", addrs, "reachability", n.selfReachability())
}

func (n *Host) addrStrings(addrs []ma.Multiaddr) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

// inviteAddrs ranks this node's addresses for an invite: public direct (QUIC first), relay
// circuits, then LAN; loopback only when allowed; at most maxInviteAddrs.
func (n *Host) inviteAddrs() []ma.Multiaddr {
	type ranked struct {
		a    ma.Multiaddr
		rank int
	}
	var rs []ranked
	seen := map[string]bool{}
	lan, relays := 0, 0
	for _, a := range n.h.Addrs() {
		if seen[a.String()] {
			continue
		}
		seen[a.String()] = true
		if !dialable(a) {
			continue
		}
		quicAddr := strings.Contains(a.String(), "/quic-v1")
		r := 0
		switch {
		case isRelayAddr(a):
			if relays >= 2 {
				continue
			}
			relays++
			r = 20
		case manet.IsIPLoopback(a):
			if !n.opts.AllowLoopback {
				continue
			}
			r = 40
		case isLinkLocal(a):
			continue
		case manet.IsPublicAddr(a):
			r = 0
		default:
			if lan >= 4 {
				continue
			}
			lan++
			r = 30
		}
		if !quicAddr {
			r += 5
		}
		if strings.HasPrefix(a.String(), "/ip6") {
			r += 2
		}
		rs = append(rs, ranked{a, r})
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].rank < rs[j].rank })
	out := make([]ma.Multiaddr, 0, maxInviteAddrs)
	for _, r := range rs {
		if len(out) == maxInviteAddrs {
			break
		}
		out = append(out, r.a)
	}
	return out
}

// dialable: tmx nodes speak QUIC v1 and TCP only, so an address over anything else (e.g. a
// relay's legacy /quic draft-29 or /ws address) is useless in an invite.
func dialable(a ma.Multiaddr) bool {
	ok := true
	ma.ForEach(a, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_QUIC, ma.P_WS, ma.P_WSS, ma.P_WEBTRANSPORT, ma.P_WEBRTC_DIRECT, ma.P_WEBRTC:
			ok = false
		}
		return ok
	})
	return ok
}

func isLinkLocal(a ma.Multiaddr) bool {
	ip, err := manet.ToIP(a)
	return err == nil && (ip.IsLinkLocalUnicast() || ip.Equal(net.IPv6unspecified))
}

// ---------------------------------------------------------------------------------------
// events, reconnects

func (n *Host) eventLoop(sub event.Subscription) {
	defer n.wg.Done()
	defer sub.Close()
	for {
		select {
		case <-n.ctx.Done():
			return
		case ev, ok := <-sub.Out():
			if !ok {
				return
			}
			switch e := ev.(type) {
			case event.EvtLocalReachabilityChanged:
				n.mu.Lock()
				n.reach = e.Reachability
				n.mu.Unlock()
				n.log.Info("p2p reachability", "reachability", e.Reachability.String())
			case event.EvtLocalAddressesUpdated:
				// New addresses (a relay reservation, a port mapping): try offline peers again.
				n.mu.Lock()
				for p := range n.nextDial {
					n.nextDial[p] = time.Time{}
				}
				n.mu.Unlock()
				n.logAddrs()
			case event.EvtPeerConnectednessChanged:
				n.peerChanged(e.Peer)
			}
		}
	}
}

// peerChanged emits a PeerEvent for a paired peer when its reachability changed.
func (n *Host) peerChanged(p peer.ID) {
	r := n.peerReach(p)
	n.mu.Lock()
	rec, ok := n.st.Peers[p.String()]
	if !ok || n.closed {
		n.mu.Unlock()
		return
	}
	if n.lastConn[p] == r {
		n.mu.Unlock()
		return
	}
	n.lastConn[p] = r
	if r != Offline {
		rec.LastSeen = nowMs()
		delete(n.backoff, p)
		delete(n.nextDial, p)
		if addrs := n.addrStrings(n.h.Peerstore().Addrs(p)); len(addrs) > 0 {
			rec.Addrs = trimAddrs(addrs)
		}
		n.saveLocked()
	}
	n.mu.Unlock()
	select {
	case n.events <- PeerEvent{PeerID: p.String(), Reachability: r}:
	default:
		n.log.Debug("p2p: PeerEvents full, dropped", "peer", p.String())
	}
}

// trimAddrs keeps at most 8 addresses, skipping loopback unless nothing else is known.
func trimAddrs(in []string) []string {
	var out, loop []string
	for _, s := range in {
		if strings.HasPrefix(s, "/ip4/127.") || strings.HasPrefix(s, "/ip6/::1/") {
			loop = append(loop, s)
			continue
		}
		out = append(out, s)
	}
	out = append(out, loop...)
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// redialLoop dials paired peers that are offline, with a per-peer backoff (RetryInterval,
// doubling to 10×). Both sides do this, so whichever comes online first finds the other and
// both see PeerEvents (the chat layer then sends what it queued).
func (n *Host) redialLoop() {
	defer n.wg.Done()
	t := time.NewTicker(n.opts.RetryInterval / 4)
	defer t.Stop()
	first := time.After(500 * time.Millisecond)
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-first:
		case <-t.C:
		}
		n.sweepStrangers()
		now := time.Now()
		var due []peer.ID
		n.mu.Lock()
		for id := range n.st.Peers {
			p, err := peer.Decode(id)
			if err != nil || n.h.Network().Connectedness(p) == network.Connected {
				continue
			}
			if now.Before(n.nextDial[p]) {
				continue
			}
			b := n.backoff[p]
			if b == 0 {
				b = n.opts.RetryInterval
			} else {
				b = min(b*2, 10*n.opts.RetryInterval)
			}
			n.backoff[p] = b
			n.nextDial[p] = now.Add(b)
			due = append(due, p)
		}
		n.mu.Unlock()
		for _, p := range due {
			n.goDial(p)
		}
	}
}

// bgLookupEvery limits background DHT lookups for one offline peer: a FindPeer for a peer
// that is not online walks the DHT, which costs real CPU and traffic.
const bgLookupEvery = 30 * time.Minute

// goDial tries to reach a paired peer in the background: known addresses each time, a DHT
// lookup at most every bgLookupEvery.
func (n *Host) goDial(p peer.ID) {
	n.mu.Lock()
	lookup := time.Since(n.lookedUp[p]) > bgLookupEvery
	if lookup {
		n.lookedUp[p] = time.Now()
	}
	n.mu.Unlock()
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		ctx, cancel := context.WithTimeout(n.ctx, dialTimeout+findPeerTimeout)
		defer cancel()
		_ = n.dial(ctx, p, lookup)
	}()
}

// connect makes sure there is a connection to p: known addresses first, then a DHT lookup.
func (n *Host) connect(ctx context.Context, p peer.ID) error { return n.dial(ctx, p, true) }

func (n *Host) dial(ctx context.Context, p peer.ID, lookup bool) error {
	if n.h.Network().Connectedness(p) == network.Connected {
		return nil
	}
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	err := n.h.Connect(dctx, peer.AddrInfo{ID: p, Addrs: n.h.Peerstore().Addrs(p)})
	cancel()
	if err == nil {
		return nil
	}
	n.mu.Lock()
	kad := n.kad
	n.mu.Unlock()
	if kad == nil || !lookup {
		return err
	}
	fctx, cancel := context.WithTimeout(ctx, findPeerTimeout)
	defer cancel()
	ai, ferr := kad.FindPeer(fctx, p)
	if ferr != nil {
		return err
	}
	dctx, cancel2 := context.WithTimeout(ctx, dialTimeout)
	defer cancel2()
	return n.h.Connect(dctx, ai)
}

// ---------------------------------------------------------------------------------------
// names, peers

func (n *Host) SetDisplayName(name string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	name = truncUTF8(strings.TrimSpace(name), maxNameBytes)
	if name == n.st.Name {
		return
	}
	n.st.Name = name
	n.saveLocked()
}

// DisplayName is the name this node gives in invites and pairings.
func (n *Host) DisplayName() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.st.Name
}

// PeerID is this node's peer ID.
func (n *Host) PeerID() string { return n.self.String() }

// Trust accepts envelopes from a peer this node did not pair with itself (a co-member of a
// group, vouched for by the group's admin) and remembers addresses to reach it; Send then
// dials it from those addresses, the peerstore or a DHT lookup. Trusting a peer that is
// already paired only refreshes its name and addresses. Forget undoes it.
func (n *Host) Trust(peerID, name string, addrs []string) error {
	p, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("not a peer ID: %q", peerID)
	}
	if p == n.self {
		return nil
	}
	defer n.syncMDNS()
	n.mu.Lock()
	defer n.mu.Unlock()
	rec, ok := n.st.Peers[p.String()]
	if !ok {
		rec = &peerRec{PeerID: p.String(), PairedAt: nowMs(), Introduced: true}
		n.st.Peers[p.String()] = rec
	}
	if name != "" {
		rec.Name = truncUTF8(name, maxNameBytes)
	}
	if len(addrs) > 0 {
		var valid []string
		for _, a := range addrs {
			if _, err := ma.NewMultiaddr(a); err == nil {
				valid = append(valid, a)
			}
		}
		if len(valid) > 0 {
			rec.Addrs = trimAddrs(valid)
			n.rememberAddrsLocked(p, rec.Addrs)
		}
	}
	n.cm.Protect(p, contactTag)
	n.saveLocked()
	return nil
}

// PeerAddrs returns the addresses this node knows for a peer (for introducing it to others).
func (n *Host) PeerAddrs(peerID string) []string {
	p, err := peer.Decode(peerID)
	if err != nil {
		return nil
	}
	return trimAddrs(n.addrStrings(n.h.Peerstore().Addrs(p)))
}

// Forget drops a peer: its envelopes are refused from now on and it is no longer dialled.
// Its connections are closed before Forget returns: the connection gater refuses it from
// now on (unless an invite is open), and sweepStrangers would close a surviving inbound one
// on its next tick anyway, possibly mid-stream. Closing here makes the outcome of the
// peer's next Send deterministic (unreachable: gated) instead of racing that tick.
func (n *Host) Forget(peerID string) {
	p, err := peer.Decode(peerID)
	if err != nil {
		return
	}
	n.mu.Lock()
	_, ok := n.st.Peers[p.String()]
	delete(n.st.Peers, p.String())
	delete(n.lastConn, p)
	delete(n.nextDial, p)
	delete(n.backoff, p)
	delete(n.lookedUp, p)
	if ok {
		n.saveLocked()
	}
	n.mu.Unlock()
	n.cm.Unprotect(p, contactTag)
	_ = n.h.Network().ClosePeer(p)
	n.syncMDNS()
}

func (n *Host) isPeer(p peer.ID) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.st.Peers[p.String()]
	return ok
}

// Peers lists the paired (and introduced) peer IDs.
func (n *Host) Peers() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.st.Peers))
	for id := range n.st.Peers {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func (n *Host) Paired() <-chan Paired        { return n.paired }
func (n *Host) Incoming() <-chan Envelope    { return n.incoming }
func (n *Host) PeerEvents() <-chan PeerEvent { return n.events }

// Close stops the host. The channels are not closed (readers stop on their own context).
func (n *Host) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	n.mu.Unlock()
	n.cancel()
	n.mdnsMu.Lock()
	if n.md != nil {
		n.md.Close()
		n.md = nil
	}
	n.mdnsMu.Unlock()
	if n.kad != nil {
		n.kad.Close()
	}
	err := n.h.Close()
	n.wg.Wait()
	return err
}
