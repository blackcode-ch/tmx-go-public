package api

import (
	"encoding/base64"
	"errors"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"net/http"
	"strconv"

	"github.com/blackcode-ch/tmx-go/internal/chat"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

// chatRoutes: invites, contacts and chats with other tmx installs (DESIGN.md "Chats").
// Everything is owner-only except listing/reading/writing the chats an agent takes part in.
// While peer-to-peer is off every route answers 503.
func (s *Server) chatRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/p2p/files/send", s.p2pOn(s.p2pSendFile))
	mux.HandleFunc("POST /api/p2p/files/list", s.p2pOn(s.p2pFiles))
	mux.HandleFunc("POST /api/p2p/files/fetch", s.p2pOn(s.p2pFetchFile))
	o := func(h http.HandlerFunc) http.HandlerFunc { return s.p2pOn(s.ownerOnly(h)) }
	mux.HandleFunc("POST /api/invites", o(s.createInvite))
	mux.HandleFunc("GET /api/invites", o(s.listInvites))
	mux.HandleFunc("DELETE /api/invites/{id}", o(s.revokeInvite))
	mux.HandleFunc("POST /api/invites/preview", o(s.previewInvite))
	mux.HandleFunc("POST /api/invites/join", o(s.joinInvite))

	mux.HandleFunc("GET /api/contacts", o(s.listContacts))
	mux.HandleFunc("PATCH /api/contacts/{peer}", o(s.patchContact))
	mux.HandleFunc("DELETE /api/contacts/{peer}", o(s.removeContact))

	mux.HandleFunc("GET /api/chats", s.p2pOn(s.listChats))
	mux.HandleFunc("POST /api/chats", o(s.createChat))
	mux.HandleFunc("GET /api/chats/unread", o(s.unreadChats))
	mux.HandleFunc("GET /api/chats/{id}", s.p2pOn(s.getChat))
	mux.HandleFunc("PATCH /api/chats/{id}", o(s.patchChat))
	mux.HandleFunc("DELETE /api/chats/{id}", o(s.deleteChat))
	mux.HandleFunc("GET /api/chats/{id}/members", o(s.chatMembers))
	mux.HandleFunc("DELETE /api/chats/{id}/members/{peer}", o(s.removeChatMember))
	mux.HandleFunc("POST /api/chats/{id}/leave", o(s.leaveChat))
	mux.HandleFunc("POST /api/chats/{id}/read", o(s.readChat))
	mux.HandleFunc("GET /api/chats/{id}/messages", s.p2pOn(s.chatMessages))
	mux.HandleFunc("POST /api/chats/{id}/messages", s.p2pOn(s.sendChat))
	mux.HandleFunc("POST /api/chats/{id}/files", o(s.uploadChatFile))
	mux.HandleFunc("GET /api/chats/{id}/files/{file}", o(s.getChatFile))
	mux.HandleFunc("GET /api/chats/{id}/mentionable", s.p2pOn(s.chatMentionable))
	mux.HandleFunc("POST /api/chats/{id}/admin", o(s.offerChatAdmin))
	mux.HandleFunc("DELETE /api/chats/{id}/admin", o(s.cancelChatAdmin))
}

// p2pOn answers 503 while peer-to-peer is off.
func (s *Server) p2pOn(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.chats.Enabled() {
			writeError(w, http.StatusServiceUnavailable, "peer-to-peer is off: chats with other tmx installs are not available")
			return
		}
		h(w, r)
	}
}

func writeChatErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chat.ErrOff):
		writeError(w, http.StatusServiceUnavailable, "peer-to-peer is off: chats with other tmx installs are not available")
	case errors.Is(err, p2p.ErrUnreachable):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeErr(w, err)
	}
}

func (s *Server) reply(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		writeChatErr(w, err)
		return
	}
	writeJSON(w, status, v)
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) {
	var req chat.InviteRequest
	if !decode(w, r, &req) {
		return
	}
	inv, err := s.chats.CreateInvite(req)
	s.reply(w, http.StatusCreated, inv, err)
}

func (s *Server) listInvites(w http.ResponseWriter, r *http.Request) {
	list, err := s.chats.Invites()
	s.reply(w, http.StatusOK, list, err)
}

func (s *Server) revokeInvite(w http.ResponseWriter, r *http.Request) {
	err := s.chats.RevokeInvite(r.PathValue("id"))
	s.reply(w, http.StatusOK, map[string]bool{"ok": true}, err)
}

type linkReq struct {
	Link string `json:"link"`
}

func (s *Server) previewInvite(w http.ResponseWriter, r *http.Request) {
	var req linkReq
	if !decode(w, r, &req) {
		return
	}
	pv, err := s.chats.Preview(req.Link)
	s.reply(w, http.StatusOK, pv, err)
}

func (s *Server) joinInvite(w http.ResponseWriter, r *http.Request) {
	var req linkReq
	if !decode(w, r, &req) {
		return
	}
	res, err := s.chats.Join(r.Context(), req.Link)
	s.reply(w, http.StatusOK, res, err)
}

func (s *Server) listContacts(w http.ResponseWriter, r *http.Request) {
	list, err := s.chats.Contacts()
	s.reply(w, http.StatusOK, list, err)
}

func (s *Server) patchContact(w http.ResponseWriter, r *http.Request) {
	var req chat.ContactPatch
	if !decode(w, r, &req) {
		return
	}
	c, err := s.chats.UpdateContact(r.PathValue("peer"), req)
	s.reply(w, http.StatusOK, c, err)
}

