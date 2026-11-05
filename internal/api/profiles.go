package api

import (
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// profileRoutes: agent profiles (DESIGN.md "Profiles") and editing an agent after launch.
func (s *Server) profileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/profiles", s.listProfiles)
	mux.HandleFunc("POST /api/profiles", s.ownerOnly(s.createProfile))
	mux.HandleFunc("GET /api/profiles/{slug}", s.getProfile)
	mux.HandleFunc("PUT /api/profiles/{slug}", s.ownerOnly(s.putProfile))
	mux.HandleFunc("DELETE /api/profiles/{slug}", s.ownerOnly(s.deleteProfile))
	mux.HandleFunc("POST /api/profiles/{slug}/reset", s.ownerOnly(s.resetProfile))
	mux.HandleFunc("PATCH /api/agents/{id}", s.ownerOnly(s.patchAgent))
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.f.Profiles())
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.f.GetProfile(r.PathValue("slug"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	var p model.Profile
	if !decode(w, r, &p) || !s.gateProfile(w, r, "", p) {
		return
	}
	c, _ := caller(r)
	out, err := s.f.CreateProfile(c, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) putProfile(w http.ResponseWriter, r *http.Request) {
	var p model.Profile
	if !decode(w, r, &p) || !s.gateProfile(w, r, r.PathValue("slug"), p) {
		return
	}
	c, _ := caller(r)
	out, err := s.f.UpdateProfile(c, r.PathValue("slug"), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	if err := s.f.DeleteProfile(c, r.PathValue("slug")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) resetProfile(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	out, err := s.f.ResetProfile(c, r.PathValue("slug"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) patchAgent(w http.ResponseWriter, r *http.Request) {
	var p fleet.AgentPatch
	if !decode(w, r, &p) {
		return
	}
	c, _ := caller(r)
	out, err := s.f.UpdateAgent(c, r.PathValue("id"), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
