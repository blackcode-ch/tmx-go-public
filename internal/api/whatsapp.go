package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/accounts"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/whatsapp"
)

// WhatsApp (DESIGN.md "WhatsApp"): the owner's linked account. Everything is owner-only,
// except that agents the owner allows (default: the head) may list chats, read messages and
// ask to send text, which waits for the owner's Allow unless the owner allowed that chat.
// Agents never get the bus events, the media or the settings, and WhatsApp content in one
// agent's timeline is hidden from the others.
func (s *Server) whatsappRoutes(mux *http.ServeMux) {
	o, on := s.ownerOnly, s.waOn
	mux.HandleFunc("GET /api/whatsapp", o(on(s.waStatus)))
	mux.HandleFunc("GET /api/whatsapp/access", on(s.waAccess)) // agents: may I?
	mux.HandleFunc("POST /api/whatsapp/link", o(on(s.waLink)))
	mux.HandleFunc("POST /api/whatsapp/link/cancel", o(on(s.waCancelLink)))
	mux.HandleFunc("POST /api/whatsapp/unlink", o(on(s.waUnlink)))
	mux.HandleFunc("PUT /api/whatsapp/settings", o(on(s.waSettings)))
	mux.HandleFunc("GET /api/whatsapp/chats", on(s.waChats))                        // owner, allowed agents
	mux.HandleFunc("GET /api/whatsapp/chats/{jid}/messages", on(s.waMessages))      // owner, allowed agents
	mux.HandleFunc("POST /api/whatsapp/chats/{jid}/messages", on(s.waSend))         // owner; allowed agents: text, approval rules
	mux.HandleFunc("POST /api/whatsapp/chats/{jid}/read", o(on(s.waRead)))          //
	mux.HandleFunc("GET /api/whatsapp/media/{jid}/{id}", on(s.waMedia))             // the owner, or a ticket
	mux.HandleFunc("POST /api/whatsapp/media/{jid}/{id}/ticket", o(on(s.waTicket))) //
}

// waOn answers 501 without the service and 503 while it is off.
func (s *Server) waOn(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.wa == nil {
			writeError(w, http.StatusNotImplemented, "WhatsApp is not available on this daemon")
			return
		}
		if !s.wa.Enabled() {
			writeWaErr(w, whatsapp.ErrOff)
			return
		}
		h(w, r)
	}
}

func writeWaErr(w http.ResponseWriter, err error) {
	var we *whatsapp.Error
	if errors.As(err, &we) {
		writeError(w, we.Status, we.Msg)
		return
	}
	writeErr(w, err)
}

// waTicketRequest reports whether r loads an attachment with a ticket instead of a token
// (auth.go lets it through; waMedia checks the ticket).
func waTicketRequest(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.HasPrefix(r.URL.Path, "/api/whatsapp/media/") &&
		r.Header.Get("Authorization") == "" && r.URL.Query().Get("ticket") != ""
}

// waAgent checks an agent caller may use WhatsApp (it has answered when not).
func (s *Server) waAgent(w http.ResponseWriter, r *http.Request) (fleet.Caller, bool) {
	c, _ := caller(r)
	if c.Owner {
		return c, true
	}
	if c.AgentID == "" {
		writeError(w, http.StatusForbidden, "only the owner and the agents the owner allows use WhatsApp")
		return c, false
	}
	if err := s.wa.CanRead(c.AgentID); err != nil {
		writeWaErr(w, err)
		return c, false
	}
	return c, true
}

func (s *Server) waStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.wa.Status())
}

