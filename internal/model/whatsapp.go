package model

// WhatsApp (DESIGN.md "WhatsApp"): the owner's own account linked to the daemon as a
// companion device, the way WhatsApp Web is. Go internal/whatsapp; TS ui/src/lib/wa-types.ts
// (same JSON names). Everything here is other people's words: only the owner's clients see it,
// and agents read it through their tools framed as outside data.

// WaState is the connection's state.
type WaState string

const (
	WaUnlinked   WaState = "unlinked"   // no device linked
	WaLinking    WaState = "linking"    // showing a QR code or a pairing code
	WaConnecting WaState = "connecting" // linked, (re)connecting
	WaConnected  WaState = "connected"
	WaLoggedOut  WaState = "logged_out" // the phone removed this device; link again
	WaError      WaState = "error"      // error says why (a ban, an outdated client, a broken store)
)

// WaHistory is the history sync's progress after linking.
type WaHistory struct {
	Syncing  bool `json:"syncing"`
	Progress int  `json:"progress,omitempty"` // 0..100 while syncing
}

// WaSettings is the owner's WhatsApp settings (PUT /api/whatsapp/settings; config.json "whatsapp").
type WaSettings struct {
	ReadReceipts    bool     `json:"read_receipts"`     // reading in tmx sends read receipts (blue ticks); default off
	AgentsSend      string   `json:"agents_send"`       // "ask" (default): an agent's message waits for Allow; "allowed": agents send without asking
	AllowedChats    []string `json:"allowed_chats"`     // chats where agents send without asking
	NotifyHeadChats []string `json:"notify_head_chats"` // chats whose new messages reach the head as short mail
	// AgentProfiles are the profiles whose agents may read and ask to send (default ["head"]).
	// Not in the contract's Settings form; PUT without it keeps the current list.
	AgentProfiles []string `json:"agent_profiles"`
}

// Agents' sending rule (WaSettings.AgentsSend).
const (
	WaAgentsAsk     = "ask"
	WaAgentsAllowed = "allowed"
)

// WaStatus is GET /api/whatsapp and the whatsapp.status event.
type WaStatus struct {
	State    WaState    `json:"state"`
	Phone    string     `json:"phone,omitempty"` // "+41791234567"
	Name     string     `json:"name,omitempty"`  // the account's push name
	LinkedAt int64      `json:"linked_at,omitempty"`
	Error    string     `json:"error,omitempty"`
	History  WaHistory  `json:"history"`
	Settings WaSettings `json:"settings"`
}

// WaQR is the whatsapp.qr event: a QR code to scan (renewed every ~20 s) or a phone pairing code.
type WaQR struct {
	Code        string `json:"code,omitempty"`
	PNG         string `json:"png,omitempty"` // data:image/png;base64,… (the daemon renders it)
	ExpiresAt   int64  `json:"expires_at,omitempty"`
	PairingCode string `json:"pairing_code,omitempty"` // "ABCD-EFGH": typed on the phone
}

// WaChatKind: a direct chat or a group.
type WaChatKind string

const (
	WaDM    WaChatKind = "dm"
	WaGroup WaChatKind = "group"
)

// WaChat is one chat of the linked account.
type WaChat struct {
	JID      string     `json:"jid"`
	Name     string     `json:"name"`
	Kind     WaChatKind `json:"kind"`
	Unread   int        `json:"unread"`
	Pinned   bool       `json:"pinned"`
	Muted    bool       `json:"muted"`
	Archived bool       `json:"archived"`
	Last     *WaMessage `json:"last"`   // null without messages
	Avatar   *string    `json:"avatar"` // a data URL, or null
	LastAt   int64      `json:"last_at,omitempty"`
}

// WaMessageType is what a message carries.
type WaMessageType string

const (
	WaText        WaMessageType = "text"
	WaImage       WaMessageType = "image"
	WaVideo       WaMessageType = "video"
	WaAudio       WaMessageType = "audio"
	WaVoice       WaMessageType = "voice" // a push-to-talk voice note
	WaDocument    WaMessageType = "document"
	WaSticker     WaMessageType = "sticker"
	WaLocation    WaMessageType = "location"
	WaContact     WaMessageType = "contact"
	WaReaction    WaMessageType = "reaction"
	WaSystem      WaMessageType = "system"
	WaUnsupported WaMessageType = "unsupported"
)