func (s *Server) removeContact(w http.ResponseWriter, r *http.Request) {
	err := s.chats.RemoveContact(r.PathValue("peer"))
	s.reply(w, http.StatusOK, map[string]bool{"ok": true}, err)
}

func (s *Server) listChats(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	list, err := s.chats.Chats(c)
	s.reply(w, http.StatusOK, list, err)
}

func (s *Server) createChat(w http.ResponseWriter, r *http.Request) {
	var req chat.CreateRequest
	if !decode(w, r, &req) {
		return
	}
	c, err := s.chats.Create(req)
	s.reply(w, http.StatusCreated, c, err)
}

func (s *Server) unreadChats(w http.ResponseWriter, r *http.Request) {
	u, err := s.chats.Unread()
	s.reply(w, http.StatusOK, u, err)
}

func (s *Server) getChat(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	ch, err := s.chats.ChatFor(c, r.PathValue("id"))
	s.reply(w, http.StatusOK, ch, err)
}

func (s *Server) patchChat(w http.ResponseWriter, r *http.Request) {
	var req chat.ChatPatch
	if !decode(w, r, &req) {
		return
	}
	c, err := s.chats.Update(r.PathValue("id"), req)
	s.reply(w, http.StatusOK, c, err)
}

func (s *Server) deleteChat(w http.ResponseWriter, r *http.Request) {
	err := s.chats.Delete(r.PathValue("id"))
	s.reply(w, http.StatusOK, map[string]bool{"ok": true}, err)
}

func (s *Server) chatMembers(w http.ResponseWriter, r *http.Request) {
	ms, err := s.chats.Members(r.PathValue("id"))
	s.reply(w, http.StatusOK, ms, err)
}

func (s *Server) removeChatMember(w http.ResponseWriter, r *http.Request) {
	c, err := s.chats.RemoveMember(r.PathValue("id"), r.PathValue("peer"))
	s.reply(w, http.StatusOK, c, err)
}

func (s *Server) leaveChat(w http.ResponseWriter, r *http.Request) {
	c, err := s.chats.Leave(r.PathValue("id"))
	s.reply(w, http.StatusOK, c, err)
}

func (s *Server) readChat(w http.ResponseWriter, r *http.Request) {
	c, err := s.chats.MarkRead(r.PathValue("id"))
	s.reply(w, http.StatusOK, c, err)
}

func (s *Server) chatMessages(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	p, err := s.chats.Messages(c, r.PathValue("id"), before, limit)
	s.reply(w, http.StatusOK, p, err)
}

func (s *Server) sendChat(w http.ResponseWriter, r *http.Request) {
	var req chat.SendRequest
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	m, err := s.chats.SendWith(c, r.PathValue("id"), req)
	s.reply(w, http.StatusCreated, m, err)
}

// POST /api/chats/{id}/files {name, type?, data}: upload a file (base64, ≤ 25 MB) to attach
// to the next message (send with files: [id]).
func (s *Server) uploadChatFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Data string `json:"data"`
	}
	if !decodeMax(w, r, &req, chat.MaxFileBytes/3*4+64<<10) {
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "data is not valid base64")
		return
	}
	f, err := s.chats.Upload(r.PathValue("id"), req.Name, req.Type, data)
	s.reply(w, http.StatusCreated, f, err)
}

// GET /api/chats/{id}/files/{file}[?data=1]: a file's details (and content as base64).
func (s *Server) getChatFile(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	if r.URL.Query().Get("data") != "1" {
		f, err := s.chats.File(c, r.PathValue("id"), r.PathValue("file"))
		s.reply(w, http.StatusOK, f, err)
		return
	}
	f, b, err := s.chats.ReadFile(c, r.PathValue("id"), r.PathValue("file"))
	if err != nil {
		writeChatErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		model.ChatFile
		Data string `json:"data"`
	}{f, base64.StdEncoding.EncodeToString(b)})
}

// GET /api/chats/{id}/mentionable: who a message in the chat can @mention.
func (s *Server) chatMentionable(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	list, err := s.chats.Mentionable(c, r.PathValue("id"))
	s.reply(w, http.StatusOK, map[string]any{"mentionable": list}, err)
}

// POST /api/chats/{id}/admin {peer_id, then_leave?}: offer a member the group's admin role;
// 202, the member's tmx takes it over by itself (chat.updated follows). It always needs the
// owner's confirmation on this machine (Touch ID, Windows Hello), also while the owner gate is
// off: only the new admin could give the role back. A daemon without a gate refuses (501).
func (s *Server) offerChatAdmin(w http.ResponseWriter, r *http.Request) {
	var req chat.AdminRequest
	if !decode(w, r, &req) {
		return
	}
	group, name, err := s.chats.AdminTarget(r.PathValue("id"), req.PeerID) // 403/404/409 before any prompt
	if err != nil {
		writeChatErr(w, err)
		return
	}
	reason := "make " + short(name, 40) + " the admin of " + short(group, 60)
	if !s.requireOwner(w, r, gate.Action{Reason: reason, Key: actionKey(r, req), Always: true}) {
		return
	}
	c, err := s.chats.OfferAdmin(r.PathValue("id"), req)
	s.reply(w, http.StatusAccepted, c, err)
}

// DELETE /api/chats/{id}/admin: withdraw the waiting offer.
func (s *Server) cancelChatAdmin(w http.ResponseWriter, r *http.Request) {
	c, err := s.chats.CancelAdminOffer(r.PathValue("id"))
	s.reply(w, http.StatusOK, c, err)
}
