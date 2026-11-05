package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

const chatUsage = `usage: tmx chat ls
       tmx chat read CHAT [--limit N]
       tmx chat send CHAT [-f FILE]... TEXT...
       tmx chat join LINK
       tmx chat invite [CHAT] [--hours N]
       tmx chat new NAME
       tmx chat members CHAT
       tmx chat leave CHAT [--yes]
       tmx chat agent CHAT AGENT|none
       tmx chat admin CHAT PERSON [--leave] | tmx chat admin CHAT --cancel
CHAT is a chat's ID or its name (case-insensitive).`

// cmdChat: chats with other people's tmx (the API's /api/chats and /api/invites), so a
// person or an agent on a headless server can take part without the app.
func cmdChat(e *env, args []string) error {
	if len(args) == 0 {
		args = []string{"ls"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "ls":
		return chatLs(e, rest)
	case "read":
		return chatRead(e, rest)
	case "send":
		return chatSend(e, rest)
	case "join":
		return chatJoin(e, rest)
	case "invite":
		return chatInvite(e, rest)
	case "new":
		return chatNew(e, rest)
	case "members":
		return chatMembers(e, rest)
	case "leave":
		return chatLeave(e, rest)
	case "agent":
		return chatAgent(e, rest)
	case "admin":
		return chatAdmin(e, rest)
	case "help", "-h", "--help":
		fmt.Fprintln(e.out, chatUsage)
		return nil
	}
	return usageErr("unknown chat command %q\n%s", sub, chatUsage)
}

// resolveChat finds a chat by ID, else by its (unique, case-insensitive) name.
func resolveChat(c *client.Client, cx context.Context, ref string) (model.Chat, error) {
	var list []model.Chat
	if err := c.Get(cx, "/api/chats", &list); err != nil {
		return model.Chat{}, err
	}
	ref = strings.TrimSpace(ref)
	for _, ch := range list {
		if ch.ID == ref {
			return ch, nil
		}
	}
	var hits []model.Chat
	for _, ch := range list {
		if strings.EqualFold(ch.Name, ref) {
			hits = append(hits, ch)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return model.Chat{}, fmt.Errorf("no chat %q (tmx chat ls lists them)", ref)
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = fmt.Sprintf("%s (%s, %s)", h.ID, h.Kind, h.Status)
	}
	return model.Chat{}, fmt.Errorf("%d chats are called %q: %s; give the ID", len(hits), ref, strings.Join(ids, ", "))
}

func chatLs(e *env, args []string) error {
	if _, err := parse(newFlags("chat ls"), args); err != nil {
		return err
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var list []model.Chat
		if err := c.Get(cx, "/api/chats", &list); err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Fprintln(e.out, "no chats yet (tmx chat join LINK, or tmx chat new NAME)")
			return nil
		}
		tw := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tKIND\tNAME\tSTATUS\tUNREAD\tMEMBERS\tAGENT")
		for _, ch := range list {
			members := "2"
			if ch.Kind == model.ChatGroup {
				members = strconv.Itoa(len(currentMembers(ch.Members)))
			}
			agent := ch.AgentName
			if agent == "" {
				agent = ch.AgentID
			}
			if agent == "" {
				agent = "(people only)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", ch.ID, ch.Kind, ch.Name, ch.Status, ch.Unread, members, agent)
		}
		return tw.Flush()
	})
}

func currentMembers(ms []model.ChatMember) []model.ChatMember {
	var out []model.ChatMember
	for _, m := range ms {
		if m.LeftAt == 0 {
			out = append(out, m)
		}
	}
	return out
}

// chatFile is an attached file as the API reports it (decoded leniently: the field is new).
type chatFile struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	State  string `json:"state"` // pending | ready | failed
	Path   string `json:"path"`
}

// chatLine is a ChatMessage plus its attached files.
type chatLine struct {
	model.ChatMessage
	Files []chatFile `json:"files,omitempty"`
}

