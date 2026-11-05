package mcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func (t *Tools) registerProfiles(s *sdk.Server) {
	sdk.AddTool(s, &sdk.Tool{Name: "profiles", Description: "The agent profiles you may launch (agent_launch profile=...): level, model, limits and what each is for."}, t.profiles)
}

func (t *Tools) myProfile(ctx context.Context, a model.Agent) (model.Profile, error) {
	slug := a.Profile
	if slug == "" {
		slug = string(a.Level)
	}
	var p model.Profile
	err := t.c.Get(ctx, "/api/profiles/"+client.Seg(slug), &p)
	return p, err
}

func (t *Tools) profiles(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
	a, err := t.me(ctx)
	if err != nil {
		return nil, nil, err
	}
	mine, err := t.myProfile(ctx, a)
	if err != nil {
		return nil, nil, err
	}
	var all []model.Profile
	if err := t.c.Get(ctx, "/api/profiles", &all); err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Your profile: %s (%s).", mine.Name, mine.Slug)
	if mine.CanCreateProjects {
		b.WriteString(" You may create projects (project_create).")
	}
	b.WriteString("\n")
	n := 0
	for _, p := range all {
		if !slices.Contains(mine.CanLaunch, p.Slug) {
			continue
		}
		n++
		fmt.Fprintf(&b, "- %s: %s, a %s on %s", p.Slug, p.Name, p.Level, p.Model)
		if p.ReadOnly {
			b.WriteString(", read-only")
		}
		if p.Description != "" {
			fmt.Fprintf(&b, ". %s", p.Description)
		}
		b.WriteString("\n")
	}
	if n == 0 {
		b.WriteString("You cannot launch agents.")
	}
	return text(strings.TrimSpace(b.String())), nil, nil
}
