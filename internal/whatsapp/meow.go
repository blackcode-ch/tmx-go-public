package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite" // whatsmeow's store, in secrets/whatsapp.db

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Meow is the Client on go.mau.fi/whatsmeow (MPL-2.0). Its store, secrets/whatsapp.db, holds
// the linked device's keys and sessions: whoever has it can read and send as the owner. It is
// a secret file in secrets/ (the guard keeps agents out), never logged, never served.
type Meow struct {
	path string
	log  *slog.Logger

	mu        sync.Mutex
	container *sqlstore.Container
	cli       *whatsmeow.Client
	h         func(Event)
	linkStop  context.CancelFunc
	handlerID uint32
}

// NewMeow is the whatsmeow client with its store at path (opened on Open).
func NewMeow(path string, log *slog.Logger) *Meow {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Meow{path: path, log: log.With("part", "whatsmeow")}
}

func init() {
	// How tmx shows up on the phone (Settings → Linked devices): "tmx", a desktop.
	wastore.SetOSInfo("tmx", [3]uint32{1, 0, 0})
	wastore.DeviceProps.PlatformType = waCompanionReg.DeviceProps_DESKTOP.Enum()
	wastore.DeviceProps.RequireFullSync = proto.Bool(false)
}

func (w *Meow) open(ctx context.Context) error {
	if w.container != nil {
		return nil
	}
	// Made private before SQLite opens it, and not opened at all if it cannot be. SQLite's -wal
	// and -shm files are private too: on Unix SQLite gives them the db's mode, on Windows they
	// inherit the secrets folder's DACL.
	if f, err := config.CreateSecretFile(w.path); err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	_ = os.Chmod(w.path, 0o600)
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	c, err := sqlstore.New(ctx, "sqlite", "file:"+filepath.ToSlash(w.path)+"?"+q.Encode(), slogLogger{w.log, "store"})
	if err != nil {
		return err
	}
	w.container = c
	return nil
}

// newClientLocked makes a client for the stored device, or for a new one.
func (w *Meow) newClientLocked(ctx context.Context) error {
	if w.cli != nil {
		w.cli.RemoveEventHandler(w.handlerID)
		w.cli.Disconnect()
		w.cli = nil
	}
	dev, err := w.container.GetFirstDevice(ctx)
	if err != nil {
		return err
	}
	cli := whatsmeow.NewClient(dev, slogLogger{w.log, "client"})
	cli.EmitAppStateEventsOnFullSync = true // pins, mutes, archives and contacts on the first sync too
	w.handlerID = cli.AddEventHandler(w.event)
	w.cli = cli
	return nil
}

func (w *Meow) Open(ctx context.Context, h func(Event)) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.h = h
	if err := w.open(ctx); err != nil {
		return false, err
	}
	if err := w.newClientLocked(ctx); err != nil {
		return false, err
	}
	if w.cli.Store.ID == nil {
		return false, nil
	}
	go func() {
		if err := w.cli.Connect(); err != nil {
			w.emit(EvFailed{Err: "could not connect to WhatsApp: " + err.Error()})
		}
	}()
	return true, nil
}

func (w *Meow) emit(ev Event) {
	w.mu.Lock()
	h := w.h
	w.mu.Unlock()
	if h != nil {
		h(ev)
	}
}

func (w *Meow) client() *whatsmeow.Client {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cli
}

func (w *Meow) Account() Account {
	cli := w.client()
	if cli == nil || cli.Store.ID == nil {
		return Account{}
	}
	j := cli.Store.ID.ToNonAD()
	return Account{JID: j.String(), Phone: "+" + j.User, Name: cli.Store.PushName}
}

