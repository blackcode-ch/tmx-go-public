package whatsapp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// Fleet is what the service needs from the fleet: who an agent is, approvals and mail.
type Fleet interface {
	HeadID() string
	AgentProfile(idOrName string) (model.Profile, error)
	Agent(id string) (model.Agent, error)
	Approvals(state model.ApprovalState) ([]model.Approval, error)
	WhatsAppApproval(agentID string, in model.WaSendInput) (model.Approval, error)
	ExpireApproval(id, note string)
	DeliverWhatsApp(agentID, body string) (model.Message, error)
}

// Options configures the service.
type Options struct {
	Store    *store.Store
	Client   Client
	MediaDir string // whatsapp/media: attachments downloaded on demand
	Publish  func(model.BusEvent)
	// Config reads config.json "whatsapp" (nil = the defaults); SaveConfig changes and saves it.
	Config     func() *config.WhatsAppConfig
	SaveConfig func(func(*config.WhatsAppConfig)) error
	Fleet      Fleet // nil: no agents (tests)
	Logger     *slog.Logger
	Now        func() time.Time
	// Tests shorten the waits: ChatGap (0 = ChatGap) and NoticeQuiet (0 = NoticeQuiet).
	ChatGap     time.Duration
	NoticeQuiet time.Duration
}

// Error is a refusal with the HTTP status the API answers.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func errorf(status int, format string, a ...any) error {
	return &Error{Status: status, Msg: fmt.Sprintf(format, a...)}
}

// The refusals every route shares.
var (
	ErrOff          = &Error{http.StatusServiceUnavailable, "WhatsApp is off in this daemon (config.json whatsapp.enabled)"}
	ErrNotLinked    = &Error{http.StatusServiceUnavailable, "WhatsApp isn't linked: Settings → WhatsApp"}
	ErrNotConnected = &Error{http.StatusServiceUnavailable, "WhatsApp isn't connected right now; try again in a moment"}
)

// historyQuiet: the history sync counts as done when the phone sent nothing for this long.
const historyQuiet = 90 * time.Second

// Service is the linked WhatsApp account in the daemon.
type Service struct {
	o   Options
	log *slog.Logger
	key []byte // signs media tickets; lives only in memory
	win window

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	evMu sync.Mutex // one event at a time, in order

	mu        sync.Mutex
	st        model.WaStatus // without settings (read from the config on every use)
	acct      Account
	linking   context.CancelFunc
	linkHow   string
	lastQR    *model.WaQR
	histTimer *time.Timer
	queues    map[string]*chatQueue
	notices   map[string]*notice
	naming    map[string]bool
	downloads map[string]chan struct{}
	opened    bool
}

// New builds the service; Start connects a linked account.
func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Publish == nil {
		o.Publish = func(model.BusEvent) {}
	}
	if o.ChatGap <= 0 {
		o.ChatGap = ChatGap
	}
	if o.NoticeQuiet <= 0 {
		o.NoticeQuiet = NoticeQuiet
	}
	if o.Config == nil {
		o.Config = func() *config.WhatsAppConfig { return nil }
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{o: o, log: o.Logger.With("part", "whatsapp"), key: key, ctx: ctx, cancel: cancel,
		st: model.WaStatus{State: model.WaUnlinked}, queues: map[string]*chatQueue{}, notices: map[string]*notice{},
		naming: map[string]bool{}, downloads: map[string]chan struct{}{}}
}

// Enabled reports whether the service runs (config.json whatsapp.enabled, default on).
func (s *Service) Enabled() bool { return s != nil && s.o.Config().On() }

// Start opens the stored device and connects it. Messages tmx had not finished sending when
// the daemon stopped are marked failed: sending them again after a restart could send twice.
func (s *Service) Start() error {
	if !s.Enabled() || s.o.Client == nil {
		return nil
	}
	if times, err := s.o.Store.WaSentSince(s.o.Now().Add(-Window).UnixMilli()); err == nil {
		s.win.seed(times)
	}
	if pending, err := s.o.Store.WaPending(); err == nil {
		for _, m := range pending {
			m.Status, m.Error = model.WaFailed, "tmx restarted before it was sent; send it again"
			_ = s.o.Store.WaPutMessage(m)
		}
	}
	linked, err := s.o.Client.Open(s.ctx, s.handle)
	s.mu.Lock()
	s.opened = true
	if err != nil {
		s.st = model.WaStatus{State: model.WaError, Error: "WhatsApp's store could not be opened: " + err.Error()}
	} else if linked && s.st.State == model.WaUnlinked {
		// Open may deliver connection events before it returns. Initialize only
		// the untouched state; never overwrite a newer event with connecting.
		s.acct = s.o.Client.Account()
		s.st = model.WaStatus{State: model.WaConnecting, Phone: s.acct.Phone, Name: s.acct.Name, LinkedAt: s.linkedAt()}
	}
	s.mu.Unlock()
	s.publishStatus()
	return err
}

