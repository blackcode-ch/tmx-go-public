package whatsapp

import (
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Converting whatsmeow's messages (WhatsApp's protobufs) into tmx's: what a message carries,
// the attachment's details and keys, the message it quotes, and changes (edits, deletions,
// reactions). Anything else becomes "unsupported" with a line saying what it was.

// jidNorm turns a JID into the chat or person tmx keys by (phone JIDs where WhatsApp hides
// the number behind a LID and the number is known).
type jidNorm func(types.JID) string

func convert(evt *events.Message, norm jidNorm) Incoming {
	info := evt.Info
	chat := norm(info.Chat)
	sender := norm(info.Sender)
	if !info.IsGroup && info.Chat.Server == types.HiddenUserServer && !info.SenderAlt.IsEmpty() && !info.IsFromMe {
		sender = norm(info.SenderAlt)
	}
	in := Incoming{Msg: model.WaMessage{ID: info.ID, ChatJID: chat, SenderJID: sender, SenderName: info.PushName,
		FromMe: info.IsFromMe, At: info.Timestamp.UnixMilli()}}
	if info.Chat.Server == types.BroadcastServer || info.Chat.Server == types.NewsletterServer {
		in.Skip = true // status updates, broadcasts and channels are not chats here
		return in
	}
	m := evt.Message
	if m == nil {
		in.Msg.Type = model.WaSystem
		if evt.SourceWebMsg != nil {
			in.Msg.Text = stubText(evt.SourceWebMsg)
		}
		if in.Msg.Text == "" {
			in.Skip = true
		}
		return in
	}
	fill(&in, m, norm)
	return in
}

// fill reads the message's content into in.
func fill(in *Incoming, m *waE2E.Message, norm jidNorm) {
	msg := &in.Msg
	var ctx *waE2E.ContextInfo
	switch {
	case m.GetProtocolMessage() != nil:
		p := m.GetProtocolMessage()
		switch p.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			in.Revoke = p.GetKey().GetID()
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			in.Edit = p.GetKey().GetID()
			if e := p.GetEditedMessage(); e != nil {
				var tmp Incoming
				fill(&tmp, e, norm)
				msg.Text, msg.Caption = tmp.Msg.Text, tmp.Msg.Caption
			}
		}
		if in.Revoke == "" && in.Edit == "" {
			in.Skip = true // key shares, ephemeral settings, history notifications
		}
		return
	case m.GetReactionMessage() != nil:
		r := m.GetReactionMessage()
		in.React = r.GetKey().GetID()
		msg.Type, msg.Text = model.WaReaction, r.GetText()
		if in.React == "" {
			in.Skip = true
		}
		return
	case m.GetConversation() != "":
		msg.Type, msg.Text = model.WaText, m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		x := m.GetExtendedTextMessage()
		msg.Type, msg.Text, ctx = model.WaText, x.GetText(), x.GetContextInfo()
	case m.GetImageMessage() != nil:
		x := m.GetImageMessage()
		msg.Type, msg.Caption, ctx = model.WaImage, x.GetCaption(), x.GetContextInfo()
		msg.Media = &model.WaMedia{Mime: x.GetMimetype(), Size: int64(x.GetFileLength()), Width: int(x.GetWidth()), Height: int(x.GetHeight())}
		in.Ref = ref("image", x)
	case m.GetVideoMessage() != nil || m.GetPtvMessage() != nil:
		x := m.GetVideoMessage()
		if x == nil {
			x = m.GetPtvMessage() // a round video note
		}
		msg.Type, msg.Caption, ctx = model.WaVideo, x.GetCaption(), x.GetContextInfo()
		msg.Media = &model.WaMedia{Mime: x.GetMimetype(), Size: int64(x.GetFileLength()), DurationMS: int64(x.GetSeconds()) * 1000,
			Width: int(x.GetWidth()), Height: int(x.GetHeight())}
		in.Ref = ref("video", x)
	case m.GetAudioMessage() != nil:
		x := m.GetAudioMessage()
		msg.Type, ctx = model.WaAudio, x.GetContextInfo()
		if x.GetPTT() {
			msg.Type = model.WaVoice
		}
		msg.Media = &model.WaMedia{Mime: x.GetMimetype(), Size: int64(x.GetFileLength()), DurationMS: int64(x.GetSeconds()) * 1000}
		in.Ref = ref("audio", x)
	case m.GetDocumentMessage() != nil:
		x := m.GetDocumentMessage()
		msg.Type, msg.Caption, ctx = model.WaDocument, x.GetCaption(), x.GetContextInfo()
		msg.Media = &model.WaMedia{Mime: x.GetMimetype(), Size: int64(x.GetFileLength()), Name: firstNonEmpty(x.GetFileName(), x.GetTitle())}
		in.Ref = ref("document", x)
	case m.GetStickerMessage() != nil:
		x := m.GetStickerMessage()
		msg.Type, ctx = model.WaSticker, x.GetContextInfo()
		msg.Media = &model.WaMedia{Mime: x.GetMimetype(), Size: int64(x.GetFileLength()), Width: int(x.GetWidth()), Height: int(x.GetHeight())}
		in.Ref = ref("sticker", x)
	case m.GetLocationMessage() != nil:
		x := m.GetLocationMessage()
		msg.Type, ctx = model.WaLocation, x.GetContextInfo()
		msg.Text = place(x.GetName(), x.GetAddress(), x.GetDegreesLatitude(), x.GetDegreesLongitude())
	case m.GetLiveLocationMessage() != nil:
		x := m.GetLiveLocationMessage()
		msg.Type, ctx = model.WaLocation, x.GetContextInfo()
		msg.Text = place("Live location", x.GetCaption(), x.GetDegreesLatitude(), x.GetDegreesLongitude())
	case m.GetContactMessage() != nil:
		x := m.GetContactMessage()
		msg.Type, msg.Text, ctx = model.WaContact, x.GetDisplayName(), x.GetContextInfo()
	case m.GetContactsArrayMessage() != nil:
		x := m.GetContactsArrayMessage()
		var names []string
		for _, c := range x.GetContacts() {
			names = append(names, c.GetDisplayName())
		}
		msg.Type, msg.Text, ctx = model.WaContact, strings.Join(names, ", "), x.GetContextInfo()
	case m.GetPollCreationMessage() != nil || m.GetPollCreationMessageV2() != nil || m.GetPollCreationMessageV3() != nil:
		p := firstPoll(m)
		msg.Type, msg.Text = model.WaUnsupported, "Poll: "+p.GetName()
	case m.GetSenderKeyDistributionMessage() != nil || m.GetMessageContextInfo() != nil && isEmptyContent(m):
		in.Skip = true // encryption keys travelling with (or without) a message
		return
	default:
		msg.Type, msg.Text = model.WaUnsupported, "This kind of message isn't shown in tmx; open it on your phone."
	}
	if q := ctx.GetQuotedMessage(); q != nil && ctx.GetStanzaID() != "" {
		var tmp Incoming
		fill(&tmp, q, norm)
		msg.Quoted = &model.WaQuoted{ID: ctx.GetStanzaID(), Text: shorten(Describe(tmp.Msg), 200)}
		if p := ctx.GetParticipant(); p != "" {
			if j, err := types.ParseJID(p); err == nil {
				msg.Quoted.SenderName = norm(j) // the service shows the name
			}
		}
	}
}