func chatRead(e *env, args []string) error {
	fs := newFlags("chat read")
	limit := fs.Int("limit", 30, "how many of the newest messages")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *limit < 1 {
		return usageErr("usage: tmx chat read CHAT [--limit N]")
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		ch, err := resolveChat(c, cx, pos[0])
		if err != nil {
			return err
		}
		var page struct {
			Messages []chatLine `json:"messages"`
			HasMore  bool       `json:"has_more"`
		}
		if err := c.Get(cx, "/api/chats/"+client.Seg(ch.ID)+"/messages?limit="+strconv.Itoa(*limit), &page); err != nil {
			return err
		}
		if page.HasMore {
			fmt.Fprintf(e.errw, "(older messages: --limit %d)\n", *limit*2)
		}
		if len(page.Messages) == 0 {
			fmt.Fprintf(e.out, "no messages in %s yet\n", ch.Name)
		}
		for _, m := range page.Messages {
			printChatLine(e, m)
		}
		// Reading here reads the chat (as opening it in the app does).
		return c.Post(cx, "/api/chats/"+client.Seg(ch.ID)+"/read", nil, nil)
	})
}

func printChatLine(e *env, m chatLine) {
	at := time.UnixMilli(m.At).Format("2 Jan 15:04")
	body := strings.ReplaceAll(m.Body, "\n", "\n    ")
	if m.Sender == model.ChatSystem {
		fmt.Fprintf(e.out, "%s  · %s\n", at, body)
		return
	}
	who := m.SenderName
	if who == "" {
		who = m.Sender
	}
	if m.Sender == model.ChatMe {
		who += " (you)"
	}
	if m.SenderAgent != "" {
		who += "/" + m.SenderAgent
	}
	state := ""
	if m.Sender == model.ChatMe && (m.State == model.ChatQueued || m.State == model.ChatFailed) {
		state = "  [" + string(m.State) + "]"
	}
	fmt.Fprintf(e.out, "%s  %s: %s%s\n", at, who, body, state)
	for _, f := range m.Files {
		line := fmt.Sprintf("    file: %s (%s, %s", f.Name, orDash(f.Type), sizeText(f.Size))
		if f.State != "" && f.State != "ready" {
			line += ", " + f.State
		}
		line += ")"
		if f.Path != "" {
			line += " " + f.Path
		}
		fmt.Fprintln(e.out, line)
	}
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func chatSend(e *env, args []string) error {
	const usage = "usage: tmx chat send CHAT [-f FILE]... TEXT..."
	if len(args) < 2 {
		return usageErr(usage)
	}
	// As tmx send: only -f/--file is an option; any other word (even "-x") is text.
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
	// Read the files before touching the daemon: a typo fails without side effects.
	type upload struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Data string `json:"data"`
	}
	ups := make([]upload, 0, len(files))
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		ups = append(ups, upload{Name: filepath.Base(path), Data: base64.StdEncoding.EncodeToString(b)})
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		ch, err := resolveChat(c, cx, args[0])
		if err != nil {
			return err
		}
		req := map[string]any{"body": text}
		if len(ups) > 0 {
			ids := make([]string, 0, len(ups))
			for i, up := range ups {
				var f chatFile
				if err := c.Post(cx, "/api/chats/"+client.Seg(ch.ID)+"/files", up, &f); err != nil {
					return fmt.Errorf("%s: %w", files[i], err)
				}
				ids = append(ids, f.ID)
			}
			req["files"] = ids
		}
		var m model.ChatMessage
		if err := c.Post(cx, "/api/chats/"+client.Seg(ch.ID)+"/messages", req, &m); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "message %s in %s: %s\n", m.ID, ch.Name, m.State)
		return nil
	})
}

func chatJoin(e *env, args []string) error {
	pos, err := parse(newFlags("chat join"), args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: tmx chat join LINK   (a tmx://join/... link from an invite)")
	}
	link := strings.TrimSpace(pos[0])
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var pv p2p.InvitePreview
		if err := c.Post(cx, "/api/invites/preview", map[string]string{"link": link}, &pv); err != nil {
			return err
		}
		what := "a direct chat with " + pv.Name
		if pv.Kind == p2p.InviteGroup {
			what = fmt.Sprintf("the group %q (admin %s)", pv.GroupName, pv.Name)
		}
		fmt.Fprintf(e.errw, "joining %s ...\n", what)
		var res struct {
			Chat    model.Chat    `json:"chat"`
			Contact model.Contact `json:"contact"`
		}
		if err := c.Post(cx, "/api/invites/join", map[string]string{"link": link}, &res); err != nil {
			return err
		}
		ch := res.Chat
		fmt.Fprintf(e.out, "joined %s %s (%s), %s; contact %s, safety code %s\n", ch.Kind, ch.Name, ch.ID, ch.Status,
			res.Contact.Name, res.Contact.SafetyCode)
		if ch.Status == model.ChatPending {
			fmt.Fprintln(e.errw, "waiting for the member list from the group's admin (tmx chat ls shows it active once it arrives)")
		}
		return nil
	})
}

