package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

const p2pUsage = `usage: tmx p2p status
       tmx p2p invite [--group ID --group-name NAME] [--hours N] [--uses N]
       tmx p2p invites | revoke ID
       tmx p2p preview LINK | join LINK
       tmx p2p name [NEW NAME]             the name people see in invites and chats (show or change)
       tmx p2p peers
       tmx p2p send PEER_ID [TEXT...]      a raw "ping" envelope (diagnostics)
       tmx p2p log                         what the daemon received (config p2p.debug_drain only)`

// cmdP2P: the network layer's diagnostics (status, invites, pairing, raw envelopes).
func cmdP2P(e *env, args []string) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	sub, rest := args[0], args[1:]
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		switch sub {
		case "status":
			var st api.P2PStatus
			if err := c.Get(cx, "/api/p2p", &st); err != nil {
				return err
			}
			if !st.Enabled {
				fmt.Fprintln(e.out, "peer-to-peer: off")
				return nil
			}
			fmt.Fprintf(e.out, "peer-to-peer: on, %s\n  name:    %s\n  peer id: %s\n  peers:   %d connected\n", st.Reachability, st.Name, st.PeerID, st.Peers)
			for _, a := range st.Addrs {
				fmt.Fprintf(e.out, "  addr:    %s\n", a)
			}
		case "invite":
			fs := newFlags("p2p invite")
			group := fs.String("group", "", "group ID (a group invite)")
			groupName := fs.String("group-name", "", "group name shown to joiners")
			hours := fs.Float64("hours", 0, "valid for N hours (default: 24 for a person, 168 for a group)")
			uses := fs.Int("uses", 0, "how many people may use it (default: 1 for a person, unlimited for a group)")
			if _, err := parse(fs, rest); err != nil {
				return err
			}
			req := api.InviteRequest{Kind: p2p.InviteDM, TTLMillis: int64(*hours * float64(time.Hour/time.Millisecond)), MaxUses: *uses}
			if *group != "" {
				req.Kind, req.GroupID, req.GroupName = p2p.InviteGroup, *group, *groupName
			}
			var inv p2p.Invite
			if err := c.Post(cx, "/api/p2p/net/invites", req, &inv); err != nil {
				return err
			}
			fmt.Fprintln(e.out, inv.Link)
			fmt.Fprintf(e.errw, "invite %s: %s, valid until %s, %s\n", inv.ID, inv.Kind,
				time.UnixMilli(inv.ExpiresAt).Format("Mon 2 Jan 15:04"), usesText(inv.MaxUses))
		case "invites":
			var list []p2p.Invite
			if err := c.Get(cx, "/api/p2p/net/invites", &list); err != nil {
				return err
			}
			for _, in := range list {
				state := "open"
				switch {
				case in.Revoked:
					state = "revoked"
				case in.ExpiresAt <= time.Now().UnixMilli():
					state = "expired"
				case in.MaxUses > 0 && in.Uses >= in.MaxUses:
					state = "used"
				}
				fmt.Fprintf(e.out, "%s  %-5s  %-7s  used %d  until %s  %s\n", in.ID, in.Kind, state, in.Uses,
					time.UnixMilli(in.ExpiresAt).Format("2 Jan 15:04"), in.GroupName)
			}
		case "revoke":
			if len(rest) != 1 {
				return usageErr(p2pUsage)
			}
			return c.Do(cx, http.MethodDelete, "/api/p2p/net/invites/"+client.Seg(rest[0]), nil, nil)
		case "preview", "join":
			if len(rest) != 1 {
				return usageErr(p2pUsage)
			}
			if sub == "preview" {
				var pv p2p.InvitePreview
				if err := c.Post(cx, "/api/p2p/net/preview", map[string]string{"link": rest[0]}, &pv); err != nil {
					return err
				}
				e.printJSON(pv)
				return nil
			}
			var p p2p.Paired
			if err := c.Post(cx, "/api/p2p/net/join", map[string]string{"link": rest[0]}, &p); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "paired with %s (%s); safety code %s\n", p.Name, p.PeerID, p.SafetyCode)
		case "peers":
			var list []struct {
				PeerID       string `json:"peer_id"`
				Reachability string `json:"reachability"`
			}
			if err := c.Get(cx, "/api/p2p/net/peers", &list); err != nil {
				return err
			}
			for _, p := range list {
				fmt.Fprintf(e.out, "%s  %s\n", p.PeerID, p.Reachability)
			}
		case "send":
			if len(rest) < 1 {
				return usageErr(p2pUsage)
			}
			body, _ := json.Marshal(map[string]string{"text": strings.Join(rest[1:], " ")})
			var out map[string]any
			if err := c.Post(cx, "/api/p2p/net/send", api.SendRequest{PeerID: rest[0], Type: "ping", Body: body}, &out); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "delivered (%v, %v)\n", out["id"], out["reachability"])
		case "name":
			var st struct {
				Name string `json:"name"`
			}
			if len(rest) == 0 {
				if err := c.Get(cx, "/api/p2p", &st); err != nil {
					return err
				}
			} else if err := c.Do(cx, "PUT", "/api/p2p/name", map[string]string{"name": strings.Join(rest, " ")}, &st); err != nil {
				return err
			}
			fmt.Fprintln(e.out, st.Name)
		case "log":
			var entries []p2p.LogEntry
			if err := c.Get(cx, "/api/p2p/net/log", &entries); err != nil {
				return err
			}
			e.printJSON(entries)
		default:
			return usageErr(p2pUsage)
		}
		return nil
	})
}

func usesText(n int) string {
	switch n {
	case 0:
		return "any number of uses"
	case 1:
		return "one use"
	}
	return fmt.Sprintf("%d uses", n)
}
