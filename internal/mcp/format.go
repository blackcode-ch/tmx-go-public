package mcp

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

type snapIndex struct {
	agents   map[string]model.Agent
	projects map[string]model.Project
}

func indexSnapshot(s model.Snapshot) snapIndex {
	ix := snapIndex{agents: map[string]model.Agent{}, projects: map[string]model.Project{}}
	for _, a := range s.Agents {
		ix.agents[a.ID] = a
	}
	for _, p := range s.Projects {
		ix.projects[p.ID] = p
	}
	return ix
}

// name returns the agent's name for an ID ("owner" stays), else fallback.
func (ix snapIndex) name(id, fallback string) string {
	if id == model.Owner {
		return model.Owner
	}
	if a, ok := ix.agents[id]; ok {
		return a.Name
	}
	if name, host, ok := fleet.SplitPeer(id); ok { // an agent on another machine of the fleet
		return name + "@" + host
	}
	return fallback
}

// FormatTree renders head → projects → agents with state and model (also used by `tmx ls`).
// It never shows money: plan usage is under "AI accounts" (DESIGN.md "AI accounts and usage").
func FormatTree(s model.Snapshot) string {
	var b strings.Builder
	line := func(indent string, a model.Agent) {
		level := string(a.Level)
		if a.Profile != "" && a.Profile != level {
			level += "/" + a.Profile
		}
		fmt.Fprintf(&b, "%s%s  %s  %s  %s", indent, a.Name, level, a.State, orDash(a.Model))
		if a.Harness != "" && a.Harness != model.HarnessClaude {
			fmt.Fprintf(&b, "  (%s)", a.Harness)
		}
		if a.Queued > 0 {
			fmt.Fprintf(&b, "  %d queued", a.Queued)
		}
		if a.State == model.StateQueued && a.QueuePos > 0 {
			fmt.Fprintf(&b, "  #%d in the launch queue (%s)", a.QueuePos, a.QueueReason)
		}
		if a.State == model.StateStarting && a.QueueReason != "" {
			fmt.Fprintf(&b, "  (%s)", a.QueueReason)
		}
		if a.State == model.StatePaused {
			fmt.Fprintf(&b, "  paused by tmx: %s", a.PausedReason)
		}
		if a.Role != "" {
			fmt.Fprintf(&b, "  (%s)", a.Role)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "machine %s, workspace %s\n", s.Machine, s.Workspace)
	children := map[string][]model.Agent{}
	for _, a := range s.Agents {
		children[a.ParentID] = append(children[a.ParentID], a)
	}
	var walk func(indent, id string, project string)
	walk = func(indent, id, project string) {
		for _, c := range children[id] {
			if c.ProjectID != project {
				continue
			}
			line(indent, c)
			walk(indent+"  ", c.ID, project)
		}
	}
	shown := map[string]bool{}
	if s.Head != nil {
		line("", *s.Head)
		shown[s.Head.ID] = true
	}
	for _, p := range s.Projects {
		fmt.Fprintf(&b, "  project %s (%s)  %s  %s\n", p.Name, p.Slug, p.Status, p.Folder)
		for _, a := range s.Agents {
			if a.ProjectID != p.ID {
				continue
			}
			// Roots of this project: parent outside the project.
			if par, ok := findAgent(s.Agents, a.ParentID); ok && par.ProjectID == p.ID {
				continue
			}
			line("    ", a)
			shown[a.ID] = true
			walk("      ", a.ID, p.ID)
		}
	}
	header := false
	for _, a := range s.Agents {
		if a.ProjectID == "" && !shown[a.ID] {
			if !header {
				b.WriteString("  no project\n")
				header = true
			}
			line("    ", a)
		}
	}
	if len(s.Approvals) > 0 {
		fmt.Fprintf(&b, "%d approvals waiting for the owner\n", len(s.Approvals))
	}
	return strings.TrimRight(b.String(), "\n")
}

func findAgent(list []model.Agent, id string) (model.Agent, bool) {
	for _, a := range list {
		if a.ID == id {
			return a, true
		}
	}
	return model.Agent{}, false
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
