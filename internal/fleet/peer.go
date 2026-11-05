package fleet

import (
	"errors"
	"strings"
)

// Peer mail: messages to and from agents on the fleet's other machines that still run the
// Python tmx daemon (DESIGN.md "Production profile", "Python fleet interop"). A remote agent is a
// mail party "fleet:<name>@<host>". Mail from one arrives through the daemon's peer route
// (POST /api/messages/incoming, authenticated by that machine's pair token) as DeliverPeer;
// mail to one (SendMessage to "name@host", or a reply to peer mail) is stored here and handed
// to the PeerSender, which posts it to that machine and retries until it is accepted.
//
// Message IDs: each side keeps its own. The ID on the wire is a 12-32 character lowercase hex
// string (Python's format); this daemon maps it to and from its local ID per peer, so a reply
// on either side names the message the other side knows.

// PeerPrefix starts the mail party of an agent on another machine of the fleet.
const PeerPrefix = "fleet:"

// PeerParty returns the mail party of agent name on host.
func PeerParty(name, host string) string { return PeerPrefix + name + "@" + host }

// IsPeer reports whether a mail party is an agent on another machine of the fleet.
func IsPeer(id string) bool { return strings.HasPrefix(id, PeerPrefix) }

// SplitPeer returns the agent name and host of a peer party.
func SplitPeer(id string) (name, host string, ok bool) {
	rest, found := strings.CutPrefix(id, PeerPrefix)
	if !found {
		return "", "", false
	}
	name, host, ok = strings.Cut(rest, "@")
	return name, host, ok && name != "" && host != ""
}

// PeerOutbound is one message for an agent on another machine, as the wire carries it
// (Python's /api/messages/incoming body). ID and ReplyTo are wire IDs.
type PeerOutbound struct {
	ID         string  `json:"id"`
	Sender     string  `json:"sender"`      // the local agent's name ("owner" for the owner)
	SenderHost string  `json:"sender_host"` // this machine
	To         string  `json:"to"`          // the remote agent's name
	Body       string  `json:"body"`
	ReplyTo    *string `json:"reply_to"` // the wire ID of the message answered, or null
	Urgent     bool    `json:"urgent"`
	Sent       float64 `json:"sent"`   // Unix seconds
	SHA256     string  `json:"sha256"` // of Body, UTF-8, lowercase hex
}

// PeerInbound is one message an authenticated peer machine hands over. Peer is the machine
// its pair token belongs to (never taken from the body).
type PeerInbound struct {
	Peer       string
	ID         string // the wire ID
	Sender     string
	SenderHost string
	To         string // a local agent's name (or "name@<this machine>")
	Body       string
	ReplyTo    string // the wire ID of the message answered, or ""
	Urgent     bool
}

// PeerSender posts peer mail. Send returns nil once the peer accepted the message (2xx with
// the same ID); ErrPeerUnreachable when the peer could not be reached (the fleet retries);
// any other error is a refusal (the message fails, the sender is told).
type PeerSender interface {
	Known(host string) bool // a configured peer machine
	Send(host string, m PeerOutbound) error
}

// ErrPeerUnreachable: the peer machine did not answer; the message stays queued and is retried.
var ErrPeerUnreachable = errors.New("peer machine unreachable")