// WaMessageStatus is a message's delivery state (own messages) or "read" for incoming ones read.
type WaMessageStatus string

const (
	WaPending   WaMessageStatus = "pending"
	WaSent      WaMessageStatus = "sent"
	WaDelivered WaMessageStatus = "delivered"
	WaRead      WaMessageStatus = "read"
	WaFailed    WaMessageStatus = "failed"
)

// WaStatusRank orders delivery states, so a late "delivered" never undoes "read".
func WaStatusRank(s WaMessageStatus) int {
	switch s {
	case WaPending:
		return 0
	case WaSent:
		return 1
	case WaDelivered:
		return 2
	case WaRead:
		return 3
	}
	return -1 // failed: only set by the sender
}

// WaMedia describes a message's attachment. The file is fetched from WhatsApp on demand.
type WaMedia struct {
	Mime       string `json:"mime"`
	Size       int64  `json:"size"`
	Name       string `json:"name,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Downloaded bool   `json:"downloaded"`
}

// WaQuoted is the message a reply answers.
type WaQuoted struct {
	ID         string `json:"id"`
	Text       string `json:"text,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
}

// WaReact is one person's reaction to a message.
type WaReact struct {
	Emoji string `json:"emoji"`
	By    string `json:"by"` // the reacting person's name ("You" for the owner)
	ByJID string `json:"by_jid,omitempty"`
}

// WaMessage is one message in a chat. IDs are WhatsApp's, unique within a chat.
type WaMessage struct {
	ID         string          `json:"id"`
	ChatJID    string          `json:"chat_jid"`
	SenderJID  string          `json:"sender_jid"`
	SenderName string          `json:"sender_name"`
	FromMe     bool            `json:"from_me"`
	At         int64           `json:"at"`
	Type       WaMessageType   `json:"type"`
	Text       string          `json:"text,omitempty"`
	Caption    string          `json:"caption,omitempty"`
	Media      *WaMedia        `json:"media,omitempty"`
	Quoted     *WaQuoted       `json:"quoted,omitempty"`
	Reactions  []WaReact       `json:"reactions,omitempty"`
	Status     WaMessageStatus `json:"status"`
	Error      string          `json:"error,omitempty"`
	Edited     bool            `json:"edited,omitempty"`
	Deleted    bool            `json:"deleted,omitempty"`
	// Origin: who sent it through tmx ("owner", or the agent's id); empty for messages received,
	// synced from history, or sent from the phone.
	Origin string `json:"origin,omitempty"`
}

// WaSendInput is the Input of an Approval of kind whatsapp: an agent's message waiting for the
// owner's Allow (the exact text that will be sent).
type WaSendInput struct {
	ChatJID  string `json:"chat_jid"`
	ChatName string `json:"chat_name"`
	Text     string `json:"text"`
	ReplyTo  string `json:"reply_to,omitempty"`
}

// ApprovalWhatsApp: an agent asked to send a WhatsApp message (Tool ToolWhatsAppSend, Input
// WaSendInput). Allow sends it, deny drops it; the agent hears the outcome by mail.
const ApprovalWhatsApp ApprovalKind = "whatsapp"

// ToolWhatsAppSend is Approval.Tool for an agent's WhatsApp message.
const ToolWhatsAppSend = "mcp__tmxg__whatsapp_send"

// WhatsAppParty is the From of mail the WhatsApp service sends agents: new messages in a chat
// the owner chose ("tell the head"), and the outcome of a message an agent asked to send.
const WhatsAppParty = "whatsapp"

// Bus events. They carry other people's words: only the owner's clients receive them.
const (
	EvWaStatus  = "whatsapp.status"  // data: WaStatus
	EvWaQR      = "whatsapp.qr"      // data: WaQR
	EvWaChat    = "whatsapp.chat"    // data: WaChat (new or changed)
	EvWaMessage = "whatsapp.message" // data: WaMessage (new or changed)
)
