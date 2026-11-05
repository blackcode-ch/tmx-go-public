package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

// Envelope types of the chat layer (p2p.Envelope.Type). Every Body is JSON with "v": 2;
// receivers ignore unknown fields and drop envelopes of another major version. See
// DESIGN.md "Chats" for the semantics. The author of every envelope is Envelope.From,
// authenticated and signed by the p2p layer: nobody relays another peer's envelopes.
const (
	TypeMsg     = "chat.msg"     // a chat message (ID = message ID, ChatID = chat ID)
	TypeReceipt = "chat.receipt" // "stored these messages" (delivery receipts)
	TypeGroup   = "chat.group"   // a group's state, from its admin (members, name, version, event)
	TypeLeave   = "chat.leave"   // a member tells the admin it leaves the group
	TypeName    = "chat.name"    // a peer tells its contacts and group co-members its new display name
	TypeTopic   = "chat.topic"   // board changes: topics, messages moved between them, log lines (topics.go)
	// Handing a group's admin role over (admin.go): the admin offers it to a member, whose
	// tmx accepts; the admin then signs a certificate (never sent alone: carried in every
	// chat.group as admin_proof, the one envelope a peer passes on, checked by its signature).
	TypeAdminOffer  = "chat.admin_offer"  // admin → member: take over the group
	TypeAdminAccept = "chat.admin_accept" // member → admin: taken
	TypeAdminCert   = "chat.admin"        // signed by the admin: from version on, admin is the admin
)

// WireVersion is the major version of the Body formats. 2: group members send to each
// other directly (no admin relay; no "sender"/"by" fields), group states carry addresses.
const WireVersion = 2

type msgBody struct {
	V           int            `json:"v"`
	Kind        model.ChatKind `json:"kind"`
	ChatName    string         `json:"chat_name,omitempty"`    // group name (hint)
	SenderName  string         `json:"sender_name,omitempty"`  // the author's display name (hint; receivers prefer their own names)
	SenderAgent string         `json:"sender_agent,omitempty"` // the agent that wrote it, "" = the person
	Body        string         `json:"body"`
	At          int64          `json:"at"`
	// Added in place (receivers ignore unknown fields): files to download separately,
	// @mentions, and the author install's agent in this chat (so others can @mention it).
	Files     []wireFile    `json:"files,omitempty"`
	Mentions  []wireMention `json:"mentions,omitempty"`
	ChatAgent string        `json:"chat_agent,omitempty"`
	// Topic: the board topic it was written in (absent = General; topics.go).
	Topic *wireTopicRef `json:"topic,omitempty"`
}

// receiptBody: Envelope.From stored these messages.
type receiptBody struct {
	V   int      `json:"v"`
	IDs []string `json:"ids"`
	// Caps: what this tmx understands beyond v2 (added in place; older installs ignore it).
	Caps []string `json:"caps,omitempty"`
}

type wireMember struct {
	PeerID   string         `json:"peer_id"`
	Name     string         `json:"name"`
	Role     model.ChatRole `json:"role"`
	JoinedAt int64          `json:"joined_at"`
	// Addrs: where the admin last saw this member (multiaddrs), so the other members can
	// reach it directly (Node.Trust); empty when unknown.
	Addrs []string `json:"addrs,omitempty"`
}

// maxMemberAddrs bounds the addresses per member in a group state.
const maxMemberAddrs = 8

// Group events carried by a chat.group state.
const (
	evCreated = "created"
	evJoined  = "joined"
	evRemoved = "removed"
	evLeft    = "left"
	evRenamed = "renamed"
	evFull    = "full"   // the joiner could not be added: 20 members already
	evClosed  = "closed" // the admin closed the group (it left)
	evAdmin   = "admin"  // the admin role moved to event.peer_id (admin.go)
	evMoved   = "moved"  // a joiner came through an old admin's invite: ask event.peer_id
)

type groupEvent struct {
	Kind   string `json:"kind"`
	PeerID string `json:"peer_id,omitempty"`
	Name   string `json:"name,omitempty"`
}

type groupBody struct {
	V       int          `json:"v"`
	Name    string       `json:"name"`
	Version int64        `json:"version"`
	Members []wireMember `json:"members"`
	Event   groupEvent   `json:"event"`
	// AdminProof: the signed chat.admin certificates since the group's first handover (at
	// most 8), so members who missed one can check the admin themselves (admin.go).
	AdminProof []p2p.Envelope `json:"admin_proof,omitempty"`
}

type leaveBody struct {
	V int `json:"v"`
}

// nameBody: Envelope.From now goes by Name.
type nameBody struct {
	V    int    `json:"v"`
	Name string `json:"name"`
}

func envelope(id, typ, chatID string, body any) (p2p.Envelope, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return p2p.Envelope{}, err
	}
	return p2p.Envelope{ID: id, Type: typ, ChatID: chatID, At: model.Now(), Body: b}, nil
}

// decodeBody parses an envelope body of the current major version.
func decodeBody(b []byte, v any) error {
	var head struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return fmt.Errorf("body is not JSON: %w", err)
	}
	if head.V != WireVersion {
		return fmt.Errorf("body version %d (this tmx reads %d)", head.V, WireVersion)
	}
	return json.Unmarshal(b, v)
}

// DMChatID is the ID of the DM between two peers: the same on both sides.
func DMChatID(a, b string) string {
	if a > b {
		a, b = b, a
	}
	sum := sha256.Sum256([]byte("tmx-dm|" + a + "|" + b))
	return "dm-" + hex.EncodeToString(sum[:8])
}
