package p2p

import (
	"time"

	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// connGater refuses inbound connections from strangers before any protocol runs on them
// (identify, ping, AutoNAT, hole punching, /tmx/*): libp2p negotiates its own protocols on
// every connection it accepts, so "paired peers only" in handleMsg/handleFile is not enough.
// The decision is made in InterceptSecured, the first hook that knows the remote's
// authenticated peer ID (InterceptAccept runs before the handshake). It is called for every
// inbound connection: TCP and relay circuits through the upgrader, QUIC from its listener,
// hole-punched ones in the "server" role included. Outbound dials are never gated: this
// node chooses whom it dials (paired peers, an inviter, DHT peers, relays).
//
// An inbound connection from peer p is allowed when (admit):
//
//  1. p is paired or trusted (a known peer: contacts and group co-members);
//  2. this node holds an outbound connection to p: it dialled p itself, e.g. the inviter it
//     is joining, whose hole punch or direct dial back must get through before the pairing
//     is recorded (p could open streams on that connection anyway);
//  3. an invite is open within two minutes of its creation, or this peer has proved
//     the invite and its pairing is being completed:
//     anyone may connect so that handlePair can check their proof (a stranger whose pairing
//     fails is disconnected then, see handlePair);
//  4. AutoNAT dial-back: a direct (not relayed) connection from a public IP address that
//     belongs to a peer this node is directly connected to and either dialled or is paired
//     with (that connection's remote IP, or one of the peer's known addresses). AutoNAT
//     servers dial back from a throwaway host with a random peer ID (go-libp2p v0.50:
//     config.makeAutoNATV2Host and the v1 service's dialer swarm in config.addAutoNAT both
//     generate a new key), at the server's IP, which is a peer AutoNAT picked among our
//     connected peers (autonatv2.pickServer, autonat.getPeerToProbe). v2 may dial an address
//     of the other IP family than the one we reached the server on, hence the peer's known
//     addresses. Refusing these would make AutoNAT conclude we are unreachable and fall back
//     to relays. Relay circuits never match: their address starts with the relay's IP.
//
// Everyone else is refused.
type connGater struct{ n *Host }

func (g connGater) InterceptPeerDial(peer.ID) bool               { return true }
func (g connGater) InterceptAddrDial(peer.ID, ma.Multiaddr) bool { return true }
func (g connGater) InterceptAccept(network.ConnMultiaddrs) bool  { return true }
func (g connGater) InterceptUpgraded(network.Conn) (bool, control.DisconnectReason) {
	return true, 0
}

func (g connGater) InterceptSecured(dir network.Direction, p peer.ID, c network.ConnMultiaddrs) bool {
	if dir == network.DirOutbound {
		return true
	}
	ok := g.n.admit(p, c.RemoteMultiaddr(), g.n.gateView())
	if !ok {
		g.n.log.Debug("p2p: refused inbound connection", "peer", p.String(), "addr", c.RemoteMultiaddr().String())
	}
	return ok
}

// gateConn is one current connection, as the gater sees it.
type gateConn struct {
	peer     peer.ID
	remote   ma.Multiaddr
	outbound bool
}

// gateNet is what the gater reads from the libp2p host (a fake in unit tests).
type gateNet interface {
	conns() []gateConn
	addrs(p peer.ID) []ma.Multiaddr
}

type hostNet struct{ h host.Host }

func (v hostNet) conns() []gateConn {
	cs := v.h.Network().Conns()
	out := make([]gateConn, 0, len(cs))
	for _, c := range cs {
		out = append(out, gateConn{peer: c.RemotePeer(), remote: c.RemoteMultiaddr(), outbound: c.Stat().Direction == network.DirOutbound})
	}
	return out
}

func (v hostNet) addrs(p peer.ID) []ma.Multiaddr { return v.h.Peerstore().Addrs(p) }

// gateView is the running host's view, or nil while libp2p.New has not returned yet (its
// listeners may already accept): then only rules 1 and 3 apply.
func (n *Host) gateView() gateNet {
	if !n.hostReady.Load() {
		return nil
	}
	return hostNet{n.h}
}

// admit is the gater's rule for an inbound connection from p at remote (see connGater).
func (n *Host) admit(p peer.ID, remote ma.Multiaddr, v gateNet) bool {
	n.mu.Lock()
	_, known := n.st.Peers[p.String()]
	open := known || n.pairing[p] > 0 || n.st.inviteOpen(nowMs())
	n.mu.Unlock()
	if open {
		return true
	}
	if v == nil {
		return false
	}
	conns := v.conns()
	for _, c := range conns {
		if c.peer == p && c.outbound {
			return true
		}
	}
	return n.autonatDialBack(remote, conns, v)
}

// autonatDialBack is rule 4 of connGater: remote is a direct connection from a public IP of
// a peer we are directly connected to and either dialled or are paired with.
func (n *Host) autonatDialBack(remote ma.Multiaddr, conns []gateConn, v gateNet) bool {
	if !n.opts.DHT && len(n.opts.Relays) == 0 {
		return false
	}
	if remote == nil || isRelayAddr(remote) || !manet.IsPublicAddr(remote) {
		return false
	}
	ip, err := manet.ToIP(remote)
	if err != nil {
		return false
	}
	sameIP := func(a ma.Multiaddr) bool {
		if isRelayAddr(a) {
			return false
		}
		aip, err := manet.ToIP(a)
		return err == nil && aip.Equal(ip)
	}
	seen := map[peer.ID]bool{}
	for _, c := range conns {
		if isRelayAddr(c.remote) || seen[c.peer] {
			continue
		}
		if !c.outbound && !n.isPeer(c.peer) {
			continue // an unpaired peer that connected to us vouches for nobody
		}
		seen[c.peer] = true
		if sameIP(c.remote) {
			return true
		}
		for _, a := range v.addrs(c.peer) {
			if sameIP(a) {
				return true
			}
		}
	}
	return false
}

// inviteOpen reports whether any invite can still be used.
func (s *state) inviteOpen(now int64) bool {
	for _, in := range s.Invites {
		if in.open(now) && in.CreatedAt > 0 && now >= in.CreatedAt && now-in.CreatedAt < int64(2*time.Minute/time.Millisecond) {
			return true
		}
	}
	return false
}

// sweepStrangers re-runs the gater over the current inbound connections and closes those
// it would refuse now. A stranger let in while an invite was open that never opened a pair
// stream is not dropped by handlePair, so without this it would keep its connection after
// the invite is used, revoked or past its admission window. Called on every redial tick,
// after a revoke and after each pair stream; the caller must not hold n.mu.
func (n *Host) sweepStrangers() {
	if !n.hostReady.Load() {
		return
	}
	v := hostNet{n.h}
	for _, c := range n.h.Network().Conns() {
		if c.Stat().Direction != network.DirInbound {
			// Outbound connections are ours to choose (inviter, relays, DHT) and stay open.
			// One to a non-peer at a direct address is logged once, so a hole punch toward a
			// stranger would show up in real-network runs (loopback tests cannot punch).
			p := c.RemotePeer()
			if !isRelayAddr(c.RemoteMultiaddr()) && !n.isPeer(p) {
				if n.firstStrangerOut(p) {
					n.log.Debug("p2p: outbound connection to a non-peer", "peer", p.String(), "addr", c.RemoteMultiaddr().String())
				}
			}
			continue
		}
		if !n.admit(c.RemotePeer(), c.RemoteMultiaddr(), v) {
			n.log.Debug("p2p: closed a stranger's connection", "peer", c.RemotePeer().String())
			_ = c.Close()
		}
	}
}

const maxStrangerOut = 256

// firstStrangerOut reports whether p is new to strangerOut, and records it.
func (n *Host) firstStrangerOut(p peer.ID) bool {
	n.strangerOutMu.Lock()
	defer n.strangerOutMu.Unlock()
	if _, ok := n.strangerOut[p]; ok {
		return false
	}
	if n.strangerOut == nil || len(n.strangerOut) >= maxStrangerOut {
		n.strangerOut = make(map[peer.ID]struct{})
	}
	n.strangerOut[p] = struct{}{}
	return true
}

// dropStranger closes p's inbound connections unless p is a known peer: called when a
// pairing stream ends without pairing, so a stranger let in for an invite does not stay.
func (n *Host) dropStranger(p peer.ID) {
	if n.isPeer(p) {
		return
	}
	for _, c := range n.h.Network().ConnsToPeer(p) {
		if c.Stat().Direction == network.DirInbound {
			_ = c.Close()
		}
	}
}