func firstPoll(m *waE2E.Message) *waE2E.PollCreationMessage {
	for _, p := range []*waE2E.PollCreationMessage{m.GetPollCreationMessage(), m.GetPollCreationMessageV2(), m.GetPollCreationMessageV3()} {
		if p != nil {
			return p
		}
	}
	return nil
}

// isEmptyContent: nothing but context info (keys) in the message.
func isEmptyContent(m *waE2E.Message) bool {
	c := &waE2E.Message{MessageContextInfo: m.GetMessageContextInfo()}
	return m.String() == c.String()
}

// downloadable is what every attachment message has.
type downloadable interface {
	GetDirectPath() string
	GetURL() string
	GetMediaKey() []byte
	GetFileSHA256() []byte
	GetFileEncSHA256() []byte
	GetFileLength() uint64
}

func ref(kind string, x downloadable) *MediaRef {
	if x.GetDirectPath() == "" && x.GetURL() == "" {
		return nil
	}
	return &MediaRef{Kind: kind, DirectPath: x.GetDirectPath(), URL: x.GetURL(), MediaKey: x.GetMediaKey(),
		FileSHA256: x.GetFileSHA256(), FileEncSHA256: x.GetFileEncSHA256(), Size: int64(x.GetFileLength())}
}

func place(name, addr string, lat, lng float64) string {
	parts := []string{}
	for _, p := range []string{name, addr} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	parts = append(parts, fmt.Sprintf("%.5f, %.5f", lat, lng))
	return strings.Join(parts, " · ")
}

// stubText describes a history entry without content: a group created, someone added, …
func stubText(w *waWeb.WebMessageInfo) string {
	t := w.GetMessageStubType()
	if t == waWeb.WebMessageInfo_UNKNOWN {
		return ""
	}
	name := strings.ToLower(strings.ReplaceAll(t.String(), "_", " "))
	if p := w.GetMessageStubParameters(); len(p) > 0 && len(p[0]) < 60 && !strings.Contains(p[0], "@") {
		name += ": " + p[0]
	}
	return name
}
