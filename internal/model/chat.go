package model

// Chats between tmx installs (DESIGN.md "Chats"). The p2p layer (internal/p2p) connects
// installs; the chat layer (internal/chat) keeps contacts, chats and messages here.

// Contact is another person's tmx install we paired with (by invite).
type Contact struct {
	PeerID     string `json:"peer_id"`
	Name       string `json:"name"`        // shown here; the owner may rename it
	RemoteName string `json:"remote_name"` // the name they gave themselves
	Note       string `json:"note"`
	SafetyCode string `json:"safety_code"` // the same on both sides; compare it to verify
	AddedAt    int64  `json:"added_at"`
	UpdatedAt  int64  `json:"updated_at"`
	// Reachability is live (p2p.Reachability: direct | relayed | offline), not stored.
	Reachability string `json:"reachability"`
}

type ChatKind string

const (
	ChatDM    ChatKind = "dm"
	ChatGroup ChatKind = "group"
)

// ChatStatus: active; pending (joined a group, waiting for its member list); left (we left);
// removed (an admin removed us, or the group was full).
type ChatStatus string

const (
	ChatActive  ChatStatus = "active"
	ChatPending ChatStatus = "pending"
	ChatLeft    ChatStatus = "left"
	ChatRemoved ChatStatus = "removed"
)

// Me is the Sender (and Member.PeerID alias in the UI) of messages written on this install;
// System marks local notices ("Alex joined").
const (
	ChatMe     = "me"
	ChatSystem = "system"
)

type Chat struct {
	ID        string     `json:"id"`
	Kind      ChatKind   `json:"kind"`
	Name      string     `json:"name"`              // group name; for a DM the contact's name
	PeerID    string     `json:"peer_id,omitempty"` // DM: the other side
	CreatedBy string     `json:"created_by"`        // peer ID, or "me"
	AgentID   string     `json:"agent_id"`          // the local agent taking part; "" = people only
	Status    ChatStatus `json:"status"`
	Version   int64      `json:"version"` // group state version (admin's counter)
	ReadSeq   int64      `json:"read_seq"`
	CreatedAt int64      `json:"created_at"`
	UpdatedAt int64      `json:"updated_at"`
	LastAt    int64      `json:"last_at"` // last message (received_at)

	// Derived (not stored).
	Unread      int          `json:"unread"`
	Mentions    int          `json:"mentions"` // unread messages that @mention the owner
	Admin       bool         `json:"admin"`    // this install is an admin of the group
	Members     []ChatMember `json:"members,omitempty"`
	LastMessage *ChatMessage `json:"last_message,omitempty"`
	AgentName   string       `json:"agent_name,omitempty"`
	// AdminOffer: we (the admin) offered the admin role to a member and wait for its tmx.
	AdminOffer *ChatAdminOffer `json:"admin_offer,omitempty"`
}

// ChatAdminOffer is an open offer of a group's admin role (DESIGN.md "Chats", Groups). The
// member's tmx takes the role over by itself as soon as the offer reaches it.
type ChatAdminOffer struct {
	PeerID    string `json:"peer_id"`
	Name      string `json:"name"`
	At        int64  `json:"at"`
	ThenLeave bool   `json:"then_leave,omitempty"` // we leave the group once the new admin took over
	// Sent: the offer reached the member's tmx (no longer queued). An answer follows at once
	// from an up-to-date tmx; an older one ignores the offer.
	Sent bool `json:"sent,omitempty"`
}

type ChatRole string

const (
	RoleAdmin  ChatRole = "admin"
	RoleMember ChatRole = "member"
)

type ChatMember struct {
	ChatID   string   `json:"chat_id"`
	PeerID   string   `json:"peer_id"`
	Name     string   `json:"name"`
	Role     ChatRole `json:"role"`
	JoinedAt int64    `json:"joined_at"`
	LeftAt   int64    `json:"left_at,omitempty"`
	Me       bool     `json:"me,omitempty"` // derived
	// Handover (derived): the member's tmx said it understands admin handovers (an older
	// one keeps the member list it has until it is updated).
	Handover bool `json:"handover,omitempty"`
}

type ChatMessageState string

const (
	ChatQueued    ChatMessageState = "queued"    // waiting in the outbox (a recipient is offline)
	ChatSent      ChatMessageState = "sent"      // every recipient's install (or the group's relay) took it
	ChatDelivered ChatMessageState = "delivered" // every recipient stored it (receipts)
	ChatFailed    ChatMessageState = "failed"    // gave up (expired in the outbox, or the peer was removed)
)

type ChatMessage struct {
	Seq         int64            `json:"seq"` // local order (paging cursor)
	ID          string           `json:"id"`
	ChatID      string           `json:"chat_id"`
	Sender      string           `json:"sender"`       // peer ID, "me" or "system"
	SenderName  string           `json:"sender_name"`  // the person
	SenderAgent string           `json:"sender_agent"` // the agent's name when an agent wrote it; "" = the person
	Body        string           `json:"body"`
	At          int64            `json:"at"` // written (sender's clock)
	State       ChatMessageState `json:"state"`
	ReceivedAt  int64            `json:"received_at"`
	Recipients  int              `json:"recipients,omitempty"` // outgoing: how many must confirm
	Delivered   int              `json:"delivered,omitempty"`  // outgoing: receipts so far (derived)
	// Mentions: who the author @mentioned (Label is the text after "@" in Body).
	Mentions []ChatMention `json:"mentions,omitempty"`
	// MentionsMe: the message @mentions this install's owner.
	MentionsMe bool `json:"mentions_me,omitempty"`
	// Files attached to the message (derived from chat_files; state per file).
	Files []ChatFile `json:"files,omitempty"`
	// Topic: the board topic it hangs on ("" = General; derived from chat_message_topics).
	Topic string `json:"topic,omitempty"`
}

// ChatMention is one @mention: a person (an install, Agent "") or that install's chat agent.
type ChatMention struct {
	PeerID string `json:"peer_id"`
	Agent  string `json:"agent,omitempty"`
	Label  string `json:"label"`
}

type ChatFileState string

const (
	ChatFilePending ChatFileState = "pending" // announced, still downloading (or waiting for a peer)
	ChatFileReady   ChatFileState = "ready"   // stored here, checksum verified
	ChatFileFailed  ChatFileState = "failed"  // gave up (Error says why)
)

// ChatFile is a file attached to a chat message. Files travel separately from messages:
// receivers download them from a member that has them (p2p /tmx/file/1.0.0).
type ChatFile struct {
	ID        string        `json:"id"`
	ChatID    string        `json:"chat_id"`
	MessageID string        `json:"message_id,omitempty"` // "" = uploaded, not sent yet
	Sender    string        `json:"sender"`               // peer ID or "me"
	Name      string        `json:"name"`
	Type      string        `json:"type"`
	Size      int64         `json:"size"`
	SHA256    string        `json:"sha256"`
	State     ChatFileState `json:"state"`
	Error     string        `json:"error,omitempty"`
	Tries     int           `json:"-"`
	NextAt    int64         `json:"-"`
	CreatedAt int64         `json:"created_at"`
	// Derived.
	Path  string `json:"path,omitempty"` // where it is stored here (ready files)
	Image bool   `json:"image,omitempty"`
}

// Bus event types of the chat layer.
const (
	EvChatMessage    = "chat.message"    // data: ChatMessage (new or changed state)
	EvChatUpdated    = "chat.updated"    // data: Chat
	EvChatRemoved    = "chat.removed"    // data: {id}
	EvContactUpdated = "contact.updated" // data: Contact
	EvContactRemoved = "contact.removed" // data: {peer_id}
)
