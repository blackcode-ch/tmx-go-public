package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/whatsapp"
	"github.com/blackcode-ch/tmx-go/internal/whatsapp/watest"
)

const (
	waAnna = "41791111111@s.whatsapp.net"
	waBo   = "41792222222@s.whatsapp.net"
)

func waSeed(d *apitest.Daemon) {
	t0 := time.Now().Add(-time.Hour).UnixMilli()
	photo := whatsapp.Incoming{Msg: model.WaMessage{ID: "A2", ChatJID: waAnna, SenderJID: waAnna, At: t0 + 2, Type: model.WaImage,
		Caption: "the view", Media: &model.WaMedia{Mime: "image/jpeg", Size: 5}},
		Ref: &whatsapp.MediaRef{Kind: "image", DirectPath: "/v/photo", Size: 5}}
	d.WaFake.Media["/v/photo"] = []byte("JPEG!")
	d.WaFake.Emit(whatsapp.EvHistory{
		Contacts: []whatsapp.Contact{{JID: waAnna, Name: "Anna Muster"}, {JID: waBo, PushName: "Bo"}},
		Messages: []whatsapp.Incoming{
			{Msg: model.WaMessage{ID: "A1", ChatJID: waAnna, SenderJID: waAnna, At: t0 + 1, Type: model.WaText, Text: "hello"}},
			photo,
			{Msg: model.WaMessage{ID: "B1", ChatJID: waBo, SenderJID: waBo, At: t0 + 3, Type: model.WaText, Text: "lunch?"}},
		},
		Progress: 100,
	})
}

// socket reads bus events of one caller.
type socket struct {
	t    *testing.T
	conn *websocket.Conn
	ctx  context.Context
}

func dialEvents(t *testing.T, d *apitest.Daemon, token string) *socket {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, strings.Replace(d.URL, "http://", "ws://", 1)+"/api/events?token="+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	s := &socket{t: t, conn: conn, ctx: ctx}
	s.next() // the snapshot
	return s
}

func (s *socket) next() model.BusEvent {
	s.t.Helper()
	_, b, err := s.conn.Read(s.ctx)
	if err != nil {
		s.t.Fatal(err)
	}
	var ev model.BusEvent
	json.Unmarshal(b, &ev)
	return ev
}

// until reads events until one of type typ (returns it) or the marker type (fails).
func (s *socket) until(typ string) model.BusEvent {
	s.t.Helper()
	for {
		if ev := s.next(); ev.Type == typ {
			return ev
		}
	}
}

