package p2p

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-msgio"
)

type protocolID = protocol.ID

const (
	maxPairMsg    = 4 << 10
	streamTimeout = 20 * time.Second
	pairedWait    = 10 * time.Second // how long the inviter waits for the Paired reader
)

// pairRequest / pairResponse are the /tmx/pair/1.0.0 messages: one varint-length-prefixed
// JSON object each way, then the stream closes.
type pairRequest struct {
	V     int    `json:"v"`
	Proof []byte `json:"proof"` // pairProof(secret, inviter, joiner)
	Name  string `json:"name"`
}

type pairResponse struct {
	OK        bool       `json:"ok"`
	Error     string     `json:"error,omitempty"`
	Name      string     `json:"name,omitempty"`
	Kind      InviteKind `json:"kind,omitempty"`
	GroupID   string     `json:"group_id,omitempty"`
	GroupName string     `json:"group_name,omitempty"`
}

func writeMsg(s network.Stream, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return msgio.NewVarintWriter(s).WriteMsg(b)
}

func readMsg(s network.Stream, limit int, v any) error {
	r := msgio.NewVarintReaderSize(s, limit)
	b, err := r.ReadMsg()
	if err != nil {
		return err
	}
	defer r.ReleaseMsg(b)
	return json.Unmarshal(b, v)
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------------------
// invites

// CreateInvite makes an invite link for this node. DM: single use, 24 h. Group: any number
// of uses only when MaxUses is explicitly set; the default is one use and 24 hours.
func (n *Host) CreateInvite(o InviteOptions) (Invite, error) {
	switch o.Kind {
	case InviteDM:
		o.GroupID, o.GroupName = "", ""
	case InviteGroup:
		if strings.TrimSpace(o.GroupID) == "" {
			return Invite{}, errors.New("a group invite needs the group's ID")
		}
		if len(o.GroupID) > maxGroupID {
			return Invite{}, fmt.Errorf("the group ID is longer than %d bytes", maxGroupID)
		}
	default:
		return Invite{}, fmt.Errorf("unknown invite kind %q (dm or group)", o.Kind)
	}
	ttl := time.Duration(o.TTLMillis) * time.Millisecond
	switch {
	case ttl <= 0 && o.Kind == InviteDM:
		ttl = defaultDMTTL
	case ttl <= 0:
		ttl = defaultGroupTTL
	case ttl > maxTTL:
		return Invite{}, fmt.Errorf("an invite can last at most %d days", int(maxTTL.Hours()/24))
	case ttl < time.Minute:
		return Invite{}, errors.New("an invite must last at least a minute")
	}
	maxUses := o.MaxUses
	if maxUses < 0 {
		return Invite{}, errors.New("max uses cannot be negative")
	}
	if maxUses == 0 {
		maxUses = 1
	}
	secret := make([]byte, secretLen)
	if _, err := rand.Read(secret); err != nil {
		return Invite{}, err
	}
	now := nowMs()
	rec := &inviteRec{
		Invite: Invite{ID: randomID(), Kind: o.Kind, GroupID: o.GroupID, GroupName: truncUTF8(o.GroupName, maxNameBytes),
			ExpiresAt: now + ttl.Milliseconds(), MaxUses: maxUses},
		Secret: secret, CreatedAt: now,
	}
	link, err := payload{Kind: o.Kind, PeerID: n.self, Addrs: n.inviteAddrs(), Secret: secret,
		ExpiresAt: rec.ExpiresAt, Name: n.DisplayName(), GroupID: rec.GroupID, GroupName: rec.GroupName}.encode()
	if err != nil {
		return Invite{}, err
	}
	rec.Link = link
	n.mu.Lock()
	n.st.pruneInvites(now)
	n.st.Invites = append(n.st.Invites, rec)
	n.saveLocked()
	out := rec.Invite
	n.mu.Unlock()
	return out, nil
}

// Invites lists invites, newest first (ended ones stay listed for a day).
func (n *Host) Invites() []Invite {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.st.pruneInvites(nowMs()) {
		n.saveLocked()
	}
	out := make([]Invite, 0, len(n.st.Invites))
	for i := len(n.st.Invites) - 1; i >= 0; i-- {
		out = append(out, n.st.Invites[i].Invite)
	}
	return out
}

// RevokeInvite ends an invite at once; a link already sent stops working.
func (n *Host) RevokeInvite(id string) error {
	defer n.sweepStrangers() // runs after the unlock below
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, in := range n.st.Invites {
		if in.ID == id {
			if !in.Revoked {
				in.Revoked = true
				if in.EndedAt == 0 {
					in.EndedAt = nowMs()
				}
				n.saveLocked()
			}
			return nil
		}
	}
	return fmt.Errorf("no invite %q", id)
}

// Preview decodes a link without connecting anywhere.
func (n *Host) Preview(link string) (InvitePreview, error) {
	p, err := parseLink(link)
	if err != nil {
		return InvitePreview{}, err
	}
	return InvitePreview{Kind: p.Kind, PeerID: p.PeerID.String(), Name: p.Name,
		GroupID: p.GroupID, GroupName: p.GroupName, ExpiresAt: p.ExpiresAt}, nil
}

// ---------------------------------------------------------------------------------------
// joining (the invitee's side)

// Join dials the inviter (the link's addresses, else known/DHT addresses), proves the
// secret over /tmx/pair/1.0.0 and records the inviter as a paired peer.
func (n *Host) Join(ctx context.Context, link string) (Paired, error) {
	p, err := parseLink(link)
	if err != nil {
		return Paired{}, err
	}
	if p.PeerID == n.self {
		return Paired{}, errors.New("this invite is your own; send it to the person you want to connect with")
	}
	if p.ExpiresAt > 0 && p.ExpiresAt <= nowMs() {
		return Paired{}, errors.New("this invite has expired; ask for a new one")
	}
	if len(p.Addrs) > 0 {
		n.h.Peerstore().AddAddrs(p.PeerID, p.Addrs, peerstore.TempAddrTTL)
	}
	if err := n.connect(ctx, p.PeerID); err != nil {
		return Paired{}, fmt.Errorf("%w: could not reach %s (%v); they need tmx running, then try again", ErrUnreachable, nameOr(p.Name, "the inviter"), shortErr(err))
	}
	// We connected, but the connection is gone before any answer: the inviter's connection
	// gater (gater.go) closes a stranger's connection at once when none of its invites is
	// open. Say so, rather than "unreachable", which would have the joiner retry in vain.
	// The inviter's own close code says so (QUIC: ConnGated from the remote, on the
	// authenticated connection); test it first, because our swarm may not have dropped the
	// closed connection yet, so Connectedness alone races with it on a busy machine.
	closed := func(err error) error {
		if !errors.Is(err, gatedByRemote) && n.h.Network().Connectedness(p.PeerID) == network.Connected {
			return nil
		}
		n.log.Debug("p2p: join: connection closed by the inviter", "peer", p.PeerID.String(), "err", err)
		// Drop what is left of it, so that a later Join dials afresh instead of picking a
		// connection our swarm has not yet noticed is closed.
		_ = n.h.Network().ClosePeer(p.PeerID)
		return fmt.Errorf("%s's tmx closed the connection: this invite was probably used, revoked or has expired; ask for a new one", nameOr(p.Name, "the inviter"))
	}
	sctx, cancel := context.WithTimeout(network.WithAllowLimitedConn(ctx, "tmx-pair"), streamTimeout)
	defer cancel()
	s, err := n.h.NewStream(sctx, p.PeerID, protoID(ProtoPair))
	if err != nil {
		if cerr := closed(err); cerr != nil {
			return Paired{}, cerr
		}
		return Paired{}, fmt.Errorf("%w: %v", ErrUnreachable, shortErr(err))
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(streamTimeout))
	req := pairRequest{V: 1, Proof: pairProof(p.Secret, p.PeerID, n.self), Name: n.DisplayName()}
	if err := writeMsg(s, req); err != nil {
		s.Reset()
		if cerr := closed(err); cerr != nil {
			return Paired{}, cerr
		}
		return Paired{}, fmt.Errorf("pairing: %w", err)
	}
	var resp pairResponse
	if err := readMsg(s, maxPairMsg, &resp); err != nil {
		s.Reset()
		if cerr := closed(err); cerr != nil {
			return Paired{}, cerr
		}
		return Paired{}, fmt.Errorf("pairing: no answer from %s: %v", nameOr(p.Name, "the inviter"), shortErr(err))
	}
	if !resp.OK {
		if !n.isPeer(p.PeerID) {
			// The inviter disconnects a stranger after a refusal (handlePair): close our side
			// now, so that a retry dials afresh instead of reusing a connection being closed.
			_ = n.h.Network().ClosePeer(p.PeerID)
		}
		return Paired{}, fmt.Errorf("%s's tmx refused: %s", nameOr(p.Name, "the inviter"), nameOr(resp.Error, "invalid invite"))
	}
	name := truncUTF8(strings.TrimSpace(resp.Name), maxNameBytes)
	if name == "" {
		name = p.Name
	}
	groupID, groupName := p.GroupID, p.GroupName
	if resp.GroupID != "" {
		groupID, groupName = resp.GroupID, resp.GroupName
	}
	n.recordPeer(p.PeerID, name, p.Kind, groupID)
	return Paired{PeerID: p.PeerID.String(), Name: name, SafetyCode: SafetyCode(n.self.String(), p.PeerID.String()),
		Kind: p.Kind, GroupID: groupID, GroupName: groupName}, nil
}

// gatedByRemote matches the error of a connection the other side's connection gater closed.
var gatedByRemote = &network.ConnError{Remote: true, ErrorCode: network.ConnGated}

func nameOr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// shortErr keeps the first line of libp2p's (often very long, per-address) dial errors.
func shortErr(err error) string {
	s := err.Error()
	if i := strings.IndexAny(s, "\n"); i > 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func (n *Host) recordPeer(p peer.ID, name string, kind InviteKind, groupID string) {
	n.mu.Lock()
	rec, ok := n.st.Peers[p.String()]
	if !ok {
		rec = &peerRec{PeerID: p.String(), PairedAt: nowMs()}
		n.st.Peers[p.String()] = rec
	}
	rec.Name, rec.Kind, rec.Introduced, rec.LastSeen = name, kind, false, nowMs()
	if groupID != "" {
		rec.GroupID = groupID
	}
	if addrs := n.addrStrings(n.h.Peerstore().Addrs(p)); len(addrs) > 0 {
		rec.Addrs = trimAddrs(addrs)
		n.rememberAddrsLocked(p, rec.Addrs)
	}
	n.lastConn[p] = n.peerReach(p)
	n.saveLocked()
	n.mu.Unlock()
	n.cm.Protect(p, contactTag)
	n.syncMDNS()
}

// ---------------------------------------------------------------------------------------
// the inviter's side

func (n *Host) handlePair(s network.Stream) {
	defer n.sweepStrangers() // last: the invite may have closed for everyone else
	defer s.Close()
	remote := s.Conn().RemotePeer()
	_ = s.SetDeadline(time.Now().Add(streamTimeout))
	// The connection gater lets strangers in only to try an invite (gater.go): one whose
	// attempt ends without pairing is disconnected, once it has read the answer (it closes
	// the stream then; the stream deadline bounds the wait).
	done := false
	defer func() {
		if !done && !n.isPeer(remote) {
			_ = s.CloseWrite()
			_, _ = io.Copy(io.Discard, io.LimitReader(s, 2*maxPairMsg)) // an unread request, then EOF
			n.dropStranger(remote)
		}
	}()
	fail := func(msg string) {
		_ = writeMsg(s, pairResponse{Error: msg})
	}
	if !n.limiter.allow("pair:"+remote.String(), 5, time.Minute) || !n.limiter.allow("pair:*", 30, time.Minute) {
		fail("too many attempts; wait a minute")
		return
	}
	var req pairRequest
	if err := readMsg(s, maxPairMsg, &req); err != nil {
		s.Reset()
		return
	}
	if req.V != 1 {
		fail("unsupported pairing version; update tmx")
		return
	}
	now := nowMs()
	n.mu.Lock()
	var match *inviteRec
	for _, in := range n.st.Invites {
		if hmac.Equal(req.Proof, pairProof(in.Secret, n.self, remote)) {
			match = in
			break
		}
	}
	var errMsg string
	retry := false
	switch {
	case match == nil:
		errMsg = "this invite is not valid here"
	case slices.Contains(match.UsedBy, remote.String()) && !match.Revoked && match.ExpiresAt > now:
		retry = true // the same person again (e.g. the answer got lost): fine, costs no use
	case match.Revoked:
		errMsg = "this invite was revoked; ask for a new one"
	case match.ExpiresAt <= now:
		errMsg = "this invite has expired; ask for a new one"
	case match.MaxUses > 0 && match.Uses >= match.MaxUses:
		errMsg = "this invite was already used; ask for a new one"
	}
	if errMsg != "" {
		n.mu.Unlock()
		fail(errMsg)
		return
	}
	name := truncUTF8(strings.TrimSpace(req.Name), maxNameBytes)
	resp := pairResponse{OK: true, Name: n.st.Name, Kind: match.Kind, GroupID: match.GroupID, GroupName: match.GroupName}
	paired := Paired{PeerID: remote.String(), Name: name, SafetyCode: SafetyCode(n.self.String(), remote.String()),
		Kind: match.Kind, GroupID: match.GroupID, GroupName: match.GroupName, InviteID: match.ID}
	if !retry { // take the use now, so two joiners cannot both use a single-use invite
		match.Uses++
		match.UsedBy = append(match.UsedBy, remote.String())
		if match.MaxUses > 0 && match.Uses >= match.MaxUses && match.EndedAt == 0 {
			match.EndedAt = now
		}
	}
	// Only this proof-checked peer may reconnect while its pairing is committed.
	if n.pairing == nil {
		n.pairing = make(map[peer.ID]int)
	}
	n.pairing[remote]++
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		n.pairing[remote]--
		if n.pairing[remote] == 0 {
			delete(n.pairing, remote)
		}
		n.mu.Unlock()
	}()

	// Hand the pairing to the chat layer first: if nobody takes it, the use is given back.
	if !retry {
		t := time.NewTimer(pairedWait)
		defer t.Stop()
		select {
		case n.paired <- paired:
		case <-t.C:
			n.mu.Lock()
			match.Uses--
			match.UsedBy = slices.DeleteFunc(match.UsedBy, func(id string) bool { return id == remote.String() })
			if match.EndedAt == now && !match.Revoked {
				match.EndedAt = 0
			}
			n.mu.Unlock()
			fail("the other tmx is busy; try again in a moment")
			return
		}
	}
	n.recordPeer(remote, name, paired.Kind, paired.GroupID)
	done = true
	if err := writeMsg(s, resp); err != nil {
		n.log.Warn("p2p: pair answer", "peer", remote.String(), "err", err)
	}
	// Let the joiner read the answer before the stream closes.
	_ = s.CloseWrite()
	_, _ = io.Copy(io.Discard, io.LimitReader(s, 1))
}

// ---------------------------------------------------------------------------------------
// rate limiting

type rateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{hits: map[string][]time.Time{}} }

// allow records a hit for key and reports whether it is within n per window.
func (r *rateLimiter) allow(key string, n int, window time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	h := r.hits[key]
	i := 0
	for i < len(h) && now.Sub(h[i]) > window {
		i++
	}
	h = h[i:]
	if len(h) >= n {
		r.hits[key] = h
		return false
	}
	r.hits[key] = append(h, now)
	if len(r.hits) > 4096 { // forget idle keys
		for k, v := range r.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > window {
				delete(r.hits, k)
			}
		}
	}
	return true
}