// startLink connects a fresh device and follows its QR channel until it ends.
func (w *Meow) startLink(ctx context.Context, onFirst func(cli *whatsmeow.Client) error) error {
	w.mu.Lock()
	if w.linkStop != nil {
		w.linkStop()
	}
	if err := w.open(ctx); err != nil {
		w.mu.Unlock()
		return err
	}
	if w.cli != nil && w.cli.Store.ID != nil {
		w.mu.Unlock()
		return errors.New("a device is already linked")
	}
	if err := w.newClientLocked(ctx); err != nil {
		w.mu.Unlock()
		return err
	}
	lctx, stop := context.WithCancel(ctx)
	w.linkStop = stop
	cli := w.cli
	w.mu.Unlock()

	ch, err := cli.GetQRChannel(lctx)
	if err != nil {
		stop()
		return err
	}
	if err := cli.Connect(); err != nil {
		stop()
		return err
	}
	go func() {
		defer stop()
		first := true
		for {
			select {
			case <-lctx.Done():
				cli.Disconnect()
				w.emit(EvLinkEnded{})
				return
			case item, ok := <-ch:
				if !ok {
					return
				}
				switch item.Event {
				case whatsmeow.QRChannelEventCode:
					if first && onFirst != nil {
						first = false
						if err := onFirst(cli); err != nil {
							cli.Disconnect()
							w.emit(EvLinkEnded{Err: "the pairing code could not be made: " + err.Error()})
							return
						}
						continue
					}
					if onFirst == nil {
						w.emit(EvQR{Code: item.Code, Expires: time.Now().Add(item.Timeout)})
					}
				case "success":
					return // PairSuccess and Connected come as events
				case "timeout":
					w.emit(EvLinkEnded{Err: "no code was scanned in time; start again"})
					return
				case whatsmeow.QRChannelEventError:
					w.emit(EvLinkEnded{Err: "linking failed: " + fmt.Sprint(item.Error)})
					return
				case "err-client-outdated":
					w.emit(EvLinkEnded{Err: "WhatsApp says this version of tmx is outdated; update tmx"})
					return
				case "err-scanned-without-multidevice":
					w.emit(EvLinkEnded{Err: "the phone's WhatsApp is too old for linked devices; update it"})
					return
				case whatsmeow.QRChannelEventPasskeyRequest, whatsmeow.QRChannelEventPasskeyResponse:
					// Passkey pairing is not offered by tmx; the phone falls back to the code.
				default:
					w.emit(EvLinkEnded{Err: "linking stopped (" + item.Event + ")"})
					return
				}
			}
		}
	}()
	return nil
}

func (w *Meow) LinkQR(ctx context.Context) error { return w.startLink(ctx, nil) }

func (w *Meow) LinkPhone(ctx context.Context, phone string) error {
	return w.startLink(ctx, func(cli *whatsmeow.Client) error {
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		code, err := cli.PairPhone(pctx, phone, true, whatsmeow.PairClientChrome, "Chrome ("+osName()+")")
		if err != nil {
			return err
		}
		w.emit(EvPairingCode{Code: code})
		return nil
	})
}

func osName() string {
	switch runtime.GOOS {
	case "darwin":
		return "Mac OS"
	case "windows":
		return "Windows"
	}
	return "Linux"
}

func (w *Meow) CancelLink() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.linkStop != nil {
		w.linkStop()
		w.linkStop = nil
	}
}

