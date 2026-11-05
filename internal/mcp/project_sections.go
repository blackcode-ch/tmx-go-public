package mcp

import (
	"context"
	"encoding/json"
	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type projectSectionArgs struct {
	FinalOutput  *[]model.ProjectOutput `json:"final_output,omitempty" jsonschema:"shared final outputs; only the project lead can set these"`
	Project      string                 `json:"project" jsonschema:"project name or id"`
	Status       *string                `json:"status,omitempty" jsonschema:"at most three short lines in plain words"`
	Needs        *[]string              `json:"needs,omitempty" jsonschema:"replace the checklist; preserves ticks for unchanged text; never creates cards"`
	Links        *[]string              `json:"links,omitempty" jsonschema:"Title | URL or a bare path; MERGED into the existing links (same URL = one entry, your title wins) unless replace_links"`
	ReplaceLinks bool                   `json:"replace_links,omitempty" jsonschema:"replace the whole links list with links (an empty list clears it)"`
	Lead         *string                `json:"lead,omitempty" jsonschema:"local feedback recipient; empty restores the project lead"`
}

// LegacyToolNames maps a tool to earlier names it still answers to. Set by the private build
// (mcp_private.go); a public snapshot has none.
var LegacyToolNames = map[string][]string{}

func (t *Tools) registerProjectSections(s *sdk.Server) {
	sdk.AddTool(s, &sdk.Tool{Name: "project_import", Description: "Import a project page and question cards from a migration export. Source/id make retries idempotent; preserves original timestamps and authors. Historical answers are archived, never executed or sent."}, t.projectImport)
	sdk.AddTool(s, &sdk.Tool{Name: "my_part", Description: "Update your own contribution to a local project. Identity comes from authentication; omitted fields stay and empty values clear."}, t.projectContributionSet)
	sdk.AddTool(s, &sdk.Tool{Name: "project_artifacts", Description: "Read local project final outputs and agent contributions. Links and paths are reported metadata, not verified files."}, t.projectArtifactsGet)
	const sectionSetDesc = "Update the project's daily section: status (at most 3 lines), needs (checklist only, never Your-turn cards), links (merged with the existing ones unless replace_links) and local feedback lead. Omitted fields stay; empty values clear, except links, which clear only with replace_links."
	sdk.AddTool(s, &sdk.Tool{Name: "announce_set", Description: sectionSetDesc}, t.projectSectionSet)
	for _, old := range LegacyToolNames["announce_set"] { // earlier names agents still call (private build)
		sdk.AddTool(s, &sdk.Tool{Name: old, Description: "Earlier name of announce_set. " + sectionSetDesc}, t.projectSectionSet)
	}
}
func (t *Tools) projectSectionSet(ctx context.Context, _ *sdk.CallToolRequest, in projectSectionArgs) (*sdk.CallToolResult, any, error) {
	var sec model.ProjectSection
	patch := fleet.ProjectSectionPatch{Status: in.Status, Needs: in.Needs, Links: in.Links, ReplaceLinks: in.ReplaceLinks, Lead: in.Lead, FinalOutput: in.FinalOutput}
	if err := t.c.Post(ctx, "/api/projects/"+client.Seg(in.Project)+"/section", patch, &sec); err != nil {
		return nil, nil, err
	}
	data, err := json.Marshal(sec)
	if err != nil {
		return nil, nil, err
	}
	return untrusted(string(data)), nil, nil
}

type projectContributionArgs struct {
	Project     string                 `json:"project" jsonschema:"local project name or id"`
	Status      *string                `json:"status,omitempty"`
	FinalOutput *[]model.ProjectOutput `json:"final_output,omitempty"`
}

func (t *Tools) projectContributionSet(ctx context.Context, _ *sdk.CallToolRequest, in projectContributionArgs) (*sdk.CallToolResult, any, error) {
	var out model.ProjectContribution
	if err := t.c.Post(ctx, "/api/projects/"+client.Seg(in.Project)+"/contribution", fleet.ProjectContributionPatch{Status: in.Status, FinalOutput: in.FinalOutput}, &out); err != nil {
		return nil, nil, err
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return untrusted(string(data)), nil, nil
}

type projectArtifactsArgs struct {
	Project string `json:"project" jsonschema:"local project name or id"`
}

func (t *Tools) projectArtifactsGet(ctx context.Context, _ *sdk.CallToolRequest, in projectArtifactsArgs) (*sdk.CallToolResult, any, error) {
	var out model.ProjectArtifacts
	if err := t.c.Get(ctx, "/api/projects/"+client.Seg(in.Project)+"/artifacts", &out); err != nil {
		return nil, nil, err
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return untrusted(string(data)), nil, nil
}

type projectImportArgs struct {
	Project string                     `json:"project" jsonschema:"existing local project name or id"`
	Import  fleet.ProjectImportRequest `json:"import" jsonschema:"normalized migration batch with stable source and id"`
}

func (t *Tools) projectImport(ctx context.Context, _ *sdk.CallToolRequest, in projectImportArgs) (*sdk.CallToolResult, any, error) {
	var out model.ProjectImportReceipt
	if err := t.c.Post(ctx, "/api/projects/"+client.Seg(in.Project)+"/import", in.Import, &out); err != nil {
		return nil, nil, err
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return untrusted(string(data)), nil, nil
}
