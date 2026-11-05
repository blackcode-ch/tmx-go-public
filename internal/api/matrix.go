package api

import (
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

// The Matrix (DESIGN.md "Matrix"): a thread with everything on it, and links. The same calls
// serve the owner's view and the agents' matrix tool; the fleet scopes them per caller.
func (s *Server) matrixRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/matrix/threads/{id}", gzipped(s.matrixView)) // ?past=1: ended links too; ?all=1: chatter too
	mux.HandleFunc("GET /api/matrix/links", gzipped(s.matrixLinksTo))     // ?axis=&ref=: every thread one thing is on
	mux.HandleFunc("POST /api/matrix/links", s.matrixAttach)
	mux.HandleFunc("DELETE /api/matrix/links/{id}", s.matrixDetach)
	mux.HandleFunc("POST /api/matrix/threads", s.matrixNew)              // owner, head, a lead under its project
	mux.HandleFunc("POST /api/matrix/threads/{id}/state", s.matrixState) // owner, head, the thread's owner or project lead
	mux.HandleFunc("GET /api/matrix/mine", gzipped(s.matrixMine))
	mux.HandleFunc("GET /api/matrix/pivot", gzipped(s.matrixPivot)) // ?axis=
}

func (s *Server) matrixNew(w http.ResponseWriter, r *http.Request) {
	var req fleet.NewThreadRequest
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	t, err := s.f.NewThread(c, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) matrixState(w http.ResponseWriter, r *http.Request) {
	var req fleet.StateRequest
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	t, err := s.f.SetState(c, r.PathValue("id"), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) matrixMine(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	cards, err := s.f.Mine(c)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cards)
}

func (s *Server) matrixPivot(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	rows, err := s.f.Pivot(c, r.URL.Query().Get("axis"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) matrixView(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	q := r.URL.Query()
	v, err := s.f.View(c, r.PathValue("id"), q.Get("past") == "1", q.Get("all") == "1")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) matrixLinksTo(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	q := r.URL.Query()
	links, err := s.f.LinksTo(c, q.Get("axis"), q.Get("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, links)
}

// POST /api/matrix/links {thread, axis, ref, role?, label?, why?}
func (s *Server) matrixAttach(w http.ResponseWriter, r *http.Request) {
	var req fleet.LinkRequest
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	l, err := s.f.AttachLink(c, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) matrixDetach(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	l, err := s.f.DetachLink(c, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}
