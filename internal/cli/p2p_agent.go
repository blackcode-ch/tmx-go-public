package cli

import (
	"encoding/json"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"os"
)

func cmdAgentP2P(e *env, sub string, args []string) error {
	fs := newFlags("p2p " + sub)
	approval := fs.String("approval", "", "resume this approval; omit other flags")
	label := fs.String("label", "", "person label or new group name")
	kind := fs.String("kind", "", "person or group (default person)")
	ttl := fs.String("ttl", "", "duration (default 24h)")
	uses := fs.Int("max-uses", 0, "maximum uses (default 1)")
	link := fs.String("link", "", "tmx://join link")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return usageErr(p2pUsage)
	}
	url, tok := os.Getenv(fleet.EnvURL), os.Getenv(fleet.EnvToken)
	if url == "" || tok == "" {
		return fmt.Errorf("pairing tools require a head or lead agent's environment")
	}
	path := "/api/p2p/agent/invite"
	var body any = api.AgentP2PInviteRequest{Label: *label, Kind: *kind, TTL: *ttl, MaxUses: *uses, ApprovalID: *approval}
	if sub == "agent-join" {
		if *label != "" || *kind != "" || *ttl != "" || *uses != 0 {
			return usageErr("agent-join accepts only --link or --approval")
		}
		path = "/api/p2p/agent/join"
		body = api.AgentP2PJoinRequest{Link: *link, ApprovalID: *approval}
	} else if *link != "" {
		return usageErr("invite-create does not accept --link")
	}
	cx, cancel := ctx()
	defer cancel()
	var out api.AgentP2PResult
	if err := client.New(url, tok).Post(cx, path, body, &out); err != nil {
		return err
	}
	if err := json.NewEncoder(e.out).Encode(out); err != nil {
		return err
	}
	if out.Error != "" {
		return fmt.Errorf("pairing failed: %s", out.Error)
	}
	if out.State == "denied" || out.State == "expired" {
		return fmt.Errorf("pairing approval %s", out.State)
	}
	return nil
}
