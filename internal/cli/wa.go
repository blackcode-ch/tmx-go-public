package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/coder/websocket"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/mcp"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/whatsapp"
)

// tmx wa: the owner's WhatsApp, linked to the daemon (DESIGN.md "WhatsApp").
const waUsage = `usage: tmx wa status | link [--phone +41…] | unlink --yes | chats [--limit N] [SEARCH...]
              | read CHAT [--limit N] | send CHAT [-f FILE]... TEXT...
  (CHAT: a name, a phone number with country code, or the JID that tmx wa chats shows)`

// WaRisk is the warning shown before linking.
const WaRisk = "WhatsApp doesn't officially support linking tools like tmx and can ban numbers that automate; tmx sends at a person's pace, but it is your number."

func cmdWa(e *env, args []string) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	switch args[0] {
	case "status":
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			var st model.WaStatus
			if err := c.Get(cx, "/api/whatsapp", &st); err != nil {
				return err
			}
			printWaStatus(e, st)
			return nil
		})
	case "link":
		fs := newFlags("wa link")
		phone := fs.String("phone", "", "link with a pairing code for this number (+41…) instead of a QR code")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}
		return waLink(e, *phone)
	case "unlink":
		fs := newFlags("wa unlink")
		yes := fs.Bool("yes", false, "confirm")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}
		if !*yes {
			return usageErr("this logs tmx out of WhatsApp and forgets its chats in tmx (your phone keeps them); repeat with --yes")
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			if err := ownerDo(e, c, cx, http.MethodPost, "/api/whatsapp/unlink", nil, nil); err != nil {
				return err
			}
			fmt.Fprintln(e.out, "WhatsApp unlinked; tmx forgot its chats")
			return nil
		})
	case "chats", "ls":
		fs := newFlags("wa chats")
		limit := fs.Int("limit", 30, "how many, newest activity first")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			var list []model.WaChat
			q := url.Values{"limit": {strconv.Itoa(*limit)}, "q": {strings.Join(pos, " ")}}
			if err := c.Get(cx, "/api/whatsapp/chats?"+q.Encode(), &list); err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(e.out, "no chats")
				return nil
			}
			tw := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tKIND\tUNREAD\tLAST\tJID")
			for _, ch := range list {
				last := ""
				if ch.Last != nil {
					last = time.UnixMilli(ch.Last.At).Format("2006-01-02 15:04") + "  " + cut(ch.Last.SenderName+": "+whatsapp.Describe(*ch.Last), 50)
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", cut(ch.Name, 30), ch.Kind, ch.Unread, last, ch.JID)
			}
			return tw.Flush()
		})
	case "read":
		fs := newFlags("wa read")
		limit := fs.Int("limit", 30, "how many of the latest messages")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) == 0 {
			return usageErr(waUsage)
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			ch, err := waResolve(c, cx, strings.Join(pos, " "))
			if err != nil {
				return err
			}
			var msgs []model.WaMessage
			if err := c.Get(cx, "/api/whatsapp/chats/"+client.Seg(ch.JID)+"/messages?limit="+strconv.Itoa(*limit), &msgs); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "%s (%s, %s)\n", ch.Name, ch.Kind, ch.JID)
			for _, m := range msgs {
				fmt.Fprint(e.out, mcp.FormatWaMessage(m))
			}
			return nil
		})
	case "send":
		return waSend(e, args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(e.out, waUsage)
		return nil
	}
	return usageErr("unknown wa command %q\n%s", args[0], waUsage)
}

func printWaStatus(e *env, st model.WaStatus) {
	line := "WhatsApp: " + string(st.State)
	if st.Phone != "" {
		line += ", " + st.Phone
	}
	if st.Name != "" {
		line += " (" + st.Name + ")"
	}
	fmt.Fprintln(e.out, line)
	if st.LinkedAt > 0 {
		fmt.Fprintln(e.out, "  linked "+time.UnixMilli(st.LinkedAt).Format("2006-01-02 15:04"))
	}
	if st.History.Syncing {
		fmt.Fprintf(e.out, "  history: syncing (%d%%)\n", st.History.Progress)
	}
	if st.Error != "" {
		fmt.Fprintln(e.out, "  "+st.Error)
	}
	s := st.Settings
	receipts := "off"
	if s.ReadReceipts {
		receipts = "on"
	}
	fmt.Fprintf(e.out, "  read receipts %s · agents: %s (%d chats allowed) · tell the head about %d chats · agent profiles: %s\n",
		receipts, s.AgentsSend, len(s.AllowedChats), len(s.NotifyHeadChats), strings.Join(s.AgentProfiles, ", "))
	if st.State == model.WaUnlinked || st.State == model.WaLoggedOut {
		fmt.Fprintln(e.out, "  link it: tmx wa link (or Settings → WhatsApp)")
	}
}