// Close disconnects (the device stays linked) and waits for sends in progress.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.cancel()
	s.mu.Lock()
	if s.linking != nil {
		s.linking()
	}
	if s.histTimer != nil {
		s.histTimer.Stop()
	}
	for _, n := range s.notices {
		n.timer.Stop()
	}
	opened := s.opened
	s.mu.Unlock()
	if opened && s.o.Client != nil {
		s.o.Client.Close()
	}
	s.wg.Wait()
}

// Status is the connection's state with the owner's settings.
func (s *Service) Status() model.WaStatus {
	s.mu.Lock()
	st := s.st
	s.mu.Unlock()
	st.Settings = s.Settings()
	return st
}

func (s *Service) publish(typ string, data any) {
	s.o.Publish(model.BusEvent{Type: typ, Data: data, At: model.Now()})
}

func (s *Service) publishStatus() { s.publish(model.EvWaStatus, s.Status()) }

// publishChat announces a chat that belongs in the chat list; a chat the phone hides (only
// system notices so far) is announced once a real message arrives.
func (s *Service) publishChat(c model.WaChat) {
	if s.o.Store.WaChatShown(c.JID) {
		s.publish(model.EvWaChat, c)
	}
}

// linked reports whether an account is linked (its chats can be read).
func (s *Service) linkedLocked() bool { return s.acct.JID != "" }

