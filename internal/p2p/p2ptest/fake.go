// Package p2ptest is an in-memory p2p.Node for tests of the chat layer: a Network connects
// any number of Fakes; each can be switched offline and back, invites and pairing behave
// like the real node's (dm invites single use, group invites multi-use until expiry), and
// Send delivers only between paired, online nodes.
package p2ptest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

// Network is the shared medium of a set of Fakes.
type Network struct {
	mu      sync.Mutex
	nodes   map[string]*Fake
	invites map[string]*invite // by link
	seq     int
}

type invite struct {
	p2p.Invite
	owner *Fake
}

func NewNetwork() *Network {
	return &Network{nodes: map[string]*Fake{}, invites: map[string]*invite{}}
}

// Fake is one node. All methods are safe for concurrent use.
type Fake struct {
	net    *Network
	id     string
	name   string
	secret []byte // signs this node's envelopes (a stand-in for its private key)

	incoming chan p2p.Envelope
	paired   chan p2p.Paired
	events   chan p2p.PeerEvent

	// guarded by net.mu
	online bool
	closed bool
	peers  map[string]bool
	trust  map[string][]string // peers trusted through Trust, with the addresses given
	sent   []Sent
	// SendHook, when set, runs before each Send; a non-nil error is returned instead of
	// delivering (e.g. to drop envelopes), except Dropped: Send then reports success without
	// delivering, like a peer whose tmx acknowledges an envelope it does not understand.
	sendHook func(peerID string, env p2p.Envelope) error
	files    p2p.FileSource
	fetches  int
}

// Sent records one successful Send.
type Sent struct {
	To  string
	Env p2p.Envelope
}

// NewNode adds an online node named name (its peer ID is "peer-<name>").
func (n *Network) NewNode(name string) *Fake {
	n.mu.Lock()
	defer n.mu.Unlock()
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	f := &Fake{net: n, id: "peer-" + name, name: name, secret: secret, online: true, peers: map[string]bool{}, trust: map[string][]string{},
		incoming: make(chan p2p.Envelope, 1024), paired: make(chan p2p.Paired, 64), events: make(chan p2p.PeerEvent, 256)}
	n.nodes[f.id] = f
	return f
}

func (f *Fake) ID() string   { return f.id }
func (f *Fake) Name() string { return f.name }

// SetOnline switches the node on or off the network; paired peers get a PeerEvent.
func (f *Fake) SetOnline(on bool) {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	if f.online == on {
		return
	}
	f.online = on
	r := p2p.Offline
	if on {
		r = p2p.Direct
	}
	for pid := range f.peers {
		other := f.net.nodes[pid]
		if other == nil || other.closed {
			continue
		}
		if other.peers[f.id] {
			other.emit(p2p.PeerEvent{PeerID: f.id, Reachability: r})
		}
		// This node also learns about its peers coming into or going out of reach.
		if other.online {
			f.emit(p2p.PeerEvent{PeerID: pid, Reachability: r})
		}
	}
}

// SetSendHook installs (nil removes) a hook run before every Send of this node.
func (f *Fake) SetSendHook(h func(peerID string, env p2p.Envelope) error) {
	f.net.mu.Lock()
	f.sendHook = h
	f.net.mu.Unlock()
}

// SentLog returns the envelopes this node delivered so far.
func (f *Fake) SentLog() []Sent {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	return append([]Sent(nil), f.sent...)
}

// IsPaired reports whether this node accepts envelopes from peerID.
func (f *Fake) IsPaired(peerID string) bool {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	return f.peers[peerID]
}

func (f *Fake) emit(ev p2p.PeerEvent) {
	select {
	case f.events <- ev:
	default:
	}
}

func (f *Fake) Status() p2p.Status {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	r := p2p.Offline
	if f.online {
		r = p2p.Direct
	}
	return p2p.Status{Enabled: true, PeerID: f.id, Addrs: []string{"/memory/" + f.id}, Reachability: r, Peers: len(f.peers)}
}

func (f *Fake) SetDisplayName(name string) {
	f.net.mu.Lock()
	f.name = name
	f.net.mu.Unlock()
}

func (f *Fake) DisplayName() string {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	return f.name
}

// Trust lets peerID talk with this node without an invite (a group co-member). Like real
// trust it is one-sided: both members trust each other before envelopes flow.
func (f *Fake) Trust(peerID, name string, addrs []string) error {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	if peerID == "" || peerID == f.id {
		return fmt.Errorf("cannot trust %q", peerID)
	}
	f.peers[peerID] = true
	f.trust[peerID] = append([]string(nil), addrs...)
	return nil
}

// Trusted reports whether peerID was trusted through Trust (and not forgotten since), with
// the addresses given.
func (f *Fake) Trusted(peerID string) ([]string, bool) {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	a, ok := f.trust[peerID]
	return a, ok
}

// PeerAddrs is where this node "last saw" a paired peer (like (*p2p.Host).PeerAddrs).
func (f *Fake) PeerAddrs(peerID string) []string {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	if !f.peers[peerID] || f.net.nodes[peerID] == nil {
		return nil
	}
	return []string{"/memory/" + peerID}
}

