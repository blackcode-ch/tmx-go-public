// Package whatsapp links the owner's own WhatsApp account to the daemon as a companion device,
// the way WhatsApp Web and Desktop are linked (a QR code scanned on the phone, or a pairing
// code typed on it), and keeps its chats in tmx's database (DESIGN.md "WhatsApp").
//
// The connection is go.mau.fi/whatsmeow (MPL-2.0) behind the small Client interface below, so
// the service and its tests never touch WhatsApp itself: meow.go adapts whatsmeow, watest has a
// fake. Client events are tmx's own types, already in the shape the store keeps.
//
// WhatsApp does not support this officially and can ban numbers that automate, so sending is
// paced and capped (limits.go), there is nothing that sends to many chats at once, and an
// agent's message waits for the owner's Allow unless the owner said otherwise.
package whatsapp

import (
	"context"
	"os"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Account is the linked WhatsApp account.
type Account struct {
	JID   string // the account's phone JID, e.g. 41791234567@s.whatsapp.net
	Phone string // "+41791234567"
	Name  string // the push name the owner chose
}

// Client is the connection to WhatsApp: whatsmeow in the daemon (NewMeow), a fake in tests.
// Events go to the handler given to Open, from any goroutine, in the order they happened.
type Client interface {
	// Open loads the stored device, if any, and connects it. linked is false when no device is
	// stored (nothing connects then).
	Open(ctx context.Context, h func(Event)) (linked bool, err error)
	// Account is the linked account (zero before linking).
	Account() Account
	// LinkQR connects a new device and shows QR codes (EvQR, renewed until one is scanned) until
	// the phone links it (EvLinked) or linking ends (EvLinkEnded). It returns once linking runs.
	LinkQR(ctx context.Context) error
	// LinkPhone links with a pairing code for this phone number (EvPairingCode), typed on the
	// phone under Linked devices → Link with phone number instead.
	LinkPhone(ctx context.Context, phone string) error
	// CancelLink stops linking (EvLinkEnded with no error follows).
	CancelLink()
	// Logout unlinks this device on WhatsApp (best effort while offline) and forgets its keys.
	Logout(ctx context.Context) error
	// NewMessageID returns the id a message about to be sent will have.
	NewMessageID() string
	// Upload uploads an attachment before Send.
	Upload(ctx context.Context, f OutFile) (MediaRef, error)
	// Send sends one message and returns WhatsApp's timestamp for it.
	Send(ctx context.Context, m Outgoing) (time.Time, error)
	// Download fetches an attachment into f (decrypted and checked).
	Download(ctx context.Context, ref MediaRef, f *os.File) error
	// MarkRead sends read receipts for messages of one sender in a chat.
	MarkRead(ctx context.Context, chat, sender string, ids []string) error
	// ChatName looks a chat's name up (a group's subject, a contact's name); "" if unknown.
	ChatName(ctx context.Context, jid string) string
	// Close disconnects (the device stays linked).
	Close()
}

// MediaRef says how to fetch an attachment from WhatsApp's servers. It holds the media's keys:
// stored in tmx.db, never in the API.
type MediaRef struct {
	Kind          string `json:"kind"` // image | video | audio | document | sticker
	DirectPath    string `json:"direct_path"`
	URL           string `json:"url,omitempty"`
	MediaKey      []byte `json:"media_key"`
	FileSHA256    []byte `json:"file_sha256"`
	FileEncSHA256 []byte `json:"file_enc_sha256"`
	Size          int64  `json:"size"`
}

// OutFile is an attachment to send.
type OutFile struct {
	Name string
	Mime string
	Kind string // image | video | audio | document
	Data []byte
}

// Outgoing is one message to send: text, or an uploaded attachment with Text as its caption.
type Outgoing struct {
	ID      string
	Chat    string
	Text    string
	Quote   *Quote   // a reply
	File    *OutFile // with Media set by Upload
	Media   *MediaRef
	Seconds int // audio and video length, when known
}

// Quote is the message a reply answers.
type Quote struct {
	ID     string
	Sender string // the quoted message's sender JID
	Text   string
	FromMe bool
}

// Incoming is a message from WhatsApp: a new one (Msg), or a change to an earlier one (Edit,
// Revoke, React name the message changed).
type Incoming struct {
	Msg    model.WaMessage
	Ref    *MediaRef
	Edit   string // Msg.Text/Caption replace message Edit's
	Revoke string // message Revoke was deleted for everyone
	React  string // Msg.Text is a reaction (an emoji; "" takes it back) to message React
	Skip   bool   // nothing tmx shows (protocol messages, key shares)
}

// ChatUpdate changes some of a chat's fields (nil = unchanged).
type ChatUpdate struct {
	JID      string
	Kind     model.WaChatKind
	Name     *string
	Unread   *int
	Pinned   *bool
	Muted    *bool
	Archived *bool
	LastAt   int64 // the chat's last activity (history), 0 = unknown
	Read     bool  // the owner read it on another device
	Delete   bool  // deleted or cleared on the phone
}

// Contact is a person's names: from the owner's address book (Name) and their own (PushName).
type Contact struct {
	JID      string
	Name     string
	PushName string
}

// Event is something that happened on the connection: one of the Ev types below.
type Event any

type (
	// EvQR: show this QR code until it expires (linking).
	EvQR struct {
		Code    string
		Expires time.Time
	}
	// EvPairingCode: type this code on the phone (linking with a phone number).
	EvPairingCode struct{ Code string }
	// EvLinked: the phone linked this device.
	EvLinked struct{ Account Account }
	// EvLinkEnded: linking stopped without a link; Err is "" when it was cancelled.
	EvLinkEnded struct{ Err string }
	// EvConnected: connected and logged in.
	EvConnected struct{ Account Account }
	// EvDisconnected: the connection dropped; the client reconnects by itself.
	EvDisconnected struct{}
	// EvLoggedOut: the phone removed this device (or WhatsApp did); its keys are gone.
	EvLoggedOut struct{ Reason string }
	// EvFailed: the connection stopped and will not come back by itself (a ban, an outdated
	// client, another program using the same keys).
	EvFailed struct{ Err string }
	// EvHistory: a part of the history the phone sends after linking.
	EvHistory struct {
		Chats    []ChatUpdate
		Messages []Incoming
		Contacts []Contact
		Progress int // 0..100, -1 unknown
	}
	// EvMessage: a message arrived (or one of the owner's, sent from the phone).
	EvMessage struct{ In Incoming }
	// EvReceipt: messages were delivered, read or played. Mine: about own messages (the other
	// side's receipt); otherwise the owner read someone's messages on another device.
	EvReceipt struct {
		Chat   string
		IDs    []string
		Status model.WaMessageStatus
		Mine   bool
	}
	// EvChat: a chat's settings changed (pinned, muted, archived, renamed, read elsewhere).
	EvChat struct{ Chat ChatUpdate }
	// EvContacts: names of people.
	EvContacts struct{ Contacts []Contact }
)
