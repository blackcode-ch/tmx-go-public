package whatsapp

import (
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func plainNorm(j types.JID) string { return j.ToNonAD().String() }

func evt(m *waE2E.Message) *events.Message {
	chat := types.NewJID("41791111111", types.DefaultUserServer)
	return &events.Message{Info: types.MessageInfo{ID: "M1", PushName: "Anna", Timestamp: time.UnixMilli(1_700_000_000_000),
		MessageSource: types.MessageSource{Chat: chat, Sender: chat}}, Message: m}
}

func TestConvertKinds(t *testing.T) {
	anna := "41791111111@s.whatsapp.net"
	quoted := &waE2E.ContextInfo{StanzaID: proto.String("Q1"), Participant: proto.String("41792222222@s.whatsapp.net"),
		QuotedMessage: &waE2E.Message{Conversation: proto.String("the question")}}
	for _, tc := range []struct {
		name  string
		m     *waE2E.Message
		check func(t *testing.T, in Incoming)
	}{
		{"text", &waE2E.Message{Conversation: proto.String("hi")}, func(t *testing.T, in Incoming) {
			if in.Msg.Type != model.WaText || in.Msg.Text != "hi" || in.Msg.ChatJID != anna || in.Msg.SenderName != "Anna" || in.Msg.At != 1_700_000_000_000 {
				t.Fatalf("%+v", in.Msg)
			}
		}},
		{"reply", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("yes"), ContextInfo: quoted}}, func(t *testing.T, in Incoming) {
			q := in.Msg.Quoted
			if q == nil || q.ID != "Q1" || q.Text != "the question" || q.SenderName != "41792222222@s.whatsapp.net" {
				t.Fatalf("%+v", q)
			}
		}},
		{"photo", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("view"), Mimetype: proto.String("image/jpeg"),
			FileLength: proto.Uint64(1234), Width: proto.Uint32(640), Height: proto.Uint32(480), DirectPath: proto.String("/v/t62/x"),
			MediaKey: []byte{1}, FileSHA256: []byte{2}, FileEncSHA256: []byte{3}}}, func(t *testing.T, in Incoming) {
			if in.Msg.Type != model.WaImage || in.Msg.Caption != "view" || in.Msg.Media.Size != 1234 || in.Msg.Media.Width != 640 ||
				in.Ref == nil || in.Ref.DirectPath != "/v/t62/x" || in.Ref.Kind != "image" || string(in.Ref.MediaKey) != "\x01" {
				t.Fatalf("%+v %+v", in.Msg, in.Ref)
			}
		}},
		{"voice", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true), Seconds: proto.Uint32(12),
			Mimetype: proto.String("audio/ogg; codecs=opus"), DirectPath: proto.String("/v/a")}}, func(t *testing.T, in Incoming) {
			if in.Msg.Type != model.WaVoice || in.Msg.Media.DurationMS != 12000 || in.Ref.Kind != "audio" {
				t.Fatalf("%+v", in.Msg)
			}
			if d := Describe(in.Msg); d != "[voice note, 0:12]" {
				t.Fatalf("describe %q", d)
			}
		}},
		{"document", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("offer.pdf"), Mimetype: proto.String("application/pdf"),
			DirectPath: proto.String("/v/d")}}, func(t *testing.T, in Incoming) {
			if in.Msg.Type != model.WaDocument || in.Msg.Media.Name != "offer.pdf" || Describe(in.Msg) != "[document offer.pdf]" {
				t.Fatalf("%+v", in.Msg)
			}
		}},
		{"location", &waE2E.Message{LocationMessage: &waE2E.LocationMessage{Name: proto.String("Café"), DegreesLatitude: proto.Float64(47.37),
			DegreesLongitude: proto.Float64(8.54)}}, func(t *testing.T, in Incoming) {
			if in.Msg.Type != model.WaLocation || in.Msg.Text != "Café · 47.37000, 8.54000" {
				t.Fatalf("%+v", in.Msg)
			}
		}},
		{"contact", &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Bo")}}, func(t *testing.T, in Incoming) {
			if in.Msg.Type != model.WaContact || in.Msg.Text != "Bo" {
				t.Fatalf("%+v", in.Msg)
			}
		}},
		{"reaction", &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Key: &waCommon.MessageKey{ID: proto.String("T1")}, Text: proto.String("👍")}},
			func(t *testing.T, in Incoming) {
				if in.React != "T1" || in.Msg.Text != "👍" {
					t.Fatalf("%+v", in)
				}
			}},
		{"revoke", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String("T2")}}},
			func(t *testing.T, in Incoming) {
				if in.Revoke != "T2" || in.Skip {
					t.Fatalf("%+v", in)
				}
			}},
		{"edit", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key: &waCommon.MessageKey{ID: proto.String("T3")}, EditedMessage: &waE2E.Message{Conversation: proto.String("fixed")}}},
			func(t *testing.T, in Incoming) {
				if in.Edit != "T3" || in.Msg.Text != "fixed" {
					t.Fatalf("%+v", in)
				}
			}},
		{"key share", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_APP_STATE_SYNC_KEY_SHARE.Enum()}},
			func(t *testing.T, in Incoming) {
				if !in.Skip {
					t.Fatalf("%+v", in)
				}
			}},
		{"poll", &waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Dinner?")}}, func(t *testing.T, in Incoming) {
			if in.Msg.Type != model.WaUnsupported || !strings.Contains(in.Msg.Text, "Dinner?") {
				t.Fatalf("%+v", in.Msg)
			}
		}},
		{"unknown", &waE2E.Message{ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{}}, func(t *testing.T, in Incoming) {
			if in.Msg.Type != model.WaUnsupported || in.Skip {
				t.Fatalf("%+v", in.Msg)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.check(t, convert(evt(tc.m), plainNorm)) })
	}
}

func TestConvertSkipsStatusAndChannels(t *testing.T) {
	e := evt(&waE2E.Message{Conversation: proto.String("story")})
	e.Info.Chat = types.StatusBroadcastJID
	if !convert(e, plainNorm).Skip {
		t.Fatal("status broadcast kept")
	}
}

func TestQRCodes(t *testing.T) {
	png, err := QRPNG("2@abc,def,ghi")
	if err != nil || !strings.HasPrefix(png, "data:image/png;base64,iVBORw0KGgo") {
		t.Fatalf("png %q %v", png[:min(40, len(png))], err)
	}
	txt, err := QRText("2@abc,def,ghi")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(txt, "\n"), "\n")
	if len(lines) < 12 || !strings.HasPrefix(lines[0], "██") || len([]rune(lines[0])) != len([]rune(lines[3])) {
		t.Fatalf("text:\n%s", txt)
	}
}

func TestWindow(t *testing.T) {
	var w window
	t0 := time.Now()
	if ok, _ := w.take(t0, WindowMax); !ok {
		t.Fatal("full window refused")
	}
	ok, wait := w.take(t0.Add(time.Minute), 1)
	if ok || wait < Window-time.Minute-time.Second || wait > Window {
		t.Fatalf("over: %v %v", ok, wait)
	}
	if ok, _ := w.take(t0.Add(Window+time.Millisecond), 1); !ok {
		t.Fatal("not freed")
	}
	if ok, _ := w.take(t0, WindowMax+1); ok {
		t.Fatal("more than the window at once")
	}
}
