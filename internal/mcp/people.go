package mcp

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Python tmx's People tools, under their Python names (parity). Minimal: an ask or draft is
// stored as a promise and shows on the Promises page. Nothing is ever sent from here; sending
// to a person still needs the owner's OK.

type personProposeArgs struct {
	Person  string `json:"person" jsonschema:"the person's key (see the People page)"`
	Title   string `json:"title" jsonschema:"one line: what you ask or promise"`
	Draft   string `json:"draft,omitempty" jsonschema:"the message you propose to send; stored for the owner, not sent"`
	Channel string `json:"channel,omitempty" jsonschema:"whatsapp, email or github (a hint for the owner)"`
	Due     string `json:"due,omitempty" jsonschema:"ISO date to follow up (you get a reminder)"`
}

type personOutboxArgs struct {
	Status string `json:"status,omitempty" jsonschema:"open (default), done, dropped or all"`
}

type promiseIDArgs struct {
	ID string `json:"id" jsonschema:"the ask/draft id from tmx_person_outbox"`
}

func (t *Tools) registerPeople(s *sdk.Server) {
	sdk.AddTool(s, &sdk.Tool{Name: "tmx_person_propose", Description: "Propose an ask or a message to a person without their own tmx. " +
		"Stored as an open promise on the Promises page for the owner; NOT sent â€” sending needs the owner's OK."}, t.personPropose)
	sdk.AddTool(s, &sdk.Tool{Name: "tmx_person_outbox", Description: "Your asks and drafts to people (the promises you opened or are assigned)."}, t.personOutbox)
	sdk.AddTool(s, &sdk.Tool{Name: "tmx_person_ask_close", Description: "Close one of your asks as done."}, t.personAskClose)
	sdk.AddTool(s, &sdk.Tool{Name: "tmx_person_draft_withdraw", Description: "Withdraw one of your drafts (marks it dropped)."}, t.personDraftWithdraw)
}

func (t *Tools) personPropose(ctx context.Context, _ *sdk.CallToolRequest, in personProposeArgs) (*sdk.CallToolResult, any, error) {
	body := map[string]any{"person_key": in.Person, "title": in.Title}
	var txt []string
	if in.Channel != "" {
		txt = append(txt, "Channel: "+in.Channel)
	}
	if in.Draft != "" {
		txt = append(txt, "Draft (not sent; needs the owner's OK):\n"+in.Draft)
	}
	if len(txt) > 0 {
		body["text"] = strings.Join(txt, "\n")
	}
	if in.Due != "" {
		body["due"] = in.Due
	}
	var p model.Promise
	if err := t.c.Post(ctx, "/api/promises", body, &p); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("Stored as %s (open) on the Promises page; nothing was sent.", p.ID)), nil, nil
}

func (t *Tools) personOutbox(ctx context.Context, _ *sdk.CallToolRequest, in personOutboxArgs) (*sdk.CallToolResult, any, error) {
	st := in.Status
	switch st {
	case "":
		st = "open"
	case "all":
		st = ""
	}
	var list []model.Promise
	if err := t.c.Get(ctx, "/api/promises?status="+url.QueryEscape(st), &list); err != nil {
		return nil, nil, err
	}
	if len(list) == 0 {
		return text("Nothing."), nil, nil
	}
	var b strings.Builder
	for _, p := range list {
		fmt.Fprintf(&b, "%s  %s  %s: %s", p.ID, p.Status, p.PersonKey, p.Title)
		if p.Due != "" {
			fmt.Fprintf(&b, " (due %s)", p.Due)
		}
		b.WriteByte('\n')
	}
	return text(b.String()), nil, nil
}

func (t *Tools) setPromiseStatus(ctx context.Context, id, status string) (*sdk.CallToolResult, any, error) {
	var p model.Promise
	if err := t.c.Post(ctx, "/api/promises/"+client.Seg(id), map[string]any{"status": status}, &p); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("%s is %s.", p.ID, p.Status)), nil, nil
}

func (t *Tools) personAskClose(ctx context.Context, _ *sdk.CallToolRequest, in promiseIDArgs) (*sdk.CallToolResult, any, error) {
	return t.setPromiseStatus(ctx, in.ID, "done")
}

func (t *Tools) personDraftWithdraw(ctx context.Context, _ *sdk.CallToolRequest, in promiseIDArgs) (*sdk.CallToolResult, any, error) {
	return t.setPromiseStatus(ctx, in.ID, "dropped")
}
