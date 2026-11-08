package api

import (
	"github.com/blackcode-ch/tmx-go/internal/model"
	"net/http"
)

// These compact views preserve Python Companion's read-only tool contracts.
// They deliberately omit approval context, command inputs and project briefs.
func (s *Server) linkProjects(w http.ResponseWriter, r *http.Request) {
	snap, err := s.f.Snapshot()
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := []map[string]any{}
	for _, p := range snap.Projects {
		agents, live, working := 0, 0, 0
		for _, a := range snap.Agents {
			if a.ProjectID != p.ID {
				continue
			}
			agents++
			if a.State != model.StateStopped && a.State != model.StateError {
				live++
			}
			if a.State == model.StateWorking {
				working++
			}
		}
		rows = append(rows, map[string]any{"name": p.Name, "agents": agents, "live": live, "working": working})
	}
	writeJSON(w, http.StatusOK, map[string]any{"notice": LinkNotice, "projects": rows})
}

func (s *Server) linkHealth(w http.ResponseWriter, r *http.Request) {
	rows, err := s.f.CompanionMailHealth()
	if err != nil {
		writeErr(w, err)
		return
	}
	approvals, err := s.f.Approvals(model.ApprovalPending)
	if err != nil {
		writeErr(w, err)
		return
	}
	open, later := 0, 0
	for _, a := range approvals {
		if a.Deferred {
			later++
		} else {
			open++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"notice": LinkNotice, "agents": rows,
		"your_turn": map[string]any{"open": open, "later": later, "stale": nil}})
}

func (s *Server) linkTurns(w http.ResponseWriter, r *http.Request) {
	snap, err := s.f.Snapshot()
	if err != nil {
		writeErr(w, err)
		return
	}
	projects := map[string]string{}
	for _, p := range snap.Projects {
		projects[p.ID] = p.Name
	}
	agents := map[string]string{}
	for _, a := range snap.Agents {
		agents[a.ID] = projects[a.ProjectID]
	}
	rows := []map[string]any{}
	for _, a := range snap.Approvals {
		if a.Deferred {
			continue
		}
		rows = append(rows, map[string]any{"project": agents[a.AgentID], "title": short(a.Question, 120),
			"urgent": a.Card != nil && a.Card.Urgent, "since": float64(a.CreatedAt) / 1000})
	}
	writeJSON(w, http.StatusOK, map[string]any{"notice": LinkNotice, "your_turn": rows})
}
