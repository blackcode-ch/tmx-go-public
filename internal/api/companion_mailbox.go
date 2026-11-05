package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func companionMailRoute(r *http.Request) bool {
	p := r.URL.Path
	if p == "/api/inbox" {
		return r.Method == http.MethodGet
	}
	if p == "/api/messages" {
		return r.Method == http.MethodPost
	}
	parts := strings.Split(strings.TrimPrefix(p, "/api/messages/"), "/")
	if !strings.HasPrefix(p, "/api/messages/") || parts[0] == "" {
		return false
	}
	return (len(parts) == 1 && r.Method == http.MethodGet) || (len(parts) == 2 && r.Method == http.MethodPost && (parts[1] == "read" || parts[1] == "reply"))
}

func (s *Server) companionMailboxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/inbox", s.companionOnly(s.companionInbox))
	mux.HandleFunc("GET /api/messages/{id}", s.companionOnly(s.companionGetMail))
	mux.HandleFunc("POST /api/messages/{id}/read", s.companionOnly(s.companionReadMail))
	mux.HandleFunc("POST /api/messages/{id}/reply", s.companionOnly(s.companionReplyMail))
}

type companionMailRequest struct {
	ID     string `json:"id"`
	To     string `json:"to"`
	From   string `json:"from"`
	Body   string `json:"body"`
	Urgent bool   `json:"urgent"`
}

func (s *Server) companionPostMail(w http.ResponseWriter, r *http.Request) {
	s.companionSendMail(w, r, "")
}
func (s *Server) companionReplyMail(w http.ResponseWriter, r *http.Request) {
	s.companionSendMail(w, r, r.PathValue("id"))
}
func (s *Server) companionSendMail(w http.ResponseWriter, r *http.Request, reply string) {
	var req companionMailRequest
	if !decode(w, r, &req) {
		return
	}
	if req.From != "" && req.From != "companion" {
		writeError(w, http.StatusForbidden, "sender must match authenticated Companion")
		return
	}
	c, _ := caller(r)
	m, err := s.f.CompanionMailboxSend(c, req.ID, req.To, req.Body, reply, req.Urgent)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Receipt fields come solely from the stored message, including on replay.
	writeJSON(w, http.StatusOK, map[string]any{"id": m.ID, "state": m.State, "reply_to": m.ReplyTo})
}
func (s *Server) companionMailView(m model.Message) map[string]any {
	host := strings.ToLower(s.f.Machine())
	if s.peers != nil {
		host = s.peers.Machine()
	}
	name := func(id string) string {
		if a, err := s.f.Agent(id); err == nil {
			return a.Name
		}
		return id
	}
	return map[string]any{"id": m.ID, "sender": name(m.From), "sender_host": host, "recipient": name(m.To), "recipient_host": host, "from": name(m.From), "to": name(m.To), "body": m.Body, "reply_to": m.ReplyTo, "state": m.State, "sent_iso": time.UnixMilli(m.CreatedAt).UTC().Format(time.RFC3339Nano)}
}
func (s *Server) companionGetMail(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	m, err := s.f.CompanionMailboxMessage(c, r.PathValue("id"), false)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.companionMailView(m))
}
func (s *Server) companionReadMail(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	_, err := s.f.CompanionMailboxMessage(c, r.PathValue("id"), true)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Server) companionInbox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("agent") != "companion" || (q.Get("box") != "" && q.Get("box") != "in") {
		writeError(w, http.StatusForbidden, "only Companion's inbox is available")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		writeError(w, http.StatusBadRequest, "negative offset")
		return
	}
	c, _ := caller(r)
	msgs, total, err := s.f.CompanionMailbox(c, q.Get("unread") == "1", limit, offset)
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, 0)
	for _, m := range msgs {
		row := s.companionMailView(m)
		delete(row, "body")
		row["chars"] = utf8.RuneCountInString(m.Body)
		preview := []rune(m.Body)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		row["preview"] = string(preview)
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": "companion", "total": total, "messages": rows})
}
