package api

import (
	"fmt"
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

// harnesses: GET /api/harnesses, what each harness is and whether it is installed here.
func (s *Server) harnesses(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.f.Harnesses())
}

// discover: GET /api/discover, agents running on this machine outside tmx.
func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	list, err := s.f.Discover(c)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": list})
}

// adopt: POST /api/adopt, stop an outside agent and resume its conversation under tmx.
func (s *Server) adopt(w http.ResponseWriter, r *http.Request) {
	var req fleet.AdoptRequest
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	if req.Confirm { // stopping a process tmx did not start: checked first, then the owner gate
		plan, err := s.f.PlanAdopt(c, req)
		if err != nil {
			writeErr(w, err)
			return
		}
		reason := fmt.Sprintf("stop %s (pid %d in %s) and adopt it as %s", plan.Process.Harness, req.PID, short(plan.Process.Cwd, 60), plan.Name)
		if !s.RequireOwner(w, r, reason, req) {
			return
		}
	}
	a, err := s.f.Adopt(c, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}