// GET /api/whatsapp/access (agents): {allowed, state, reason?}. The MCP server offers the
// WhatsApp tools only to agents that may use them.
func (s *Server) waAccess(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	out := map[string]any{"allowed": true, "state": s.wa.Status().State}
	if !c.Owner {
		if err := s.wa.CanRead(c.AgentID); err != nil {
			out["allowed"], out["reason"] = false, err.Error()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) waLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
		Phone  string `json:"phone"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if err := s.wa.Link(req.Method, req.Phone); err != nil {
		writeWaErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.wa.Status())
}

func (s *Server) waCancelLink(w http.ResponseWriter, r *http.Request) {
	if err := s.wa.CancelLink(); err != nil {
		writeWaErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/whatsapp/unlink: logs the device out and forgets the account's data; behind the
// owner gate (once it is on).
func (s *Server) waUnlink(w http.ResponseWriter, r *http.Request) {
	reason := "unlink WhatsApp and forget its chats in tmx"
	if p := s.wa.Status().Phone; p != "" {
		reason = "unlink WhatsApp (" + p + ") and forget its chats in tmx"
	}
	if !s.RequireOwner(w, r, reason, nil) {
		return
	}
	if err := s.wa.Unlink(r.Context()); err != nil {
		writeWaErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) waSettings(w http.ResponseWriter, r *http.Request) {
	var p whatsapp.SettingsPatch
	if !decode(w, r, &p) {
		return
	}
	out, err := s.wa.SetSettings(p)
	if err != nil {
		writeWaErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) waChats(w http.ResponseWriter, r *http.Request) {
	c, ok := s.waAgent(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.wa.Chats(r.URL.Query().Get("q"), limit)
	if err != nil {
		writeWaErr(w, err)
		return
	}
	if !c.Owner {
		for i := range list {
			list[i].Avatar = nil // pictures stay with the owner
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) waMessages(w http.ResponseWriter, r *http.Request) {
	c, ok := s.waAgent(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	jid := r.PathValue("jid")
	if !c.Owner { // agents may name the chat
		ch, err := s.wa.FindChat(jid)
		if err != nil {
			writeWaErr(w, err)
			return
		}
		jid = ch.JID
	}
	list, err := s.wa.Messages(jid, q.Get("before"), limit)
	if err != nil {
		writeWaErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// maxWaBody: five files of 16 MB in base64, and the text.
const maxWaBody = whatsapp.MaxUploads*(whatsapp.MaxUploadBytes/3*4+4) + 1<<20

// POST /api/whatsapp/chats/{jid}/messages {text?, reply_to?, uploads?: [{name, type, data}]}:
// the owner → 201 WaMessage (pending; a message with several files is several messages, the
// rest come by event). An agent (text only) → 201 {message} when it went out at once, 202
// {approval} when it waits for the owner's Allow.
func (s *Server) waSend(w http.ResponseWriter, r *http.Request) {
	c, ok := s.waAgent(w, r)
	if !ok {
		return
	}
	var req struct {
		Text    string `json:"text"`
		ReplyTo string `json:"reply_to"`
		Uploads []struct {
			Name string `json:"name"`
			Type string `json:"type"`
			Data string `json:"data"`
		} `json:"uploads"`
	}
	limit := int64(maxWaBody)
	if !c.Owner {
		limit = MaxBody
	}
	if !decodeMax(w, r, &req, limit) {
		return
	}
	if !c.Owner {
		if len(req.Uploads) > 0 {
			writeError(w, http.StatusForbidden, "agents send text only")
			return
		}
		res, err := s.wa.AgentSend(c.AgentID, r.PathValue("jid"), req.Text, req.ReplyTo)
		if err != nil {
			writeWaErr(w, err)
			return
		}
		status := http.StatusCreated
		if res.Approval != nil {
			status = http.StatusAccepted
		}
		writeJSON(w, status, res)
		return
	}
	sr := whatsapp.SendRequest{Text: req.Text, ReplyTo: req.ReplyTo}
	for _, u := range req.Uploads {
		data, err := base64.StdEncoding.DecodeString(u.Data)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s: data is not valid base64", u.Name))
			return
		}
		sr.Uploads = append(sr.Uploads, whatsapp.Upload{Name: u.Name, Type: u.Type, Data: data})
	}
	msgs, err := s.wa.Send(model.Owner, r.PathValue("jid"), sr)
	if err != nil {
		writeWaErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, msgs[0])
}

func (s *Server) waRead(w http.ResponseWriter, r *http.Request) {
	if err := s.wa.MarkRead(r.Context(), r.PathValue("jid")); err != nil {
		writeWaErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/whatsapp/media/{jid}/{id}[?data=1][&ticket=T]: the attachment (downloaded from
// WhatsApp the first time), with Range; data=1 answers {type, size, name, data} (base64) for
// the desktop bridge. The owner's token or a ticket for this attachment; never an agent.
func (s *Server) waMedia(w http.ResponseWriter, r *http.Request) {
	jid, id, q := r.PathValue("jid"), r.PathValue("id"), r.URL.Query()
	if c, ok := caller(r); !ok || !c.Owner {
		if ok || !s.wa.CheckTicket(jid, id, q.Get("ticket")) {
			writeError(w, http.StatusUnauthorized, "this needs the owner's token or a valid ticket")
			return
		}
	}
	f, err := s.wa.Media(r.Context(), jid, id)
	if err != nil {
		writeWaErr(w, err)
		return
	}
	file, err := os.Open(f.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "the file is no longer stored here")
		return
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if q.Get("data") == "1" {
		if st.Size() > maxBridgeSegment {
			writeError(w, http.StatusRequestEntityTooLarge, "this file is too large to pass through the window's bridge")
			return
		}
		b, err := io.ReadAll(file)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": f.Mime, "size": len(b), "name": f.Name,
			"data": base64.StdEncoding.EncodeToString(b)})
		return
	}
	w.Header().Set("Content-Type", f.Mime)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	if !strings.HasPrefix(f.Mime, "image/") && !strings.HasPrefix(f.Mime, "audio/") && !strings.HasPrefix(f.Mime, "video/") {
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlPathEscape(f.Name))
	}
	http.ServeContent(w, r, f.Name, st.ModTime(), file)
}

func urlPathEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (s *Server) waTicket(w http.ResponseWriter, r *http.Request) {
	if devicePlayback(w, r) {
		return
	}
	t, exp, err := s.wa.Ticket(r.PathValue("jid"), r.PathValue("id"))
	if err != nil {
		writeWaErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ticket": t, "expires_at": exp.UnixMilli()})
}

// ---- the privacy filter for agents

// waHidden replaces WhatsApp content another agent sees in a timeline.
const waHidden = "[WhatsApp content: only the owner and the agent that read it see it]"

// agentEvent is what an agent caller may see of a bus event: no WhatsApp events (they carry
// other people's words), no other agent's WhatsApp or process-launch approvals, other agents'
// timelines without WhatsApp content, and only its own Companion tasks and messages.
func agentEvent(c fleet.Caller, ev model.BusEvent) (model.BusEvent, bool) {
	if c.Owner {
		return ev, true
	}
	if strings.HasPrefix(ev.Type, "whatsapp.") || ev.Type == accounts.EvUpdated { // the owner's accounts are theirs alone
		return ev, false
	}
	switch d := ev.Data.(type) {
	case model.Approval:
		if (d.Kind == model.ApprovalWhatsApp || d.Tool == fleet.ToolProcessStart) && d.AgentID != c.AgentID {
			return ev, false
		}
	case model.Message:
		if d.From == model.WhatsAppParty && d.To != c.AgentID {
			return ev, false
		}
	case model.CompanionTask:
		// An agent follows its own Companion tasks and messages, never the owner's
		// conversation or other agents' (as GET /api/companion/tasks/{id}).
		if d.AgentID == "" || d.AgentID != c.AgentID {
			return ev, false
		}
	case model.TimelineEntry:
		if d.AgentID != c.AgentID {
			ev.Data = redactEntry(d)
		}
	}
	return ev, true
}

// agentSnapshot drops other agents' private WhatsApp and command approvals.
func agentSnapshot(c fleet.Caller, snap model.Snapshot) model.Snapshot {
	if c.Owner {
		return snap
	}
	keep := snap.Approvals[:0:0]
	for _, ap := range snap.Approvals {
		if (ap.Kind != model.ApprovalWhatsApp && ap.Tool != fleet.ToolProcessStart) || ap.AgentID == c.AgentID {
			keep = append(keep, ap)
		}
	}
	snap.Approvals = keep
	return snap
}

// agentTimeline hides WhatsApp content and owned-command inputs in another agent's timeline.
func agentTimeline(c fleet.Caller, agentID string, entries []model.TimelineEntry) []model.TimelineEntry {
	if c.Owner || c.AgentID == agentID {
		return entries
	}
	out := make([]model.TimelineEntry, len(entries))
	for i, e := range entries {
		out[i] = redactEntry(e)
	}
	return out
}

func redactEntry(e model.TimelineEntry) model.TimelineEntry {
	switch {
	case strings.HasPrefix(e.Text, whatsapp.Frame), strings.HasPrefix(e.Text, "[WhatsApp]"),
		e.Kind == model.EntryMessageIn && e.Meta["from"] == model.WhatsAppParty:
		e.Text = waHidden
	case e.Kind == model.EntryToolUse && strings.HasPrefix(e.Tool, "mcp__tmxg__whatsapp_"):
		e.Input = json.RawMessage(`{}`)
	case e.Kind == model.EntryToolUse && (e.Tool == fleet.ToolProcessStart || strings.HasSuffix(e.Tool, "__process_start")):
		e.Input = json.RawMessage(`{}`)
	}
	return e
}