// ready checks the service is on and linked; connected also needs the connection up.
func (s *Service) ready(connected bool) error {
	if !s.Enabled() {
		return ErrOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.linkedLocked() {
		return ErrNotLinked
	}
	if connected && s.st.State != model.WaConnected {
		return ErrNotConnected
	}
	return nil
}

// ---- linking

var phoneRx = regexp.MustCompile(`^\+?[0-9][0-9 ()./-]{5,24}$`)

// Link starts linking: QR codes (method "qr") or a pairing code for phone ("phone"). Asking
// again while linking runs shows the current code again.
func (s *Service) Link(method, phone string) error {
	if !s.Enabled() {
		return ErrOff
	}
	if s.o.Client == nil {
		return errorf(http.StatusNotImplemented, "WhatsApp is not available in this daemon")
	}
	if method == "" {
		method = "qr"
	}
	switch method {
	case "qr":
	case "phone":
		if !phoneRx.MatchString(strings.TrimSpace(phone)) {
			return errorf(http.StatusBadRequest, "give the phone number with its country code, e.g. +41 79 123 45 67")
		}
		if digits := strings.Map(keepDigits, phone); strings.HasPrefix(digits, "0") || len(digits) < 7 || len(digits) > 15 {
			return errorf(http.StatusBadRequest, "give the phone number with its country code, e.g. +41 79 123 45 67")
		}
	default:
		return errorf(http.StatusBadRequest, `method must be "qr" or "phone"`)
	}
	s.mu.Lock()
	if s.linkedLocked() {
		phone := s.acct.Phone
		s.mu.Unlock()
		return errorf(http.StatusConflict, "WhatsApp is already linked (%s); unlink it first", phone)
	}
	if s.st.State == model.WaLinking && s.linkHow == method+phone {
		qr := s.lastQR
		s.mu.Unlock()
		if qr != nil {
			s.publish(model.EvWaQR, *qr)
		}
		return nil
	}
	if s.linking != nil {
		s.linking()
		s.o.Client.CancelLink()
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
	s.linking, s.linkHow, s.lastQR = cancel, method+phone, nil
	s.st = model.WaStatus{State: model.WaLinking}
	s.mu.Unlock()
	s.publishStatus()

	var err error
	if method == "qr" {
		err = s.o.Client.LinkQR(ctx)
	} else {
		err = s.o.Client.LinkPhone(ctx, "+"+strings.Map(keepDigits, phone))
	}
	if err != nil {
		cancel()
		s.mu.Lock()
		if s.st.State == model.WaLinking {
			s.st = model.WaStatus{State: model.WaUnlinked, Error: "linking failed: " + err.Error()}
			s.linking = nil
		}
		s.mu.Unlock()
		s.publishStatus()
		return errorf(http.StatusBadGateway, "linking failed: %v", err)
	}
	return nil
}

func keepDigits(r rune) rune {
	if r >= '0' && r <= '9' {
		return r
	}
	return -1
}

// CancelLink stops linking.
func (s *Service) CancelLink() error {
	if !s.Enabled() {
		return ErrOff
	}
	s.mu.Lock()
	was := s.st.State == model.WaLinking
	if s.linking != nil {
		s.linking()
		s.linking = nil
	}
	if was {
		s.st = model.WaStatus{State: model.WaUnlinked}
		s.lastQR = nil
	}
	s.mu.Unlock()
	if was && s.o.Client != nil {
		s.o.Client.CancelLink()
	}
	s.publishStatus()
	return nil
}

// Unlink logs this device out of WhatsApp and forgets everything tmx kept of the account: its
// chats, messages, contacts and downloaded files. Agents' messages still waiting expire.
func (s *Service) Unlink(ctx context.Context) error {
	if !s.Enabled() {
		return ErrOff
	}
	s.mu.Lock()
	if s.linking != nil {
		s.linking()
		s.linking = nil
	}
	s.mu.Unlock()
	if s.o.Client != nil {
		if err := s.o.Client.Logout(ctx); err != nil {
			s.log.Warn("whatsapp logout", "err", err) // the keys are gone either way
		}
	}
	s.forget("WhatsApp was unlinked")
	s.mu.Lock()
	s.st = model.WaStatus{State: model.WaUnlinked}
	s.mu.Unlock()
	s.publishStatus()
	return nil
}

// forget drops the account's data (unlinked, or logged out by the phone).
func (s *Service) forget(note string) {
	s.mu.Lock()
	s.acct, s.lastQR = Account{}, nil
	for _, n := range s.notices {
		n.timer.Stop()
	}
	s.notices = map[string]*notice{}
	if s.histTimer != nil {
		s.histTimer.Stop()
	}
	s.mu.Unlock()
	if err := s.o.Store.WaClear(); err != nil {
		s.log.Error("whatsapp clear", "err", err)
	}
	_ = s.o.Store.WaSetMeta("linked_at", "")
	_ = s.o.Store.WaSetMeta("account", "")
	if s.o.MediaDir != "" {
		if err := os.RemoveAll(s.o.MediaDir); err != nil {
			s.log.Warn("whatsapp media", "err", err)
		}
	}
	s.expireApprovals(note)
}

func (s *Service) linkedAt() int64 {
	v, _ := s.o.Store.WaMeta("linked_at")
	var at int64
	fmt.Sscan(v, &at)
	return at
}

// ---- settings

// Settings are the owner's settings (config.json "whatsapp").
func (s *Service) Settings() model.WaSettings { return s.o.Config().Settings() }

// SettingsPatch is PUT /api/whatsapp/settings; a field left out keeps its value.
type SettingsPatch struct {
	ReadReceipts    *bool     `json:"read_receipts"`
	AgentsSend      *string   `json:"agents_send"`
	AllowedChats    *[]string `json:"allowed_chats"`
	NotifyHeadChats *[]string `json:"notify_head_chats"`
	AgentProfiles   *[]string `json:"agent_profiles"`
}

// SetSettings changes the owner's settings and saves them.
func (s *Service) SetSettings(p SettingsPatch) (model.WaSettings, error) {
	if !s.Enabled() {
		return model.WaSettings{}, ErrOff
	}
	next := s.Settings()
	if p.ReadReceipts != nil {
		next.ReadReceipts = *p.ReadReceipts
	}
	if p.AgentsSend != nil {
		if *p.AgentsSend != model.WaAgentsAsk && *p.AgentsSend != model.WaAgentsAllowed {
			return next, errorf(http.StatusBadRequest, `agents_send must be "ask" or "allowed"`)
		}
		next.AgentsSend = *p.AgentsSend
	}
	lists := []struct {
		in   *[]string
		out  *[]string
		jids bool
	}{{p.AllowedChats, &next.AllowedChats, true}, {p.NotifyHeadChats, &next.NotifyHeadChats, true}, {p.AgentProfiles, &next.AgentProfiles, false}}
	for _, l := range lists {
		if l.in == nil {
			continue
		}
		clean := []string{}
		for _, v := range *l.in {
			v = strings.TrimSpace(v)
			if v == "" || slices.Contains(clean, v) {
				continue
			}
			if l.jids && !strings.Contains(v, "@") {
				return next, errorf(http.StatusBadRequest, "%q is not a chat (a WhatsApp JID like 41791234567@s.whatsapp.net)", v)
			}
			if !l.jids && !model.ValidName(v) {
				return next, errorf(http.StatusBadRequest, "%q is not a profile slug", v)
			}
			clean = append(clean, v)
		}
		if len(clean) > 500 {
			return next, errorf(http.StatusBadRequest, "at most 500 entries")
		}
		*l.out = clean
	}
	if s.o.SaveConfig == nil {
		return next, errorf(http.StatusNotImplemented, "settings cannot be saved in this daemon")
	}
	if err := s.o.SaveConfig(func(c *config.WhatsAppConfig) { c.SetSettings(next) }); err != nil {
		return next, err
	}
	s.publishStatus()
	return s.Settings(), nil
}

// ---- events from the connection

func (s *Service) handle(ev Event) {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	switch e := ev.(type) {
	case EvQR:
		png, err := QRPNG(e.Code)
		if err != nil {
			s.log.Warn("whatsapp qr", "err", err)
			return
		}
		qr := model.WaQR{Code: e.Code, PNG: png, ExpiresAt: e.Expires.UnixMilli()}
		s.mu.Lock()
		s.lastQR = &qr
		s.mu.Unlock()
		s.publish(model.EvWaQR, qr)
	case EvPairingCode:
		qr := model.WaQR{PairingCode: e.Code}
		s.mu.Lock()
		s.lastQR = &qr
		s.mu.Unlock()
		s.publish(model.EvWaQR, qr)
	case EvLinked:
		s.linked(e.Account)
	case EvLinkEnded:
		s.mu.Lock()
		if s.st.State == model.WaLinking {
			s.st = model.WaStatus{State: model.WaUnlinked, Error: e.Err}
			s.linking, s.lastQR = nil, nil
		}
		s.mu.Unlock()
		s.publishStatus()
	case EvConnected:
		s.mu.Lock()
		if e.Account.JID != "" {
			s.acct = e.Account
		}
		if s.acct.JID != "" {
			s.st.State, s.st.Error = model.WaConnected, ""
			s.st.Phone, s.st.Name = s.acct.Phone, s.acct.Name
		}
		s.mu.Unlock()
		s.publishStatus()
	case EvDisconnected:
		s.mu.Lock()
		changed := s.st.State == model.WaConnected
		if changed {
			s.st.State = model.WaConnecting
		}
		s.mu.Unlock()
		if changed {
			s.publishStatus()
		}
	case EvLoggedOut:
		s.forget("the phone logged tmx out of WhatsApp")
		s.mu.Lock()
		reason := "Your phone logged tmx out of WhatsApp. Link it again to continue."
		if e.Reason != "" {
			reason += " (" + e.Reason + ")"
		}
		s.st = model.WaStatus{State: model.WaLoggedOut, Error: reason}
		s.mu.Unlock()
		s.publishStatus()
	case EvFailed:
		s.mu.Lock()
		s.st.State, s.st.Error = model.WaError, e.Err
		s.mu.Unlock()
		s.publishStatus()
	case EvHistory:
		s.history(e)
	case EvMessage:
		s.apply(e.In, true)
	case EvReceipt:
		s.receipt(e)
	case EvChat:
		if c, ok := s.updateChat(e.Chat, false); ok {
			s.publishChat(c)
		}
	case EvContacts:
		for _, c := range e.Contacts {
			if err := s.o.Store.WaPutContact(c.JID, c.Name, c.PushName); err != nil {
				s.log.Warn("whatsapp contact", "err", err)
			}
		}
	}
}

// linked: the phone linked this device. A different account than before starts empty.
func (s *Service) linked(a Account) {
	if prev, _ := s.o.Store.WaMeta("account"); prev != "" && prev != a.JID {
		if err := s.o.Store.WaClear(); err != nil {
			s.log.Error("whatsapp clear", "err", err)
		}
	}
	now := s.o.Now().UnixMilli()
	_ = s.o.Store.WaSetMeta("account", a.JID)
	_ = s.o.Store.WaSetMeta("linked_at", fmt.Sprint(now))
	s.mu.Lock()
	s.acct, s.linking, s.lastQR = a, nil, nil
	s.st = model.WaStatus{State: model.WaConnecting, Phone: a.Phone, Name: a.Name, LinkedAt: now,
		History: model.WaHistory{Syncing: true}}
	s.armHistoryLocked()
	s.mu.Unlock()
	s.publishStatus()
}

func (s *Service) armHistoryLocked() {
	if s.histTimer != nil {
		s.histTimer.Stop()
	}
	s.histTimer = time.AfterFunc(historyQuiet, func() {
		s.mu.Lock()
		was := s.st.History.Syncing
		s.st.History = model.WaHistory{}
		s.mu.Unlock()
		if was {
			s.publishStatus()
		}
	})
}

// maxHistoryChats: a history part announces at most this many chats (the newest); clients
// read the whole list again when the sync is done.
const maxHistoryChats = 100

func (s *Service) history(e EvHistory) {
	for _, c := range e.Contacts {
		if err := s.o.Store.WaPutContact(c.JID, c.Name, c.PushName); err != nil {
			s.log.Warn("whatsapp contact", "err", err)
		}
	}
	// A one-to-one chat is stored only once it has a real message (see ghost).
	real := map[string]bool{}
	for _, in := range e.Messages {
		if !in.Skip && in.React == "" && in.Edit == "" && in.Revoke == "" && in.Msg.Type != model.WaSystem {
			real[in.Msg.ChatJID] = true
		}
	}
	touched := map[string]bool{}
	for _, cu := range e.Chats {
		if _, ok := s.updateChat(cu, real[cu.JID]); ok {
			touched[cu.JID] = true
		}
	}
	for _, in := range e.Messages {
		if s.apply(in, false) {
			touched[in.Msg.ChatJID] = true
		}
	}
	var chats []model.WaChat
	for jid := range touched {
		if c, err := s.o.Store.WaGetChat(jid); err == nil && s.o.Store.WaChatShown(jid) {
			chats = append(chats, s.chatView(c))
		}
	}
	slices.SortFunc(chats, func(a, b model.WaChat) int { return int(b.LastAt - a.LastAt) })
	if len(chats) > maxHistoryChats {
		chats = chats[:maxHistoryChats]
	}
	for _, c := range chats {
		s.publishChat(c)
	}
	s.mu.Lock()
	if e.Progress >= 100 {
		s.st.History = model.WaHistory{}
		if s.histTimer != nil {
			s.histTimer.Stop()
		}
	} else {
		s.st.History.Syncing = true
		if e.Progress > s.st.History.Progress {
			s.st.History.Progress = e.Progress
		}
		s.armHistoryLocked()
	}
	s.mu.Unlock()
	s.publishStatus()
}

// ghost: a one-to-one chat tmx does not have yet, that this change would create although it has
// no real message. WhatsApp's history sync carries a conversation for nearly everyone the account
// ever had an encrypted session with (group members, businesses, chats deleted on the phone),
// holding only "messages are end-to-end encrypted" or a business privacy notice, and the app
// state names chats that are long gone (muted, archived, read). The phone shows none of them;
// tmx does not store them. A group, a pinned chat or one with unread messages is never a ghost;
// a ghost becomes a chat when a real message arrives.
func ghost(u ChatUpdate, hasMessages bool) bool {
	return !hasMessages && kindOf(u.JID, u.Kind) == model.WaDM &&
		(u.Pinned == nil || !*u.Pinned) && (u.Unread == nil || *u.Unread <= 0)
}

// updateChat applies a change to a chat; ok when the chat exists afterwards. A chat tmx does not
// have yet is created, unless it is a ghost (hasMessages: the change comes with real messages).
func (s *Service) updateChat(u ChatUpdate, hasMessages bool) (model.WaChat, bool) {
	if u.JID == "" {
		return model.WaChat{}, false
	}
	if u.Delete {
		if err := s.o.Store.WaDeleteChat(u.JID); err != nil {
			s.log.Warn("whatsapp delete chat", "err", err)
		}
		return model.WaChat{}, false
	}
	c, err := s.o.Store.WaGetChat(u.JID)
	if errors.Is(err, store.ErrNotFound) {
		if ghost(u, hasMessages) {
			return model.WaChat{}, false
		}
		c = model.WaChat{JID: u.JID, Kind: kindOf(u.JID, u.Kind)}
	} else if err != nil {
		s.log.Warn("whatsapp chat", "err", err)
		return c, false
	}
	if u.Name != nil && *u.Name != "" {
		c.Name = *u.Name
	}
	if u.Unread != nil {
		c.Unread = max(0, *u.Unread)
	}
	if u.Pinned != nil {
		c.Pinned = *u.Pinned
	}
	if u.Muted != nil {
		c.Muted = *u.Muted
	}
	if u.Archived != nil {
		c.Archived = *u.Archived
	}
	if u.Read {
		c.Unread = 0
	}
	c.LastAt = max(c.LastAt, u.LastAt)
	if err := s.o.Store.WaPutChat(c); err != nil {
		s.log.Warn("whatsapp chat", "err", err)
		return c, false
	}
	s.nameLater(c)
	return s.chatView(c), true
}

func kindOf(jid string, k model.WaChatKind) model.WaChatKind {
	if k != "" {
		return k
	}
	if strings.HasSuffix(jid, "@g.us") {
		return model.WaGroup
	}
	return model.WaDM
}

// nameLater asks WhatsApp for the name of a chat that has none yet (once per chat).
func (s *Service) nameLater(c model.WaChat) {
	if c.Name != "" || s.o.Client == nil {
		return
	}
	if n := s.o.Store.WaContactName(c.JID); n != "" && c.Kind == model.WaDM {
		return // chatView shows the contact's name
	}
	s.mu.Lock()
	if s.naming[c.JID] {
		s.mu.Unlock()
		return
	}
	s.naming[c.JID] = true
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
		defer cancel()
		name := s.o.Client.ChatName(ctx, c.JID)
		if name == "" {
			return
		}
		s.evMu.Lock()
		defer s.evMu.Unlock()
		cur, err := s.o.Store.WaGetChat(c.JID)
		if err != nil || cur.Name != "" {
			return
		}
		cur.Name = name
		if s.o.Store.WaPutChat(cur) == nil {
			s.publishChat(s.chatView(cur))
		}
	}()
}

// apply stores a message from WhatsApp, or applies its change (an edit, a deletion, a
// reaction). live: it just happened (counts as unread, announced, may tell the head). It
// reports whether a chat changed.
func (s *Service) apply(in Incoming, live bool) bool {
	m := in.Msg
	if in.Skip || m.ChatJID == "" || m.ID == "" && in.React == "" && in.Edit == "" && in.Revoke == "" {
		return false
	}
	switch {
	case in.React != "":
		by, name := m.SenderJID, m.SenderName
		if m.FromMe {
			by, name = s.selfJID(), "You"
		}
		if err := s.o.Store.WaPutReaction(m.ChatJID, in.React, by, name, m.Text, m.At); err != nil {
			s.log.Warn("whatsapp reaction", "err", err)
			return false
		}
		if live {
			s.publishMessage(m.ChatJID, in.React)
		}
		return false
	case in.Edit != "" || in.Revoke != "":
		target := in.Edit + in.Revoke
		row, err := s.o.Store.WaGetMessage(m.ChatJID, target)
		if err != nil {
			return false // not synced: nothing to change
		}
		if in.Revoke != "" {
			row.Deleted, row.Text, row.Caption, row.Media, row.MediaRef, row.Quoted = true, "", "", nil, "", nil
			s.removeFile(row.MediaFile)
			row.MediaFile = ""
		} else if row.Media != nil {
			row.Caption, row.Edited = firstNonEmpty(m.Caption, m.Text), true
		} else {
			row.Text, row.Edited = firstNonEmpty(m.Text, m.Caption), true
		}
		if err := s.o.Store.WaPutMessage(row); err != nil {
			s.log.Warn("whatsapp edit", "err", err)
			return false
		}
		if live {
			s.publish(model.EvWaMessage, s.messageView(row))
		}
		return false
	}
	row := store.WaMessageRow{WaMessage: m}
	if q := m.Quoted; q != nil && strings.Contains(q.SenderName, "@") { // the client names people by JID
		named := *q
		named.SenderName = s.personName(q.SenderName)
		row.Quoted = &named
	}
	if in.Ref != nil {
		row.MediaRef = encodeRef(*in.Ref)
	}
	if row.Status == "" {
		row.Status = model.WaDelivered
		if m.FromMe {
			row.Status = model.WaSent
		}
	}
	old, err := s.o.Store.WaGetMessage(m.ChatJID, m.ID)
	known := err == nil
	if !known && m.Type == model.WaSystem && kindOf(m.ChatJID, "") == model.WaDM {
		if _, err := s.o.Store.WaGetChat(m.ChatJID); errors.Is(err, store.ErrNotFound) {
			return false // a notice alone does not make a chat (ghost)
		}
	}
	if known { // history overlapping live messages, or tmx's own message coming back
		row.Origin, row.MediaFile = old.Origin, old.MediaFile
		if model.WaStatusRank(old.Status) > model.WaStatusRank(row.Status) {
			row.Status = old.Status
		}
		if old.Deleted {
			return false
		}
		if row.MediaRef == "" {
			row.MediaRef = old.MediaRef
		}
	}
	if err := s.o.Store.WaPutMessage(row); err != nil {
		s.log.Warn("whatsapp message", "err", err)
		return false
	}
	c, err := s.o.Store.WaGetChat(m.ChatJID)
	if errors.Is(err, store.ErrNotFound) {
		c, err = model.WaChat{JID: m.ChatJID, Kind: kindOf(m.ChatJID, "")}, nil
	}
	if err != nil {
		return false
	}
	c.LastAt = max(c.LastAt, m.At)
	if live && !known && !m.FromMe {
		c.Unread++
	}
	if live && m.FromMe && !known {
		c.Unread = 0 // the owner wrote from the phone: they have seen the chat
	}
	if err := s.o.Store.WaPutChat(c); err != nil {
		s.log.Warn("whatsapp chat", "err", err)
	}
	s.nameLater(c)
	if live {
		if cur, err := s.o.Store.WaGetMessage(m.ChatJID, m.ID); err == nil {
			v := s.messageView(cur)
			s.publish(model.EvWaMessage, v)
			if !known && !m.FromMe {
				s.tellHead(v)
			}
		}
		if cur, err := s.o.Store.WaGetChat(m.ChatJID); err == nil {
			s.publishChat(s.chatView(cur))
		}
	}
	return true
}

func (s *Service) receipt(e EvReceipt) {
	changed, err := s.o.Store.WaSetStatus(e.Chat, e.IDs, e.Status)
	if err != nil {
		s.log.Warn("whatsapp receipt", "err", err)
	}
	for _, m := range changed {
		s.publish(model.EvWaMessage, s.messageView(m))
	}
	if !e.Mine && e.Status == model.WaRead { // read on the phone: the chat is read
		if c, ok := s.updateChat(ChatUpdate{JID: e.Chat, Read: true}, false); ok {
			s.publishChat(c)
		}
	}
}

func (s *Service) selfJID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acct.JID
}

// personName is how the owner knows someone: "You", the contact's name, else the number.
func (s *Service) personName(jid string) string {
	if self := s.selfJID(); jid == self && self != "" {
		return "You"
	}
	return firstNonEmpty(s.o.Store.WaContactName(jid), phoneOf(jid), "Someone")
}

func (s *Service) publishMessage(chat, id string) {
	if row, err := s.o.Store.WaGetMessage(chat, id); err == nil {
		s.publish(model.EvWaMessage, s.messageView(row))
	}
}

func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}

func (s *Service) expireApprovals(note string) {
	if s.o.Fleet == nil {
		return
	}
	list, err := s.o.Fleet.Approvals(model.ApprovalPending)
	if err != nil {
		return
	}
	for _, ap := range list {
		if ap.Kind == model.ApprovalWhatsApp {
			s.o.Fleet.ExpireApproval(ap.ID, note)
		}
	}
}
