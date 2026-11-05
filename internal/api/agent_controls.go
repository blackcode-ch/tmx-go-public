package api

import (
	"encoding/json"
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/gate"
)

func (s *Server) agentControlRoutes(mux *http.ServeMux) {
	for _, base := range []string{"/api/agents/{id}/", "/api/sessions/{id}/"} {
		mux.HandleFunc("POST "+base+"answer", s.ownerOnly(s.answerAgent))
		mux.HandleFunc("POST "+base+"shelve", s.shelveAgent)
		mux.HandleFunc("POST "+base+"unshelve", s.unshelveAgent)
		mux.HandleFunc("POST "+base+"rename", s.renameAgent)
	}
}

func (s *Server) renameAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	if c.Owner && !s.requireOwner(w, r, gate.Action{Reason: "rename agent " + r.PathValue("id") + " to " + req.Name, Key: actionKey(r, req)}) {
		return
	}
	a, err := s.f.RenameAgent(c, r.PathValue("id"), req.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) shelveAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	a, err := s.f.ShelveAgent(c, r.PathValue("id"), req.Note)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) unshelveAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Start bool `json:"start"`
	}
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	a, err := s.f.UnshelveAgent(c, r.PathValue("id"), req.Start)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) answerAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Choice json.RawMessage `json:"choice"`
	}
	if !decode(w, r, &req) {
		return
	}
	var choice string
	if json.Unmarshal(req.Choice, &choice) != nil {
		var n json.Number
		if json.Unmarshal(req.Choice, &n) != nil || n.String() == "" {
			writeError(w, 400, "choice must be a label or option number")
			return
		}
		choice = n.String()
	}
	c, _ := caller(r)
	ap, d, err := s.f.AgentAnswer(c, r.PathValue("id"), choice)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !s.gateDecide(w, r, ap.ID, d) || !s.gateCompanion(w, r, ap, d) {
		return
	}
	ap, err = s.f.Decide(c, ap.ID, d)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ap)
}
