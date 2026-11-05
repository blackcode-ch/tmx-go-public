package api

import (
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"net/http"
)

func (s *Server) importProject(w http.ResponseWriter, r *http.Request) {
	var req fleet.ProjectImportRequest
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	out, err := s.f.ImportProject(c, r.PathValue("id"), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