func (w *Meow) Logout(ctx context.Context) error {
	w.CancelLink()
	cli := w.client()
	if cli == nil || cli.Store.ID == nil {
		return nil
	}
	var err error
	if cli.IsConnected() && cli.IsLoggedIn() {
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = cli.Logout(lctx) // tells WhatsApp, deletes the keys
		cancel()
	}
	if cli.Store.ID != nil { // offline, or the server did not answer: forget the keys here
		cli.Disconnect()
		if derr := cli.Store.Delete(ctx); derr != nil && err == nil {
			err = derr
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if nerr := w.newClientLocked(ctx); nerr != nil && err == nil {
		err = nerr
	}
	return err
}

func (w *Meow) NewMessageID() string {
	if cli := w.client(); cli != nil {
		return string(cli.GenerateMessageID())
	}
	return strings.ToUpper(model.NewID())
}

var mediaTypes = map[string]whatsmeow.MediaType{"image": whatsmeow.MediaImage, "video": whatsmeow.MediaVideo,
	"audio": whatsmeow.MediaAudio, "document": whatsmeow.MediaDocument, "sticker": whatsmeow.MediaImage}

func (w *Meow) Upload(ctx context.Context, f OutFile) (MediaRef, error) {
	cli := w.client()
	if cli == nil {
		return MediaRef{}, errors.New("not connected")
	}
	mt, ok := mediaTypes[f.Kind]
	if !ok {
		mt, f.Kind = whatsmeow.MediaDocument, "document"
	}
	up, err := cli.Upload(ctx, f.Data, mt)
	if err != nil {
		return MediaRef{}, err
	}
	return MediaRef{Kind: f.Kind, DirectPath: up.DirectPath, URL: up.URL, MediaKey: up.MediaKey, FileSHA256: up.FileSHA256,
		FileEncSHA256: up.FileEncSHA256, Size: int64(up.FileLength)}, nil
}

func (w *Meow) Send(ctx context.Context, m Outgoing) (time.Time, error) {
	cli := w.client()
	if cli == nil {
		return time.Time{}, errors.New("not connected")
	}
	to, err := types.ParseJID(m.Chat)
	if err != nil {
		return time.Time{}, err
	}
	var ci *waE2E.ContextInfo
	if q := m.Quote; q != nil {
		ci = &waE2E.ContextInfo{StanzaID: proto.String(q.ID), Participant: proto.String(q.Sender),
			QuotedMessage: &waE2E.Message{Conversation: proto.String(q.Text)}}
	}
	msg := &waE2E.Message{}
	switch {
	case m.File == nil:
		if ci == nil {
			msg.Conversation = proto.String(m.Text)
		} else {
			msg.ExtendedTextMessage = &waE2E.ExtendedTextMessage{Text: proto.String(m.Text), ContextInfo: ci}
		}
	case m.Media == nil:
		return time.Time{}, errors.New("the file was not uploaded")
	default:
		r, f := m.Media, m.File
		size := proto.Uint64(uint64(r.Size))
		switch r.Kind {
		case "image":
			msg.ImageMessage = &waE2E.ImageMessage{Caption: optional(m.Text), Mimetype: proto.String(f.Mime), URL: proto.String(r.URL),
				DirectPath: proto.String(r.DirectPath), MediaKey: r.MediaKey, FileEncSHA256: r.FileEncSHA256, FileSHA256: r.FileSHA256,
				FileLength: size, ContextInfo: ci}
		case "video":
			msg.VideoMessage = &waE2E.VideoMessage{Caption: optional(m.Text), Mimetype: proto.String(f.Mime), URL: proto.String(r.URL),
				DirectPath: proto.String(r.DirectPath), MediaKey: r.MediaKey, FileEncSHA256: r.FileEncSHA256, FileSHA256: r.FileSHA256,
				FileLength: size, ContextInfo: ci}
		case "audio":
			msg.AudioMessage = &waE2E.AudioMessage{Mimetype: proto.String(f.Mime), URL: proto.String(r.URL),
				DirectPath: proto.String(r.DirectPath), MediaKey: r.MediaKey, FileEncSHA256: r.FileEncSHA256, FileSHA256: r.FileSHA256,
				FileLength: size, ContextInfo: ci}
		default:
			msg.DocumentMessage = &waE2E.DocumentMessage{Caption: optional(m.Text), Mimetype: proto.String(f.Mime), Title: proto.String(f.Name),
				FileName: proto.String(f.Name), URL: proto.String(r.URL), DirectPath: proto.String(r.DirectPath), MediaKey: r.MediaKey,
				FileEncSHA256: r.FileEncSHA256, FileSHA256: r.FileSHA256, FileLength: size, ContextInfo: ci}
		}
	}
	resp, err := cli.SendMessage(ctx, to, msg, whatsmeow.SendRequestExtra{ID: types.MessageID(m.ID)})
	if err != nil {
		return time.Time{}, err
	}
	return resp.Timestamp, nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return proto.String(s)
}

func (w *Meow) Download(ctx context.Context, r MediaRef, f *os.File) error {
	cli := w.client()
	if cli == nil {
		return errors.New("not connected")
	}
	mt, ok := mediaTypes[r.Kind]
	if !ok {
		mt = whatsmeow.MediaDocument
	}
	mms := map[whatsmeow.MediaType]string{whatsmeow.MediaImage: "image", whatsmeow.MediaVideo: "video",
		whatsmeow.MediaAudio: "audio", whatsmeow.MediaDocument: "document"}[mt]
	key := r.MediaKey
	if r.FileEncSHA256 == nil {
		key = nil
	}
	return cli.DownloadMediaWithPathToFile(ctx, r.DirectPath, r.FileEncSHA256, r.FileSHA256, key, mt, mms, false, f)
}

func (w *Meow) MarkRead(ctx context.Context, chat, sender string, ids []string) error {
	cli := w.client()
	if cli == nil {
		return errors.New("not connected")
	}
	c, err := types.ParseJID(chat)
	if err != nil {
		return err
	}
	var s types.JID
	if strings.HasSuffix(chat, "@g.us") {
		if s, err = types.ParseJID(sender); err != nil {
			return err
		}
	}
	list := make([]types.MessageID, len(ids))
	for i, id := range ids {
		list[i] = types.MessageID(id)
	}
	return cli.MarkRead(ctx, list, time.Now(), c, s)
}

func (w *Meow) ChatName(ctx context.Context, jid string) string {
	cli := w.client()
	if cli == nil {
		return ""
	}
	j, err := types.ParseJID(jid)
	if err != nil {
		return ""
	}
	if j.Server == types.GroupServer {
		if g, err := cli.GetGroupInfo(ctx, j); err == nil {
			return g.Name
		}
		return ""
	}
	if c, err := cli.Store.Contacts.GetContact(ctx, j); err == nil && c.Found {
		return firstNonEmpty(c.FullName, c.FirstName, c.BusinessName, c.PushName)
	}
	return ""
}

func (w *Meow) Close() {
	w.CancelLink()
	w.mu.Lock()
	cli, c := w.cli, w.container
	w.cli, w.container = nil, nil
	w.mu.Unlock()
	if cli != nil {
		cli.RemoveEventHandler(w.handlerID)
		cli.Disconnect()
	}
	if c != nil {
		_ = c.Close()
	}
}

// norm is the JID tmx keys a chat or a person by: without the device, and the phone JID
// where WhatsApp gives a LID whose number it already told this device.
func (w *Meow) norm(j types.JID) string {
	if j.IsEmpty() {
		return ""
	}
	j = j.ToNonAD()
	if j.Server == types.HiddenUserServer {
		if cli := w.client(); cli != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			pn, err := cli.Store.LIDs.GetPNForLID(ctx, j)
			cancel()
			if err == nil && !pn.IsEmpty() {
				return pn.ToNonAD().String()
			}
		}
	}
	return j.String()
}

func (w *Meow) event(raw any) {
	switch e := raw.(type) {
	case *events.PairSuccess:
		j := e.ID.ToNonAD()
		w.emit(EvLinked{Account: Account{JID: j.String(), Phone: "+" + j.User, Name: e.BusinessName}})
	case *events.Connected:
		w.emit(EvConnected{Account: w.Account()})
		w.contacts()
	case *events.Disconnected, *events.KeepAliveTimeout:
		w.emit(EvDisconnected{})
	case *events.LoggedOut:
		reason := ""
		if e.OnConnect {
			reason = e.Reason.String()
		}
		w.emit(EvLoggedOut{Reason: reason})
	case *events.StreamReplaced:
		w.emit(EvFailed{Err: "another program connected with this device's keys, so tmx stopped; restart the daemon to take over again"})
	case *events.TemporaryBan:
		w.emit(EvFailed{Err: "WhatsApp banned this number for a while: " + e.String()})
	case *events.ClientOutdated:
		w.emit(EvFailed{Err: "WhatsApp says this version of tmx is outdated; update tmx"})
	case *events.ConnectFailure:
		w.emit(EvFailed{Err: fmt.Sprintf("WhatsApp refused the connection (%d %s)", e.Reason, e.Message)})
	case *events.Message:
		w.emit(EvMessage{In: convert(e, w.norm)})
	case *events.UndecryptableMessage:
		// whatsmeow asks the sender again; the message comes as a normal one if it can.
	case *events.HistorySync:
		w.history(e.Data)
	case *events.Receipt:
		st := model.WaMessageStatus("")
		switch e.Type {
		case types.ReceiptTypeDelivered:
			st = model.WaDelivered
		case types.ReceiptTypeRead, types.ReceiptTypePlayed:
			st = model.WaRead
		case types.ReceiptTypeReadSelf, types.ReceiptTypePlayedSelf:
			st = model.WaRead
		default:
			return
		}
		ids := make([]string, len(e.MessageIDs))
		for i, id := range e.MessageIDs {
			ids[i] = string(id)
		}
		mine := !e.IsFromMe // a receipt from someone else is about my message
		w.emit(EvReceipt{Chat: w.norm(e.Chat), IDs: ids, Status: st, Mine: mine})
	case *events.Pin:
		w.emit(EvChat{Chat: ChatUpdate{JID: w.norm(e.JID), Pinned: proto.Bool(e.Action.GetPinned())}})
	case *events.Mute:
		w.emit(EvChat{Chat: ChatUpdate{JID: w.norm(e.JID), Muted: proto.Bool(e.Action.GetMuted())}})
	case *events.Archive:
		w.emit(EvChat{Chat: ChatUpdate{JID: w.norm(e.JID), Archived: proto.Bool(e.Action.GetArchived())}})
	case *events.MarkChatAsRead:
		if e.Action.GetRead() {
			w.emit(EvChat{Chat: ChatUpdate{JID: w.norm(e.JID), Read: true}})
		}
	case *events.DeleteChat:
		w.emit(EvChat{Chat: ChatUpdate{JID: w.norm(e.JID), Delete: true}})
	case *events.Contact:
		w.emit(EvContacts{Contacts: []Contact{{JID: w.norm(e.JID), Name: firstNonEmpty(e.Action.GetFullName(), e.Action.GetFirstName())}}})
	case *events.PushName:
		w.emit(EvContacts{Contacts: []Contact{{JID: w.norm(e.JID), PushName: e.NewPushName}}})
	case *events.GroupInfo:
		if e.Name != nil {
			w.emit(EvChat{Chat: ChatUpdate{JID: w.norm(e.JID), Kind: model.WaGroup, Name: proto.String(e.Name.Name)}})
		}
	case *events.JoinedGroup:
		w.emit(EvChat{Chat: ChatUpdate{JID: w.norm(e.JID), Kind: model.WaGroup, Name: proto.String(e.Name)}})
	case *events.AppStateSyncComplete:
		w.contacts()
	}
}

// contacts hands every name the device knows to the service (after connecting and syncing).
func (w *Meow) contacts() {
	cli := w.client()
	if cli == nil || cli.Store.ID == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	all, err := cli.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return
	}
	list := make([]Contact, 0, len(all))
	for j, c := range all {
		list = append(list, Contact{JID: w.norm(j), Name: firstNonEmpty(c.FullName, c.FirstName, c.BusinessName), PushName: c.PushName})
	}
	if len(list) > 0 {
		w.emit(EvContacts{Contacts: list})
	}
}

