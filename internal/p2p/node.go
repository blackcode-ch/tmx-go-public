// Package p2p connects tmx installs directly (go-libp2p): identity, invites, pairing and
// signed envelopes between peers. The chat layer (internal/chat) builds on Node only, so it
// is tested with a fake; this file is the contract between the two (coordinator, 23 Oct).
package p2p

import (
	"context"
	"errors"
	"io"
)

// ErrUnreachable: the peer is not connected and could not be dialled now. The chat layer
// queues the envelope and retries when PeerEvents reports the peer online.
var ErrUnreachable = errors.New("peer unreachable")

// ErrNoFile: the peer answered a file request but does not have that file or refuses this
// requester (not a member of the chat). Asking the same peer again will not help.
var ErrNoFile = errors.New("the peer does not have that file")

// ErrTooBig: the file is larger than the caller's limit (Fetch stops reading).
var ErrTooBig = errors.New("the file is larger than allowed")

// FileRequest is a peer asking this node for one file of a chat (protocol /tmx/file/1.0.0).
type FileRequest struct {
	PeerID string // the requester, authenticated by the connection
	ChatID string
	FileID string
}

// FileSource opens the file a peer asked for. It decides whether that peer may have it
// (e.g. is a member of the chat) and returns an error to refuse. The node closes r.
type FileSource func(req FileRequest) (r io.ReadCloser, size int64, err error)

// Reachability of this node or of a peer.
type Reachability string

const (
	Direct  Reachability = "direct"  // a direct connection (LAN, public address or hole-punched)
	Relayed Reachability = "relayed" // only through a circuit relay (small messages only)
	Offline Reachability = "offline"
)

type Status struct {
	Enabled      bool         `json:"enabled"`
	PeerID       string       `json:"peer_id"`
	Addrs        []string     `json:"addrs"`
	Reachability Reachability `json:"reachability"`
	Peers        int          `json:"peers"` // connected libp2p peers (incl. DHT/relay)
}

type InviteKind string

const (
	InviteDM    InviteKind = "dm"    // single use, becomes a contact + DM
	InviteGroup InviteKind = "group" // multi-use until expiry, joins a group
)

type InviteOptions struct {
	Kind      InviteKind
	GroupID   string // group invites
	GroupName string
	TTLMillis int64 // 0 = default (dm 24 h, group 7 days)
	MaxUses   int   // 0 = default (dm 1, group unlimited until expiry)
}

type Invite struct {
	ID        string     `json:"id"`
	Kind      InviteKind `json:"kind"`
	Link      string     `json:"link"` // tmx://join/<payload>; the QR encodes this
	GroupID   string     `json:"group_id,omitempty"`
	GroupName string     `json:"group_name,omitempty"`
	ExpiresAt int64      `json:"expires_at"`
	Uses      int        `json:"uses"`
	MaxUses   int        `json:"max_uses"`
	Revoked   bool       `json:"revoked"`
}

// InvitePreview is what Join shows before connecting.
type InvitePreview struct {
	Kind      InviteKind `json:"kind"`
	PeerID    string     `json:"peer_id"`
	Name      string     `json:"name"` // the inviter's display name
	GroupID   string     `json:"group_id,omitempty"`
	GroupName string     `json:"group_name,omitempty"`
	ExpiresAt int64      `json:"expires_at"`
}

// Paired is the result of a successful pairing, on both sides.
type Paired struct {
	PeerID     string     `json:"peer_id"`
	Name       string     `json:"name"`        // the other side's display name
	SafetyCode string     `json:"safety_code"` // same on both sides; optional verification
	Kind       InviteKind `json:"kind"`
	GroupID    string     `json:"group_id,omitempty"`
	GroupName  string     `json:"group_name,omitempty"`
	InviteID   string     `json:"invite_id,omitempty"` // set on the inviter's side
}

// Envelope is one signed unit between peers. Body is opaque JSON owned by the chat layer;
// p2p signs it with this node's key on Send and verifies From/Sig on receipt (it never
// delivers an envelope whose signature does not match the stream's remote peer).
type Envelope struct {
	ID     string `json:"id"`
	Type   string `json:"type"` // chat layer's message type, e.g. "msg", "receipt", "group"
	ChatID string `json:"chat_id,omitempty"`
	From   string `json:"from"` // sender peer ID (filled by Send)
	At     int64  `json:"at"`   // unix ms (filled by Send if 0)
	Body   []byte `json:"body"`
	Sig    []byte `json:"sig"` // filled by Send
}

type PeerEvent struct {
	PeerID       string
	Reachability Reachability
}

// Node is the running p2p host. All methods are safe for concurrent use.
type Node interface {
	Status() Status
	SetDisplayName(name string)
	// DisplayName is the name this node gives in invites and pairings.
	DisplayName() string
	CreateInvite(opts InviteOptions) (Invite, error)
	Invites() []Invite
	RevokeInvite(id string) error
	Preview(link string) (InvitePreview, error)
	// Join pairs with the inviter of link; both sides then report the pairing on Paired().
	Join(ctx context.Context, link string) (Paired, error)
	// Paired delivers pairings completed on the inviter's side (someone used our invite).
	Paired() <-chan Paired
	// Send delivers env to peerID now (one stream, waits for the peer's ack) or returns
	// ErrUnreachable; it never queues.
	Send(ctx context.Context, peerID string, env Envelope) error
	// Incoming delivers verified envelopes from paired peers only.
	Incoming() <-chan Envelope
	PeerEvents() <-chan PeerEvent
	Reachable(peerID string) Reachability
	// Forget drops a peer: no more envelopes accepted from it.
	Forget(peerID string)
	// Trust accepts envelopes from peerID and lets Send dial it (found via the DHT or the
	// addrs given) without an invite: a co-member of a group, vouched for by the group's
	// admin. name is the display name to remember. Forget undoes it.
	Trust(peerID, name string, addrs []string) error
	// ServeFiles sets what answers file requests from paired or trusted peers (nil, the
	// default, refuses them all).
	ServeFiles(src FileSource)
	// Fetch downloads file fileID of chatID from peerID into w, at most maxBytes, and returns
	// the bytes written. ErrUnreachable: not connected now (retry later); ErrNoFile: the peer
	// does not have it or refuses; ErrTooBig: larger than maxBytes. Files need a direct
	// connection (hole punching is attempted); over a relay only small files pass.
	Fetch(ctx context.Context, peerID, chatID, fileID string, w io.Writer, maxBytes int64) (int64, error)
	// Sign signs env as this node (From, At if zero, Sig) without sending it: a statement
	// others may pass on and anyone can check with VerifyEnvelope (a group's admin
	// certificate, DESIGN.md "Chats"). The same limits as Send apply.
	Sign(env Envelope) (Envelope, error)
	// VerifyEnvelope checks that env.Sig is env.From's signature over env. Envelopes from
	// Incoming are verified already; this is for one passed on by another peer.
	VerifyEnvelope(env Envelope) error
	Close() error
}
