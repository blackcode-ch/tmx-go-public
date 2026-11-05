package api

import (
	"errors"
	"net/http"
	"os"

	"github.com/blackcode-ch/tmx-go/internal/jobs"
)

func (s *Server) jobRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/jobs", s.listJobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)
	mux.HandleFunc("POST /api/jobs/{id}/release", s.releaseJob)
}
func (s *Server) hasJobs(w http.ResponseWriter) bool {
	if s.jobs == nil {
		writeError(w, http.StatusNotImplemented, "job workers unavailable")
		return false
	}
	return true
}
func jobError(w http.ResponseWriter, err error) {
	code := http.StatusConflict
	switch {
	case errors.Is(err, jobs.ErrForbidden), errors.Is(err, os.ErrNotExist):
		code = http.StatusNotFound
	case errors.Is(err, jobs.ErrBusy):
		code = http.StatusServiceUnavailable
	}
	writeError(w, code, err.Error())
}
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	if !s.hasJobs(w) {
		return
	}
	c, _ := caller(r)
	writeJSON(w, http.StatusOK, s.jobs.List(c.ID(), c.Owner))
}
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	if !s.hasJobs(w) {
		return
	}
	c, _ := caller(r)
	v, err := s.jobs.Get(r.PathValue("id"), c.ID(), c.Owner)
	if err != nil {
		jobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	if !s.hasJobs(w) {
		return
	}
	c, _ := caller(r)
	v, err := s.jobs.Cancel(r.PathValue("id"), c.ID(), c.Owner)
	if err != nil {
		jobError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}
func (s *Server) releaseJob(w http.ResponseWriter, r *http.Request) {
	if !s.hasJobs(w) {
		return
	}
	c, _ := caller(r)
	v, err := s.jobs.RequestRelease(r.PathValue("id"), c.ID(), c.Owner)
	if err != nil {
		jobError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}