func (f *Fake) CreateInvite(o p2p.InviteOptions) (p2p.Invite, error) {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	if o.Kind != p2p.InviteDM && o.Kind != p2p.InviteGroup {
		return p2p.Invite{}, fmt.Errorf("unknown invite kind %q", o.Kind)
	}
	if o.Kind == p2p.InviteGroup && o.GroupID == "" {
		return p2p.Invite{}, errors.New("a group invite needs a group")
	}
	ttl := o.TTLMillis
	if ttl <= 0 {
		ttl = (24 * time.Hour).Milliseconds()
		if o.Kind == p2p.InviteGroup {
			ttl = (24 * time.Hour).Milliseconds()
		}
	}
	maxUses := o.MaxUses
	if maxUses <= 0 {
		maxUses = 1
	}
	f.net.seq++
	id := fmt.Sprintf("inv%d", f.net.seq)
	inv := &invite{owner: f, Invite: p2p.Invite{ID: id, Kind: o.Kind, Link: "tmx://join/fake-" + f.id + "-" + id,
		GroupID: o.GroupID, GroupName: o.GroupName, ExpiresAt: time.Now().UnixMilli() + ttl, MaxUses: maxUses}}
	f.net.invites[inv.Link] = inv
	return inv.Invite, nil
}

func (f *Fake) Invites() []p2p.Invite {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	var out []p2p.Invite
	for _, inv := range f.net.invites {
		if inv.owner == f {
			out = append(out, inv.Invite)
		}
	}
	return out
}

func (f *Fake) RevokeInvite(id string) error {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	for _, inv := range f.net.invites {
		if inv.owner == f && inv.ID == id {
			inv.Revoked = true
			return nil
		}
	}
	return errors.New("no such invite")
}

func (f *Fake) lookupLocked(link string) (*invite, error) {
	inv := f.net.invites[strings.TrimSpace(link)]
	switch {
	case inv == nil:
		return nil, errors.New("this is not a valid tmx invite link")
	case inv.Revoked:
		return nil, errors.New("this invite was revoked")
	case time.Now().UnixMilli() > inv.ExpiresAt:
		return nil, errors.New("this invite has expired")
	case inv.MaxUses > 0 && inv.Uses >= inv.MaxUses:
		return nil, errors.New("this invite was already used")
	case inv.owner == f:
		return nil, errors.New("this is your own invite")
	}
	return inv, nil
}

func (f *Fake) Preview(link string) (p2p.InvitePreview, error) {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	inv, err := f.lookupLocked(link)
	if err != nil {
		return p2p.InvitePreview{}, err
	}
	return p2p.InvitePreview{Kind: inv.Kind, PeerID: inv.owner.id, Name: inv.owner.name, GroupID: inv.GroupID,
		GroupName: inv.GroupName, ExpiresAt: inv.ExpiresAt}, nil
}

// SafetyCode is the code both sides of a pairing see.
func SafetyCode(a, b string) string {
	if a > b {
		a, b = b, a
	}
	sum := sha256.Sum256([]byte(a + "|" + b))
	return fmt.Sprintf("%05d %05d %05d", int(sum[0])<<8|int(sum[1]), int(sum[2])<<8|int(sum[3]), int(sum[4])<<8|int(sum[5]))
}

func (f *Fake) Join(ctx context.Context, link string) (p2p.Paired, error) {
	f.net.mu.Lock()
	inv, err := f.lookupLocked(link)
	if err != nil {
		f.net.mu.Unlock()
		return p2p.Paired{}, err
	}
	owner := inv.owner
	if !f.online || !owner.online || owner.closed {
		f.net.mu.Unlock()
		return p2p.Paired{}, p2p.ErrUnreachable
	}
	inv.Uses++
	f.peers[owner.id] = true
	owner.peers[f.id] = true
	code := SafetyCode(f.id, owner.id)
	mine := p2p.Paired{PeerID: owner.id, Name: owner.name, SafetyCode: code, Kind: inv.Kind, GroupID: inv.GroupID, GroupName: inv.GroupName}
	theirs := p2p.Paired{PeerID: f.id, Name: f.name, SafetyCode: code, Kind: inv.Kind, GroupID: inv.GroupID,
		GroupName: inv.GroupName, InviteID: inv.ID}
	f.net.mu.Unlock()
	select {
	case owner.paired <- theirs:
	case <-ctx.Done():
		return p2p.Paired{}, ctx.Err()
	}
	return mine, nil
}

func (f *Fake) Paired() <-chan p2p.Paired { return f.paired }