func TestWhatsAppOwnerRoutes(t *testing.T) {
	d := apitest.New(t)
	d.EnableWhatsApp(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	head := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	ownerWS := dialEvents(t, d, d.OwnerToken)
	headWS := dialEvents(t, d, d.AgentToken(t, "head"))

	var st model.WaStatus
	if s := owner.do("GET", "/api/whatsapp", nil, &st); s != 200 || st.State != model.WaUnlinked || st.Settings.AgentsSend != "ask" ||
		st.Settings.AllowedChats == nil {
		t.Fatalf("status %d %+v", s, st)
	}
	if s := head.do("GET", "/api/whatsapp", nil, nil); s != 403 {
		t.Fatalf("agent status: %d", s)
	}
	if s := owner.do("GET", "/api/whatsapp/chats", nil, nil); s != 503 {
		t.Fatalf("chats while unlinked: %d", s)
	}
	if s := owner.do("POST", "/api/whatsapp/link", map[string]string{"method": "phone", "phone": "079"}, nil); s != 400 {
		t.Fatalf("bad phone: %d", s)
	}
	if s := owner.do("POST", "/api/whatsapp/link", map[string]string{"method": "qr"}, &st); s != 202 || st.State != model.WaLinking {
		t.Fatalf("link %d %+v", s, st)
	}
	qr := ownerWS.until(model.EvWaQR)
	if b, _ := json.Marshal(qr.Data); !strings.Contains(string(b), `"png":"data:image/png;base64,`) {
		t.Fatalf("qr event %s", b)
	}
	d.WaFake.Scan(watest.Self)
	waSeed(d)
	ownerWS.until(model.EvWaChat)

	var chats []model.WaChat
	if s := owner.do("GET", "/api/whatsapp/chats", nil, &chats); s != 200 || len(chats) != 2 || chats[0].JID != waBo || chats[1].Name != "Anna Muster" {
		t.Fatalf("chats %d %+v", s, chats)
	}
	if s := owner.do("GET", "/api/whatsapp/chats?q=anna", nil, &chats); s != 200 || len(chats) != 1 {
		t.Fatalf("search %d %+v", s, chats)
	}
	var msgs []model.WaMessage
	if s := owner.do("GET", "/api/whatsapp/chats/"+url.PathEscape(waAnna)+"/messages?limit=10", nil, &msgs); s != 200 || len(msgs) != 2 || msgs[0].ID != "A1" {
		t.Fatalf("messages %d %+v", s, msgs)
	}
	if s := owner.do("GET", "/api/whatsapp/chats/"+url.PathEscape(waAnna)+"/messages?before=A2", nil, &msgs); s != 200 || len(msgs) != 1 {
		t.Fatalf("page %d %+v", s, msgs)
	}
	var sent model.WaMessage
	body := map[string]any{"text": "here", "reply_to": "A1",
		"uploads": []map[string]string{{"name": "a.png", "type": "image/png", "data": base64.StdEncoding.EncodeToString([]byte("PNG"))}}}
	if s := owner.do("POST", "/api/whatsapp/chats/"+url.PathEscape(waAnna)+"/messages", body, &sent); s != 201 ||
		sent.Status != model.WaPending || sent.Caption != "here" || sent.Quoted == nil || sent.Origin != "owner" {
		t.Fatalf("send %d %+v", s, sent)
	}
	for {
		ev := ownerWS.until(model.EvWaMessage)
		var m model.WaMessage
		b, _ := json.Marshal(ev.Data)
		json.Unmarshal(b, &m)
		if m.ID == sent.ID && m.Status == model.WaSent {
			break
		}
	}
	if s := owner.do("POST", "/api/whatsapp/chats/"+url.PathEscape(waBo)+"/read", nil, nil); s != 204 {
		t.Fatalf("read %d", s)
	}
	var set model.WaSettings
	if s := owner.do("PUT", "/api/whatsapp/settings", map[string]any{"read_receipts": true, "allowed_chats": []string{waBo}}, &set); s != 200 ||
		!set.ReadReceipts || set.AllowedChats[0] != waBo || set.AgentsSend != "ask" {
		t.Fatalf("settings %d %+v", s, set)
	}
	if s := head.do("PUT", "/api/whatsapp/settings", map[string]any{"agents_send": "allowed"}, nil); s != 403 {
		t.Fatalf("agent changed settings: %d", s)
	}

	// The head's socket never carried a WhatsApp event (it gets this message mail event though).
	owner.do("POST", "/api/agents/head/send", map[string]string{"text": "marker"}, nil)
	for {
		ev := headWS.next()
		if strings.HasPrefix(ev.Type, "whatsapp.") {
			t.Fatalf("an agent got %s", ev.Type)
		}
		if ev.Type == model.EvMessageCreated {
			break
		}
	}
}

func TestWhatsAppMediaAndTickets(t *testing.T) {
	d := apitest.New(t)
	d.EnableWhatsApp(t)
	d.LinkWhatsApp(t)
	waSeed(d)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	path := "/api/whatsapp/media/" + url.PathEscape(waAnna) + "/A2"
	var data struct{ Type, Name, Data string }
	if s := owner.do("GET", path+"?data=1", nil, &data); s != 200 || data.Type != "image/jpeg" || data.Data != base64.StdEncoding.EncodeToString([]byte("JPEG!")) {
		t.Fatalf("data %d %+v", s, data)
	}
	if s, _, _ := raw(t, "GET", d.URL+path, d.AgentToken(t, "head"), nil, nil); s != 401 {
		t.Fatalf("an agent got media: %d", s)
	}
	var tk struct {
		Ticket    string
		ExpiresAt int64 `json:"expires_at"`
	}
	if s := owner.do("POST", path+"/ticket", nil, &tk); s != 200 || tk.Ticket == "" || tk.ExpiresAt == 0 {
		t.Fatalf("ticket %d %+v", s, tk)
	}
	s, b, h := raw(t, "GET", d.URL+path+"?ticket="+url.QueryEscape(tk.Ticket), "", nil, map[string]string{"Range": "bytes=1-3"})
	if s != 206 || string(b) != "PEG" || h.Get("Content-Type") != "image/jpeg" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("ticket get %d %q %v", s, b, h)
	}
	if s, _, _ := raw(t, "GET", d.URL+"/api/whatsapp/media/"+url.PathEscape(waAnna)+"/A1?ticket="+url.QueryEscape(tk.Ticket), "", nil, nil); s != 401 {
		t.Fatalf("ticket for another message: %d", s)
	}
	if s, _, _ := raw(t, "GET", d.URL+path+"?ticket=nope", "", nil, nil); s != 401 {
		t.Fatalf("bad ticket: %d", s)
	}
	if s := owner.do("GET", "/api/whatsapp/media/"+url.PathEscape(waAnna)+"/A1", nil, nil); s != 404 {
		t.Fatalf("no attachment: %d", s)
	}
}

