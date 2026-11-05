package api

import (
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

// The board (DESIGN.md "Board"): the head's turns as cards on threads. The owner reads and
// arranges it; the head files its own turn (thread_set → POST /api/board/file).
func (s *Server) boardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/board", s.ownerOnly(s.getBoard))
	mux.HandleFunc("POST /api/board/move", s.ownerOnly(s.moveCard))
	mux.HandleFunc("POST /api/board/file", s.fileTurn)
	mux.HandleFunc("GET /api/board/log", s.getLog)    // owner, head, a project's agents (their project)
	mux.HandleFunc("POST /api/board/log", s.addEntry) // the same
	mux.HandleFunc("PUT /api/board/order", s.ownerOnly(s.putOrder))
	mux.HandleFunc("GET /api/threads", s.listThreads)
	mux.HandleFunc("POST /api/threads", s.ownerOnly(s.createThread))
	mux.HandleFunc("PATCH /api/threads/{id}", s.patchThread)   // owner, head
	mux.HandleFunc("DELETE /api/threads/{id}", s.deleteThread) // owner, head
}

func (s *Server) getBoard(w http.ResponseWriter, r *http.Request) {
	b, err := s.f.Board()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// POST /api/board/move {card, thread_id}: file the turn starting at seq card under a thread.
func (s *Server) moveCard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Card     int64  `json:"card"`
		ThreadID string `json:"thread_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.f.MoveCard(req.Card, req.ThreadID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /api/board/file {title}: the head files its current turn (thread_set).
func (s *Server) fileTurn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	t, topics, err := s.f.FileTurn(c, req.Title)
	if err != nil {
		writeErr(w, err)
		return
	}
	if topics == nil {
		topics = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": t, "topics": topics})
}

// POST /api/threads {title, color?}
func (s *Server) createThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
		Color string `json:"color"`
	}
	if !decode(w, r, &req) {
		return
	}
	t, err := s.f.CreateThread(req.Title, req.Color)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// GET /api/threads: the threads with their card counts (owner, head).
func (s *Server) listThreads(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	list, err := s.f.Threads(c)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// DELETE /api/threads/{id-or-title}: a topic's cards move to General (owner, head).
func (s *Server) deleteThread(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	t, err := s.f.DeleteThread(c, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// PATCH /api/threads/{id-or-title} {title?, color?, archived?} (owner, head)
func (s *Server) patchThread(w http.ResponseWriter, r *http.Request) {
	var req fleet.ThreadPatch
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	t, err := s.f.UpdateThread(c, r.PathValue("id"), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// GET /api/board/log?thread=<id or title>: a thread's log, oldest first.
func (s *Server) getLog(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	t, entries, err := s.f.Log(c, r.URL.Query().Get("thread"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": t, "entries": entries})
}

// POST /api/board/log {thread?, kind, text, link?, card?, corrects?}: 201 new, 200 when the
// same line was filed already today.
func (s *Server) addEntry(w http.ResponseWriter, r *http.Request) {
	var req fleet.AddEntryRequest
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	e, added, err := s.f.AddEntry(c, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	if added {
		status = http.StatusCreated
	}
	writeJSON(w, status, e)
}

// PUT /api/board/order {ids}: the owner's urgency order, most urgent first.
func (s *Server) putOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.f.SetThreadOrder(req.IDs); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