func (w *Meow) history(d *waHistorySync.HistorySync) {
	cli := w.client()
	if cli == nil || d == nil {
		return
	}
	ev := EvHistory{Progress: int(d.GetProgress())}
	switch d.GetSyncType() {
	case waHistorySync.HistorySync_INITIAL_BOOTSTRAP, waHistorySync.HistorySync_RECENT, waHistorySync.HistorySync_FULL,
		waHistorySync.HistorySync_ON_DEMAND:
	default:
		ev.Progress = -1 // names and settings, not history
	}
	for _, p := range d.GetPushnames() {
		if j, err := types.ParseJID(p.GetID()); err == nil {
			ev.Contacts = append(ev.Contacts, Contact{JID: w.norm(j), PushName: p.GetPushname()})
		}
	}
	for _, conv := range d.GetConversations() {
		j, err := types.ParseJID(conv.GetID())
		if err != nil || j.Server == types.BroadcastServer || j.Server == types.NewsletterServer {
			continue
		}
		key := j
		if j.Server == types.HiddenUserServer && conv.GetPnJID() != "" {
			if pn, err := types.ParseJID(conv.GetPnJID()); err == nil {
				key = pn
			}
		}
		jid := w.norm(key)
		cu := ChatUpdate{JID: jid, Unread: new(int(conv.GetUnreadCount())), Archived: proto.Bool(conv.GetArchived()),
			Pinned: proto.Bool(conv.GetPinned() > 0), Muted: proto.Bool(conv.GetMuteEndTime() > uint64(time.Now().Unix())),
			LastAt: int64(conv.GetConversationTimestamp()) * 1000}
		if n := firstNonEmpty(conv.GetName(), conv.GetDisplayName()); n != "" {
			cu.Name = proto.String(n)
		}
		if j.Server == types.GroupServer {
			cu.Kind = model.WaGroup
		}
		ev.Chats = append(ev.Chats, cu)
		for _, hm := range conv.GetMessages() {
			evt, err := cli.ParseWebMessage(j, hm.GetMessage())
			if err != nil {
				continue
			}
			in := convert(evt, w.norm)
			in.Msg.ChatJID = jid
			ev.Messages = append(ev.Messages, in)
		}
	}
	w.emit(ev)
}

// slogLogger is whatsmeow's logger on the daemon's log: warnings and errors only (its debug
// output carries message contents).
type slogLogger struct {
	l      *slog.Logger
	module string
}

func (s slogLogger) Warnf(msg string, args ...any) {
	s.l.Warn(fmt.Sprintf(msg, args...), "module", s.module)
}
func (s slogLogger) Errorf(msg string, args ...any) {
	s.l.Error(fmt.Sprintf(msg, args...), "module", s.module)
}
func (s slogLogger) Infof(string, ...any)  {}
func (s slogLogger) Debugf(string, ...any) {}
func (s slogLogger) Sub(m string) waLog.Logger {
	return slogLogger{s.l, s.module + "/" + m}
}