func (f *Fake) Send(ctx context.Context, peerID string, env p2p.Envelope) error {
	f.net.mu.Lock()
	hook := f.sendHook
	f.net.mu.Unlock()
	env = f.sign(env)
	if hook != nil {
		if err := hook(peerID, env); errors.Is(err, Dropped) {
			return nil
		} else if err != nil {
			return err
		}
	}
	f.net.mu.Lock()
	to := f.net.nodes[peerID]
	if f.closed || !f.online || to == nil || to.closed || !to.online {
		f.net.mu.Unlock()
		return p2p.ErrUnreachable
	}
	if !to.peers[f.id] || !f.peers[peerID] {
		f.net.mu.Unlock()
		return fmt.Errorf("%s is not paired with %s", peerID, f.id)
	}
	f.sent = append(f.sent, Sent{To: peerID, Env: env})
	ch := to.incoming
	f.net.mu.Unlock()
	select {
	case ch <- env:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Dropped, returned by a send hook, makes Send succeed without delivering the envelope.
var Dropped = errors.New("p2ptest: acknowledged and dropped")

// fakeSigned is the canonical form a fake signature covers (like the real node's: a tag,
// each field length-prefixed, the time, the body), so any change to the envelope breaks it.
func fakeSigned(e p2p.Envelope) []byte {
	b := []byte("tmx-fake-envelope\x00")
	for _, s := range []string{e.ID, e.Type, e.ChatID, e.From} {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	b = binary.BigEndian.AppendUint64(b, uint64(e.At))
	b = binary.AppendUvarint(b, uint64(len(e.Body)))
	return append(b, e.Body...)
}

func (f *Fake) sign(e p2p.Envelope) p2p.Envelope {
	e.From = f.id
	if e.At == 0 {
		e.At = time.Now().UnixMilli()
	}
	m := hmac.New(sha256.New, f.secret)
	m.Write(fakeSigned(e))
	e.Sig = m.Sum(nil)
	return e
}

// Sign signs env as this node: only this node can make that signature (its secret), and it
// covers every field (a content-bound stand-in for the real Ed25519 signature).
func (f *Fake) Sign(env p2p.Envelope) (p2p.Envelope, error) {
	if env.ID == "" || env.Type == "" {
		return env, errors.New("envelope needs an ID and a type")
	}
	return f.sign(env), nil
}

// VerifyEnvelope checks env.Sig against the node named by env.From (any node of the network).
func (f *Fake) VerifyEnvelope(env p2p.Envelope) error {
	f.net.mu.Lock()
	signer := f.net.nodes[env.From]
	f.net.mu.Unlock()
	if signer == nil {
		return fmt.Errorf("bad sender %q", env.From)
	}
	if want := signer.sign(env); !bytes.Equal(want.Sig, env.Sig) {
		return errors.New("bad signature")
	}
	return nil
}

func (f *Fake) Incoming() <-chan p2p.Envelope    { return f.incoming }
func (f *Fake) PeerEvents() <-chan p2p.PeerEvent { return f.events }

func (f *Fake) Reachable(peerID string) p2p.Reachability {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	to := f.net.nodes[peerID]
	if !f.online || to == nil || to.closed || !to.online || !f.peers[peerID] {
		return p2p.Offline
	}
	return p2p.Direct
}

func (f *Fake) Forget(peerID string) {
	f.net.mu.Lock()
	delete(f.peers, peerID)
	delete(f.trust, peerID)
	f.net.mu.Unlock()
}

// Close takes the node off the network for good. Its channels stay open (readers just stop).
func (f *Fake) Close() error {
	f.net.mu.Lock()
	f.closed = true
	f.net.mu.Unlock()
	return nil
}

var _ p2p.Node = (*Fake)(nil)

// ServeFiles sets what answers this node's file requests.
func (f *Fake) ServeFiles(src p2p.FileSource) {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	f.files = src
}

// Fetches counts the files this node fetched successfully.
func (f *Fake) Fetches() int {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	return f.fetches
}

// Fetch asks peerID's file source, like the real protocol: paired or trusted peers only,
// ErrUnreachable when either side is offline, ErrNoFile when the source refuses.
func (f *Fake) Fetch(ctx context.Context, peerID, chatID, fileID string, w io.Writer, maxBytes int64) (int64, error) {
	f.net.mu.Lock()
	to := f.net.nodes[peerID]
	if f.closed || !f.online || to == nil || to.closed || !to.online {
		f.net.mu.Unlock()
		return 0, p2p.ErrUnreachable
	}
	if !to.peers[f.id] || !f.peers[peerID] {
		f.net.mu.Unlock()
		return 0, p2p.ErrNoFile
	}
	src := to.files
	f.net.mu.Unlock()
	if src == nil {
		return 0, p2p.ErrNoFile
	}
	r, size, err := src(p2p.FileRequest{PeerID: f.id, ChatID: chatID, FileID: fileID})
	if err != nil {
		return 0, p2p.ErrNoFile
	}
	defer r.Close()
	if size > maxBytes {
		return 0, p2p.ErrTooBig
	}
	n, err := io.Copy(w, io.LimitReader(r, maxBytes+1))
	if err == nil && n > maxBytes {
		err = p2p.ErrTooBig
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		f.net.mu.Lock()
		f.fetches++
		f.net.mu.Unlock()
	}
	return n, err
}