func chatInvite(e *env, args []string) error {
	fs := newFlags("chat invite")
	hours := fs.Float64("hours", 0, "valid for N hours (default: 24 for a DM, 168 for a group)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 || *hours < 0 {
		return usageErr("usage: tmx chat invite [CHAT] [--hours N]   (no CHAT: a DM invite; a group: an invite to it)")
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		req := map[string]any{"kind": p2p.InviteDM}
		if *hours > 0 {
			req["ttl_ms"] = int64(*hours * float64(time.Hour/time.Millisecond))
		}
		if len(pos) == 1 {
			ch, err := resolveChat(c, cx, pos[0])
			if err != nil {
				return err
			}
			if ch.Kind != model.ChatGroup {
				return fmt.Errorf("%s is a DM; invite someone to a direct chat with: tmx chat invite", ch.Name)
			}
			req["kind"], req["group_id"] = p2p.InviteGroup, ch.ID
		}
		var inv p2p.Invite
		if err := c.Post(cx, "/api/invites", req, &inv); err != nil {
			return err
		}
		fmt.Fprintln(e.out, inv.Link)
		what := "a direct chat with you"
		if inv.Kind == p2p.InviteGroup {
			what = fmt.Sprintf("the group %q", inv.GroupName)
		}
		fmt.Fprintf(e.errw, "invite %s to %s, valid until %s, %s; they join with: tmx chat join LINK (or in the app)\n",
			inv.ID, what, time.UnixMilli(inv.ExpiresAt).Format("Mon 2 Jan 15:04"), usesText(inv.MaxUses))
		return nil
	})
}

func chatNew(e *env, args []string) error {
	pos, err := parse(newFlags("chat new"), args)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(strings.Join(pos, " "))
	if name == "" {
		return usageErr("usage: tmx chat new NAME")
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var ch model.Chat
		if err := c.Post(cx, "/api/chats", map[string]string{"name": name}, &ch); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "group %s created (%s); invite people with: tmx chat invite %s\n", ch.Name, ch.ID, ch.ID)
		return nil
	})
}

func chatMembers(e *env, args []string) error {
	pos, err := parse(newFlags("chat members"), args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: tmx chat members CHAT")
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		ch, err := resolveChat(c, cx, pos[0])
		if err != nil {
			return err
		}
		var ms []model.ChatMember
		if err := c.Get(cx, "/api/chats/"+client.Seg(ch.ID)+"/members", &ms); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tROLE\tPEER ID\tNOTE")
		for _, m := range ms {
			var note []string
			if m.Me {
				note = append(note, "you")
			}
			if m.LeftAt != 0 {
				note = append(note, "left "+time.UnixMilli(m.LeftAt).Format("2 Jan 15:04"))
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", m.Name, m.Role, m.PeerID, strings.Join(note, ", "))
		}
		return tw.Flush()
	})
}

func chatLeave(e *env, args []string) error {
	fs := newFlags("chat leave")
	yes := fs.Bool("yes", false, "confirm closing a group you are the admin of")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: tmx chat leave CHAT [--yes]")
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		ch, err := resolveChat(c, cx, pos[0])
		if err != nil {
			return err
		}
		if ch.Admin && ch.Status == model.ChatActive && !*yes {
			return usageErr("you are the admin of %s: leaving closes the group for everyone; repeat with --yes", ch.Name)
		}
		var out model.Chat
		if err := c.Post(cx, "/api/chats/"+client.Seg(ch.ID)+"/leave", nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "%s: %s\n", out.Name, out.Status)
		return nil
	})
}