func TestWhatsAppAgentsAskTheOwner(t *testing.T) {
	d := apitest.New(t)
	d.EnableWhatsApp(t)
	d.LinkWhatsApp(t)
	waSeed(d)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	head := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	owner.do("POST", "/api/projects", map[string]string{"name": "Shop"}, nil)
	lead := client{t: t, base: d.URL, token: d.AgentToken(t, "shop-lead")}

	var acc struct {
		Allowed bool
		Reason  string
	}
	if head.do("GET", "/api/whatsapp/access", nil, &acc); !acc.Allowed {
		t.Fatalf("head access %+v", acc)
	}
	if lead.do("GET", "/api/whatsapp/access", nil, &acc); acc.Allowed || !strings.Contains(acc.Reason, "head") {
		t.Fatalf("lead access %+v", acc)
	}
	if s := lead.do("GET", "/api/whatsapp/chats", nil, nil); s != 403 {
		t.Fatalf("lead read chats: %d", s)
	}
	var chats []model.WaChat
	if s := head.do("GET", "/api/whatsapp/chats", nil, &chats); s != 200 || len(chats) != 2 {
		t.Fatalf("head chats %d", s)
	}
	var msgs []model.WaMessage
	if s := head.do("GET", "/api/whatsapp/chats/Anna%20Muster/messages", nil, &msgs); s != 200 || len(msgs) != 2 {
		t.Fatalf("head messages by name %d %+v", s, msgs)
	}
	if s := head.do("POST", "/api/whatsapp/chats/Anna/messages", map[string]any{"text": "x",
		"uploads": []map[string]string{{"name": "a", "data": "eA=="}}}, nil); s != 403 {
		t.Fatalf("agent upload: %d", s)
	}
	var res whatsapp.AgentResult
	if s := head.do("POST", "/api/whatsapp/chats/Anna/messages", map[string]string{"text": "On my way"}, &res); s != 202 || res.Approval == nil {
		t.Fatalf("head send %d %+v", s, res)
	}
	var aps []model.Approval
	owner.do("GET", "/api/approvals", nil, &aps)
	if len(aps) != 1 || aps[0].Kind != model.ApprovalWhatsApp || aps[0].Question != "On my way" || !strings.Contains(aps[0].Description, "Anna Muster") {
		t.Fatalf("approvals %+v", aps)
	}
	// Another agent's snapshot does not show it; the head's does.
	var snap model.Snapshot
	lead.do("GET", "/api/snapshot", nil, &snap)
	if len(snap.Approvals) != 0 {
		t.Fatalf("lead sees %+v", snap.Approvals)
	}
	head.do("GET", "/api/snapshot", nil, &snap)
	if len(snap.Approvals) != 1 {
		t.Fatal("head does not see its own approval")
	}
	time.Sleep(30 * time.Millisecond)
	if len(d.WaFake.SentNow()) != 0 {
		t.Fatal("sent before Allow")
	}
	var ap model.Approval
	if s := owner.do("POST", "/api/approvals/"+aps[0].ID, map[string]string{"decision": "allow"}, &ap); s != 200 || ap.State != model.ApprovalAllowed {
		t.Fatalf("allow %d %+v", s, ap)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(d.WaFake.SentNow()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s := d.WaFake.SentNow(); len(s) != 1 || s[0].Text != "On my way" || s[0].Chat != waAnna {
		t.Fatalf("sent %+v", s)
	}
	var mail []model.Message
	head.do("GET", "/api/messages", nil, &mail)
	found := false
	for _, m := range mail {
		found = found || m.From == model.WhatsAppParty && strings.Contains(m.Body, "allowed")
	}
	if !found {
		t.Fatalf("head mail %+v", mail)
	}
	// A chat on the allowed list: at once.
	owner.do("PUT", "/api/whatsapp/settings", map[string]any{"allowed_chats": []string{waBo}}, nil)
	if s := head.do("POST", "/api/whatsapp/chats/"+url.PathEscape(waBo)+"/messages", map[string]string{"text": "sure"}, &res); s != 201 ||
		res.Message == nil || res.Message.Origin == "" {
		t.Fatalf("allowed chat %d %+v", s, res)
	}
}

func TestWhatsAppHiddenFromOtherAgents(t *testing.T) {
	d := apitest.New(t)
	d.EnableWhatsApp(t)
	d.LinkWhatsApp(t)
	waSeed(d)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	head := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	owner.do("POST", "/api/projects", map[string]string{"name": "Shop"}, nil)
	lead := client{t: t, base: d.URL, token: d.AgentToken(t, "shop-lead")}
	owner.do("PUT", "/api/whatsapp/settings", map[string]any{"notify_head_chats": []string{waAnna}}, nil)
	d.Proc(t, "head").State(model.StateIdle) // mail reaches an idle agent
	d.WaFake.Emit(whatsapp.EvMessage{In: whatsapp.Incoming{Msg: model.WaMessage{ID: "A9", ChatJID: waAnna, SenderJID: waAnna,
		At: time.Now().UnixMilli(), Type: model.WaText, Text: "secret plans"}}})

	headID := d.Fleet.HeadID()
	var tl struct{ Entries []model.TimelineEntry }
	deadline := time.Now().Add(5 * time.Second)
	for {
		head.do("GET", "/api/agents/"+headID+"/timeline", nil, &tl)
		if n := len(tl.Entries); n > 0 && strings.Contains(tl.Entries[n-1].Text, "secret plans") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no mail in the head's timeline: %+v", tl.Entries)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e := tl.Entries[len(tl.Entries)-1]; !strings.HasPrefix(e.Text, whatsapp.Frame) || e.Meta["from"] != model.WhatsAppParty {
		t.Fatalf("framing %+v", e)
	}
	lead.do("GET", "/api/agents/"+headID+"/timeline", nil, &tl)
	for _, e := range tl.Entries {
		if strings.Contains(e.Text, "secret plans") {
			t.Fatalf("the lead read %q", e.Text)
		}
	}
	owner.do("GET", "/api/agents/"+headID+"/timeline", nil, &tl)
	if !strings.Contains(tl.Entries[len(tl.Entries)-1].Text, "secret plans") {
		t.Fatal("the owner does not see it")
	}
}

func TestWhatsAppUnlinkIsGated(t *testing.T) {
	d := apitest.New(t)
	d.EnableWhatsApp(t)
	d.LinkWhatsApp(t)
	waSeed(d)
	owner := tc.New(d.URL, d.OwnerToken)
	// Turn the gate on (it needs a proof itself).
	if s, conf := call(t, owner, "PUT", "/api/gate", map[string]bool{"enabled": true}, nil); s != http.StatusPreconditionRequired {
		t.Fatalf("gate on: %d", s)
	} else if s, _ := call(t, confirmed(t, owner, conf), "PUT", "/api/gate", map[string]bool{"enabled": true}, nil); s != 0 {
		t.Fatalf("gate on retry: %d", s)
	}
	s, conf := call(t, owner, "POST", "/api/whatsapp/unlink", nil, nil)
	if s != http.StatusPreconditionRequired || !strings.Contains(conf.Reason, "unlink WhatsApp (+41790000000)") {
		t.Fatalf("unlink without proof: %d %+v", s, conf)
	}
	if d.WhatsApp.Status().State != model.WaConnected {
		t.Fatal("unlinked without a proof")
	}
	if s, _ := call(t, confirmed(t, owner, conf), "POST", "/api/whatsapp/unlink", nil, nil); s != 0 {
		t.Fatalf("unlink: %d", s)
	}
	if st := d.WhatsApp.Status(); st.State != model.WaUnlinked || !d.WaFake.LoggedOut {
		t.Fatalf("after unlink %+v", st)
	}
	if s, _ := call(t, owner, "GET", "/api/whatsapp/chats", nil, nil); s != http.StatusServiceUnavailable {
		t.Fatalf("chats after unlink: %d", s)
	}
}