// waLink follows linking on the event socket: QR codes as half blocks, or the pairing code,
// until the phone linked tmx (or linking ended). Ctrl-C cancels.
func waLink(e *env, phone string) error {
	c, err := e.owner()
	if err != nil {
		return err
	}
	cx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cx, cancel := context.WithTimeout(cx, 6*time.Minute)
	defer cancel()
	ws := strings.Replace(strings.Replace(c.BaseURL, "https://", "wss://", 1), "http://", "ws://", 1) + "/api/events?token=" + url.QueryEscape(c.Token)
	conn, _, err := websocket.Dial(cx, ws, nil)
	if err != nil {
		if !healthy(c.BaseURL) {
			return fmt.Errorf("the tmx daemon is not running at %s (start it with: tmx up)", c.BaseURL)
		}
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 20) // the first frame is the whole snapshot
	fmt.Fprintln(e.errw, WaRisk)
	body := map[string]string{"method": "qr"}
	if phone != "" {
		body = map[string]string{"method": "phone", "phone": phone}
	}
	if err := c.Post(cx, "/api/whatsapp/link", body, nil); err != nil {
		return err
	}
	shown := ""
	for {
		_, b, err := conn.Read(cx)
		if err != nil {
			if cx.Err() != nil {
				ctx2, c2 := ctx()
				_ = c.Post(ctx2, "/api/whatsapp/link/cancel", nil, nil)
				c2()
				return errors.New("linking cancelled")
			}
			return err
		}
		var ev struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(b, &ev) != nil {
			continue
		}
		switch ev.Type {
		case model.EvWaQR:
			var qr model.WaQR
			json.Unmarshal(ev.Data, &qr)
			switch {
			case qr.PairingCode != "" && qr.PairingCode != shown:
				shown = qr.PairingCode
				fmt.Fprintf(e.out, "On your phone: WhatsApp → Settings → Linked devices → Link a device → Link with phone number instead,\nand type this code:\n\n    %s\n\n", qr.PairingCode)
			case qr.Code != "" && qr.Code != shown:
				shown = qr.Code
				art, err := whatsapp.QRText(qr.Code)
				if err != nil {
					return err
				}
				fmt.Fprintf(e.out, "\nScan with WhatsApp on your phone: Settings → Linked devices → Link a device.\n%s(a new code every ~20 s; Ctrl-C stops)\n", art)
			}
		case model.EvWaStatus:
			var st model.WaStatus
			json.Unmarshal(ev.Data, &st)
			switch st.State {
			case model.WaConnecting, model.WaConnected:
				if st.Phone != "" {
					fmt.Fprintf(e.out, "linked: %s; your chats arrive in the next minutes (tmx wa chats)\n", st.Phone)
					return nil
				}
			case model.WaUnlinked, model.WaError, model.WaLoggedOut:
				if st.Error != "" {
					return errors.New(st.Error)
				}
				if shown != "" {
					return errors.New("linking stopped")
				}
			}
		}
	}
}

// waResolve finds a chat by JID, number or name (unique).
func waResolve(c *client.Client, cx context.Context, ref string) (model.WaChat, error) {
	var list []model.WaChat
	if err := c.Get(cx, "/api/whatsapp/chats?limit=500&q="+url.QueryEscape(ref), &list); err != nil {
		return model.WaChat{}, err
	}
	for _, ch := range list {
		if ch.JID == ref || strings.EqualFold(ch.Name, ref) {
			return ch, nil
		}
	}
	switch len(list) {
	case 1:
		return list[0], nil
	case 0:
		return model.WaChat{}, fmt.Errorf("no WhatsApp chat %q (tmx wa chats lists them)", ref)
	}
	names := make([]string, 0, len(list))
	for _, ch := range list {
		names = append(names, ch.Name+" ("+ch.JID+")")
	}
	if len(names) > 8 {
		names = append(names[:8], "…")
	}
	return model.WaChat{}, fmt.Errorf("%q matches several chats: %s; use the JID", ref, strings.Join(names, ", "))
}

func waSend(e *env, args []string) error {
	const usage = "usage: tmx wa send CHAT [-f FILE]... TEXT..."
	if len(args) < 2 {
		return usageErr(usage)
	}
	// As tmx chat send: only -f/--file is an option; any other word is text.
	var files, words []string
	for i := 1; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			words = append(words, args[i+1:]...)
			i = len(args)
		case a == "-f" || a == "--file":
			if i+1 >= len(args) {
				return usageErr(usage)
			}
			files = append(files, args[i+1])
			i++
		case strings.HasPrefix(a, "--file="):
			files = append(files, strings.TrimPrefix(a, "--file="))
		default:
			words = append(words, a)
		}
	}
	text := strings.Join(words, " ")
	if strings.TrimSpace(text) == "" && len(files) == 0 {
		return usageErr(usage)
	}
	ups := make([]map[string]string, 0, len(files))
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		typ := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
		if typ == "" {
			typ = http.DetectContentType(b)
		}
		ups = append(ups, map[string]string{"name": filepath.Base(path), "type": typ, "data": base64.StdEncoding.EncodeToString(b)})
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		ch, err := waResolve(c, cx, args[0])
		if err != nil {
			return err
		}
		var m model.WaMessage
		if err := c.Post(cx, "/api/whatsapp/chats/"+client.Seg(ch.JID)+"/messages", map[string]any{"text": text, "uploads": ups}, &m); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "to %s: %s (%s)\n", ch.Name, m.Status, m.ID)
		return nil
	})
}

func cut(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// newWhatsApp is the daemon's WhatsApp service: whatsmeow with its keys in
// secrets/whatsapp.db, settings in config.json "whatsapp" (read at every use).
func newWhatsApp(paths config.Paths, st *store.Store, f *fleet.Fleet, log *slog.Logger) *whatsapp.Service {
	return whatsapp.New(whatsapp.Options{Store: st, Client: whatsapp.NewMeow(paths.WhatsAppDB(), log), MediaDir: paths.WhatsAppMediaDir(),
		Publish: f.Publish, Fleet: f, Logger: log,
		Config: func() *config.WhatsAppConfig {
			if c, err := config.Load(paths); err == nil {
				return c.WhatsApp
			}
			return nil
		},
		SaveConfig: func(fn func(*config.WhatsAppConfig)) error {
			c, err := config.Load(paths)
			if err != nil {
				return err
			}
			if c.WhatsApp == nil {
				c.WhatsApp = &config.WhatsAppConfig{}
			}
			fn(c.WhatsApp)
			return config.Save(paths, c)
		}})
}
