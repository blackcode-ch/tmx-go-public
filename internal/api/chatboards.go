package api

import (
	"net/http"
	"strconv"

	"github.com/blackcode-ch/tmx-go/internal/chat"
)

// chatBoardRoutes: each chat's board (DESIGN.md "Chat boards"): topics, messages moved
// between them, their logs (all shared with the chat's members) and this owner's order of
// the strings. Reading, opening and changing topics and moving messages between them are for
// the owner and the chat's participating agent (the chat service checks which chat an agent
// takes part in); log lines and the order are the owner's.
func (s *Server) chatBoardRoutes(mux *http.ServeMux) {
	o := func(h http.HandlerFunc) http.HandlerFunc { return s.p2pOn(s.ownerOnly(h)) }
	mux.HandleFunc("GET /api/chats/{id}/board", s.p2pOn(s.chatBoard))
	mux.HandleFunc("POST /api/chats/{id}/board/log", o(s.chatBoardLog))
	mux.HandleFunc("PUT /api/chats/{id}/board/order", o(s.chatBoardOrder))
	mux.HandleFunc("POST /api/chats/{id}/board/move", s.p2pOn(s.chatBoardMove))
	mux.HandleFunc("POST /api/chats/{id}/topics", s.p2pOn(s.createChatTopic))
	mux.HandleFunc("PATCH /api/chats/{id}/topics/{topic}", s.p2pOn(s.patchChatTopic))
}

// GET /api/chats/{id}/board?per_topic=N: N latest messages of each topic (default 40, 0 = none).
func (s *Server) chatBoard(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	per := chat.BoardPerTopic
	if v := r.URL.Query().Get("per_topic"); v != "" {
		per, _ = strconv.Atoi(v)
	}
	b, err := s.chats.Board(c, r.PathValue("id"), per)
	s.reply(w, http.StatusOK, b, err)
}

func (s *Server) chatBoardLog(w http.ResponseWriter, r *http.Request) {
	var req chat.TopicEntryRequest
	if !decode(w, r, &req) {
		return
	}
	e, added, err := s.chats.AddTopicEntry(r.PathValue("id"), req)
	status := http.StatusOK
	if added {
		status = http.StatusCreated
	}
	s.reply(w, status, e, err)
}

func (s *Server) chatBoardOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	err := s.chats.SetBoardOrder(r.PathValue("id"), req.IDs)
	s.reply(w, http.StatusOK, map[string]bool{"ok": true}, err)
}

func (s *Server) chatBoardMove(w http.ResponseWriter, r *http.Request) {
	var req chat.MoveRequest
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	err := s.chats.MoveMessagesAs(c, r.PathValue("id"), req)
	s.reply(w, http.StatusOK, map[string]bool{"ok": true}, err)
}

func (s *Server) createChatTopic(w http.ResponseWriter, r *http.Request) {
	var req chat.TopicRequest
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	t, err := s.chats.CreateTopicAs(c, r.PathValue("id"), req)
	s.reply(w, http.StatusCreated, t, err)
}

func (s *Server) patchChatTopic(w http.ResponseWriter, r *http.Request) {
	var req chat.TopicPatch
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	t, err := s.chats.UpdateTopicAs(c, r.PathValue("id"), r.PathValue("topic"), req)
	s.reply(w, http.StatusOK, t, err)
}
