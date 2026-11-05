package api

import (
	"encoding/json"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"net/http"
)

func (s *Server) agentProjectRoutes(mux *http.ServeMux) {
	for _, base := range []string{"/api/agents/{id}/", "/api/sessions/{id}/"} {
		mux.HandleFunc("GET "+base+"project", s.getAgentProject)
		mux.HandleFunc("POST "+base+"project", s.setAgentProject)
		mux.HandleFunc("POST "+base+"update", s.ownerOnly(s.updateAgentCompat))
	}
}

func (s *Server) getAgentProject(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	v, err := s.f.AgentProject(c, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) setAgentProject(w http.ResponseWriter, r *http.Request) {
	var patch map[string]json.RawMessage
	if !decode(w, r, &patch) {
		return
	}
	if patch == nil {
		writeError(w, 400, "project update must be an object")
		return
	}
	c, _ := caller(r)
	v, err := s.f.UpdateAgentProject(c, r.PathValue("id"), patch)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) updateAgentCompat(w http.ResponseWriter, r *http.Request) {
	var patch map[string]json.RawMessage
	if !decode(w, r, &patch) {
		return
	}
	if patch == nil {
		writeError(w, 400, "agent update must be an object")
		return
	}
	if err := fleet.ValidateAgentUpdateFields(patch); err != nil {
		writeErr(w, err)
		return
	}
	data, _ := json.Marshal(patch)
	var req fleet.AgentPatch
	if err := json.Unmarshal(data, &req); err != nil {
		writeError(w, 400, "invalid agent update")
		return
	}
	c, _ := caller(r)
	v, err := s.f.UpdateAgent(c, r.PathValue("id"), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
