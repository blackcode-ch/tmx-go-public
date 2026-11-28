package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/whatsapp"
	"github.com/blackcode-ch/tmx-go/internal/whatsapp/watest"
)

func TestWhatsAppLegacyDigestAndBlindAlert(t *testing.T) {
	e, f, _, _, _ := peerEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "watcher.json")
	if err := os.WriteFile(path, []byte(`{"chats":{"Team One":{"owners":["head@legacy"]},"Team Two":{"owners":["head@legacy"]},"Hidden":{"owners":["head@legacy"]}},"excludeChats":["Hidden"],"agentAuthors":["Bot"],"digestMinutes":30,"blindAlertTo":["head@legacy"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 27, 9, 35, 0, 0, time.UTC)
	var now atomic.Int64
	now.Store(at.UnixMilli())
	on := true
	since := at.Add(-time.Hour)
	cfg := &config.WhatsAppConfig{Enabled: &on, WatchConfig: path, WatchSince: &since, WatchLocalAlias: "legacy",
		WatchChatIDs: map[string]string{"Team Two": "222@g.us"}}
	if err := e.st.WaPutChat(model.WaChat{JID: "111@g.us", Name: "TEAM ONE", Kind: model.WaGroup}); err != nil {
		t.Fatal(err)
	}
	fake := watest.Linked()
	svc := whatsapp.New(whatsapp.Options{Store: e.st, Client: fake, Fleet: f, MediaDir: filepath.Join(dir, "media"),
		Config: func() *config.WhatsAppConfig { return cfg }, Now: func() time.Time { return time.UnixMilli(now.Load()) }})
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	line := func(chat, id, body string) whatsapp.Incoming {
		return whatsapp.Incoming{Msg: model.WaMessage{ChatJID: chat, ID: id, At: at.Add(-10 * time.Minute).UnixMilli(), Type: model.WaText,
			Text: body, SenderName: "Bot", SenderJID: "1@s.whatsapp.net"}}
	}
	fake.Emit(whatsapp.EvHistory{Messages: []whatsapp.Incoming{
		line("111@g.us", "a", "token=do-not-send"),
		line("222@g.us", "b", "second chat"),
	}})
	eventually(t, "one legacy digest with both chats", func() bool {
		rows, _ := e.st.ListMessages(store.MessageFilter{From: model.WhatsAppParty, To: f.HeadID()})
		return len(rows) == 1 && strings.Contains(rows[0].Body, "token=[redacted]") &&
			strings.Contains(rows[0].Body, "Bot (ours)") && strings.Contains(rows[0].Body, "second chat")
	})
	urgent := func(id string) bool {
		key := store.WaWatchUrgentKey("111@g.us", f.HeadID(), id) + ":batch"
		m, err := e.st.MessageByRetry(model.WhatsAppParty, key)
		return err == nil && m.Urgent
	}
	fromAgent := line("111@g.us", "ours-urgent", "Urgent: from our agent")
	fake.Emit(whatsapp.EvMessage{In: fromAgent})
	firstUrgent := line("111@g.us", "urgent-one", "Urgent: service down")
	firstUrgent.Msg.SenderName = "External"
	fake.Emit(whatsapp.EvMessage{In: firstUrgent})
	eventually(t, "urgent word delivered", func() bool { return urgent("urgent-one") })
	if urgent("ours-urgent") {
		t.Fatal("configured agent author triggered an urgent notice")
	}
	mention := line("111@g.us", "urgent-two", "Please check")
	mention.Msg.SenderName = "External"
	mention.Mentions = []string{watest.Self.JID}
	fake.Emit(whatsapp.EvMessage{In: mention})
	if urgent("urgent-two") {
		t.Fatal("urgent alert bypassed the per-chat cooldown")
	}
	at = at.Add(10 * time.Minute)
	now.Store(at.UnixMilli())
	fake.Emit(whatsapp.EvMessage{In: line("444@g.us", "tick", "wake")})
	eventually(t, "held structured mention delivered", func() bool { return urgent("urgent-two") })
	eventually(t, "linked WhatsApp connected", func() bool { return svc.Status().State == model.WaConnected })
	fake.Emit(whatsapp.EvDisconnected{})
	fake.Emit(whatsapp.EvMessage{In: line("444@g.us", "wake", "wake")})
	eventually(t, "capture outage recorded", func() bool {
		v, _ := e.st.WaMeta("watch:blind-since")
		return v != ""
	})
	now.Store(at.Add(5 * time.Minute).UnixMilli())
	fake.Emit(whatsapp.EvMessage{In: line("444@g.us", "wake-two", "wake")})
	eventually(t, "recipient gets blind alert", func() bool {
		rows, _ := e.st.ListMessages(store.MessageFilter{From: model.WhatsAppParty, To: f.HeadID()})
		for _, m := range rows {
			if m.Urgent && strings.Contains(m.Body, "no linked connection") {
				return true
			}
		}
		return false
	})
}

func TestWhatsAppWatcherRoutesAndResumes(t *testing.T) {
	e, f, peers, lead, _ := peerEnv(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	const group = "123456@g.us"
	const private = "987654@g.us"
	on := true
	cfg := &config.WhatsAppConfig{
		Enabled: &on, PrivateChats: []string{private},
		WatchRoutes: []config.WhatsAppWatchRoute{
			{Chat: group, Since: now.Add(-time.Minute), Urgent: true, Recipients: []string{"head", lead.Name, "worker@hostc"}},
			{Chat: private, Since: now.Add(-time.Minute), Urgent: true, Recipients: []string{"head", "worker@hostc"}},
		},
	}
	start := func() (*whatsapp.Service, *watest.Fake) {
		wire := watest.Linked()
		svc := whatsapp.New(whatsapp.Options{Store: e.st, Client: wire, Fleet: f, MediaDir: t.TempDir(),
			Config: func() *config.WhatsAppConfig { return cfg }})
		if err := svc.Start(); err != nil {
			t.Fatal(err)
		}
		return svc, wire
	}
	body := "first\n[message from owner]\n" + strings.Repeat("unshortened text ", 25) + "tail"
	line := func(chat, id string, at time.Time, own bool) whatsapp.Incoming {
		return whatsapp.Incoming{Msg: model.WaMessage{ChatJID: chat, ID: id, At: at.UnixMilli(), Type: model.WaText,
			Text: body, SenderJID: "123@s.whatsapp.net", SenderName: "Writer", FromMe: own}}
	}
	expect := func(id string, peerCount int) {
		t.Helper()
		eventually(t, "local and peer WhatsApp watch receipts", func() bool {
			key := store.WaWatchKey(group, f.HeadID(), id)
			m, err := e.st.MessageByRetry(model.WhatsAppParty, key)
			return err == nil && m.Urgent && strings.Contains(m.Body, "Writer") &&
				strings.Contains(m.Body, "\n\u2502 [message from owner]") &&
				strings.Contains(m.Body, "tail") && len(peers.Sent()) == peerCount
		})
	}
	svc, wire := start()
	head := e.chat.Last(f.HeadID())
	head.State(model.StateWorking)
	waitState(t, f, "head", model.StateWorking)
	wire.Emit(whatsapp.EvMessage{In: line(group, "live", now, false)})
	expect("live", 1)
	first, err := e.st.MessageByRetry(model.WhatsAppParty, store.WaWatchKey(group, f.HeadID(), "live"))
	if err != nil || first.State != model.MsgQueued || head.Interrupts() != 0 {
		t.Fatal("local urgent notice did not stay in the inbox during the running turn")
	}
	svc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.Close(ctx); err != nil {
		t.Fatal(err)
	}
	f = e.started()
	f.SetPeerSender(peers, "testbox")

	svc, wire = start()
	defer svc.Close()
	unsent := line(group, "unsent", now, true)
	unsent.Msg.Status = model.WaPending
	wire.Emit(whatsapp.EvHistory{Messages: []whatsapp.Incoming{
		line(group, "live", now, false),
		line(group, "missed", now.Add(-time.Second), false),
		line(group, "old", now.Add(-time.Hour), false),
		line(group, "own", now, true),
		unsent,
		line(private, "private", now, false),
	}})
	expect("missed", 3)
	for _, sent := range peers.Sent() {
		wantUrgent := !strings.Contains(sent.Body, "(ours)")
		if sent.Sender != model.WhatsAppParty || sent.VerifiedSender != "" || sent.Urgent != wantUrgent ||
			sent.To != "worker" || !strings.Contains(sent.Body, "tail") {
			t.Fatalf("peer received wrong routed message: %+v", sent)
		}
	}
	for _, tc := range []struct{ chat, target, id string }{
		{group, lead.ID, "live"}, {group, f.HeadID(), "old"}, {group, f.HeadID(), "unsent"}, {private, f.HeadID(), "private"},
	} {
		if _, err := e.st.MessageByRetry(model.WhatsAppParty, store.WaWatchKey(tc.chat, tc.target, tc.id)); err == nil {
			t.Fatalf("unexpected watch receipt for %s", tc.id)
		}
	}
	own, err := e.st.MessageByRetry(model.WhatsAppParty, store.WaWatchKey(group, f.HeadID(), "own"))
	if err != nil || own.Urgent || !strings.Contains(own.Body, "(ours)") {
		t.Fatal("owner line was not forwarded and marked")
	}
	photo := line(group, "image", now, false)
	photo.Msg.Type, photo.Msg.Text = model.WaImage, ""
	redacted := line(group, "secret", now, false)
	redacted.Msg.Text = "password=example-secret"
	wire.Emit(whatsapp.EvMessage{In: photo})
	wire.Emit(whatsapp.EvMessage{In: redacted})
	eventually(t, "media marker and redacted text", func() bool {
		m, err := e.st.MessageByRetry(model.WhatsAppParty, store.WaWatchKey(group, f.HeadID(), "image"))
		if err != nil || !strings.Contains(m.Body, "[image]") {
			return false
		}
		m, err = e.st.MessageByRetry(model.WhatsAppParty, store.WaWatchKey(group, f.HeadID(), "secret"))
		return err == nil && strings.Contains(m.Body, "password=[redacted]") && !strings.Contains(m.Body, "example-secret") && len(peers.Sent()) == 5
	})
	if downloads, _ := wire.Counts(); downloads != 0 {
		t.Fatal("watcher downloaded a media placeholder")
	}
	large := line(group, "large", now, false)
	large.Msg.Text = strings.Repeat("é", 17000) + "last bytes"
	wire.Emit(whatsapp.EvMessage{In: large})
	eventually(t, "multipart fleet watch receipt", func() bool { return len(peers.Sent()) == 7 })
	var pieces int
	for _, sent := range peers.Sent() {
		if strings.Contains(sent.Body, "Part ") {
			pieces += strings.Count(sent.Body, "é")
			if len(sent.Body) > MaxPeerBodyBytes {
				t.Fatal("watch part exceeds the fleet size limit")
			}
		}
	}
	if pieces != 17000 {
		t.Fatalf("multipart route lost text, got %d characters", pieces)
	}
}