func chatAgent(e *env, args []string) error {
	pos, err := parse(newFlags("chat agent"), args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("usage: tmx chat agent CHAT AGENT|none   (the local agent that takes part; none = people only)")
	}
	agent := pos[1]
	if strings.EqualFold(agent, "none") {
		agent = ""
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		ch, err := resolveChat(c, cx, pos[0])
		if err != nil {
			return err
		}
		var out model.Chat
		if err := c.Do(cx, http.MethodPatch, "/api/chats/"+client.Seg(ch.ID), map[string]string{"agent_id": agent}, &out); err != nil {
			return err
		}
		if out.AgentID == "" {
			fmt.Fprintf(e.out, "%s: people only (no agent takes part)\n", out.Name)
			return nil
		}
		fmt.Fprintf(e.out, "%s: %s takes part\n", out.Name, orDash(out.AgentName))
		return nil
	})
}

// chatAdmin hands a group's admin role to another member (POST /api/chats/{id}/admin). The
// daemon always asks the owner on this machine first (Touch ID, Windows Hello), also while the
// owner gate is off; the member's tmx then takes over by itself.
func chatAdmin(e *env, args []string) error {
	const usage = "usage: tmx chat admin CHAT PERSON [--leave]   (PERSON: a member's name or peer ID)\n       tmx chat admin CHAT --cancel"
	fs := newFlags("chat admin")
	leave := fs.Bool("leave", false, "leave the group once the new admin has taken over")
	cancel := fs.Bool("cancel", false, "withdraw the offer that is waiting")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *cancel && len(pos) != 1 || !*cancel && len(pos) != 2 {
		return usageErr("%s", usage)
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		ch, err := resolveChat(c, cx, pos[0])
		if err != nil {
			return err
		}
		if ch.Kind != model.ChatGroup {
			return fmt.Errorf("%s is a DM: only a group has an admin", ch.Name)
		}
		path := "/api/chats/" + client.Seg(ch.ID) + "/admin"
		var out model.Chat
		if *cancel {
			if err := c.Do(cx, http.MethodDelete, path, nil, &out); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "%s: offer withdrawn; you are still the admin\n", out.Name)
			return nil
		}
		m, err := pickMember(ch, pos[1])
		if err != nil {
			return err
		}
		if err := ownerDo(e, c, cx, http.MethodPost, path, map[string]any{"peer_id": m.PeerID, "then_leave": *leave}, &out); err != nil {
			return err
		}
		if old := oldInstalls(out, m.PeerID); old > 0 {
			fmt.Fprintf(e.errw, "%d %s may need to update tmx first; until then their member list stays as it is.\n", old, plural(old, "member", "members"))
		}
		switch {
		case out.AdminOffer == nil && !out.Admin:
			fmt.Fprintf(e.out, "%s: %s is the admin now\n", out.Name, m.Name)
		default:
			fmt.Fprintf(e.out, "%s: waiting for %s's tmx; it takes over by itself as soon as the offer reaches it (tmx chat admin %q --cancel withdraws it)\n",
				out.Name, m.Name, ch.Name)
		}
		if *leave {
			fmt.Fprintln(e.out, "You leave the group once they have taken over.")
		}
		return nil
	})
}

// pickMember finds a current member of a group other than this install by peer ID or by its
// (unique, case-insensitive) name.
func pickMember(ch model.Chat, ref string) (model.ChatMember, error) {
	ref = strings.TrimSpace(ref)
	var hits []model.ChatMember
	for _, m := range currentMembers(ch.Members) {
		if m.Me {
			continue
		}
		if m.PeerID == ref {
			return m, nil
		}
		if strings.EqualFold(m.Name, ref) {
			hits = append(hits, m)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return model.ChatMember{}, fmt.Errorf("no other member %q in %s (tmx chat members %q lists them)", ref, ch.Name, ch.Name)
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.PeerID
	}
	return model.ChatMember{}, fmt.Errorf("%d members are called %q: %s; give the peer ID", len(hits), ref, strings.Join(ids, ", "))
}

// oldInstalls counts the other members whose tmx has not said it understands handovers.
func oldInstalls(ch model.Chat, newAdmin string) int {
	n := 0
	for _, m := range currentMembers(ch.Members) {
		if !m.Me && m.PeerID != newAdmin && !m.Handover {
			n++
		}
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
