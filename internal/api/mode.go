package api

import (
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

// The live mode switch (DESIGN.md "Switching live"): the owner moves a Claude or Codex agent
// between chat mode and its harness's own screen, on the same conversation. Owner only: the
// screen is for a person to watch and type in; agents have no use for it.
func (s *Server) modeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/agents/{id}/mode", s.ownerOnly(s.setMode))
}

// POST /api/agents/{id}/mode {mode: chat|terminal} → {agent, pending, note}.
func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	var req fleet.ModeRequest
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	out, err := s.f.SetMode(c, r.PathValue("id"), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
