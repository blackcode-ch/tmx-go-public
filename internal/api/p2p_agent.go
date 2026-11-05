package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/chat"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/peertext"
)

// AgentP2PInviteRequest starts a request, or resumes exactly the server-held
// request named by ApprovalID. Resuming never accepts replacement arguments.
type AgentP2PInviteRequest struct {
	Label      string `json:"label,omitempty" jsonschema:"person label or name of the new group"`
	Kind       string `json:"kind,omitempty" jsonschema:"person (default) or group; group creates a new group named label"`
	TTL        string `json:"ttl,omitempty" jsonschema:"Go duration such as 24h or 30m; default 24h, maximum 720h"`
	MaxUses    int    `json:"max_uses,omitempty" jsonschema:"maximum uses; default 1"`
	ApprovalID string `json:"approval_id,omitempty" jsonschema:"resume the returned approval ID after the owner answers; omit all other fields"`
}

type AgentP2PJoinRequest struct {
	Link       string `json:"link,omitempty" jsonschema:"tmx://join invitation link"`
	ApprovalID string `json:"approval_id,omitempty" jsonschema:"resume the returned approval ID after the owner answers; omit link"`
}

type AgentP2PResult struct {
	ApprovalID string              `json:"approval_id"`
	State      model.ApprovalState `json:"state"`
	Preview    string              `json:"preview"`
	Invite     *p2p.Invite         `json:"invite,omitempty"`
	Joined     *chat.JoinResult    `json:"joined,omitempty"`
	Error      string              `json:"error,omitempty"`
}

type p2pAction struct {
	tokenHash [32]byte
	mu        sync.Mutex
	caller    fleet.Caller
	tool      string
	expires   time.Time
	invite    AgentP2PInviteRequest
	ttl       time.Duration
	link      string
	result    AgentP2PResult
	done      bool
}
type p2pActionStore struct {
	sync.Mutex
	actions map[string]*p2pAction
}

func (s *Server) p2pAgent(w http.ResponseWriter, r *http.Request) (fleet.Caller, bool) {
	c, _ := caller(r)
	if err := s.f.CheckP2PAgent(c); err != nil {
		writeErr(w, err)
		return c, false
	}
	if s.chats == nil || !s.chats.Enabled() {
		writeError(w, 503, "peer-to-peer is off")
		return c, false
	}
	return c, true
}

func (s *Server) agentP2PInvite(w http.ResponseWriter, r *http.Request) {
	c, ok := s.p2pAgent(w, r)
	if !ok {
		return
	}
	var in AgentP2PInviteRequest
	if !decode(w, r, &in) {
		return
	}
	if in.ApprovalID != "" {
		if in.Label != "" || in.Kind != "" || in.TTL != "" || in.MaxUses != 0 {
			writeError(w, 400, "resume with approval_id only")
			return
		}
		s.resumeP2P(w, r, c, in.ApprovalID, fleet.ToolP2PInvite)
		return
	}
	in.Label = strings.TrimSpace(in.Label)
	if in.Label == "" || utf8.RuneCountInString(in.Label) > 80 || strings.ContainsFunc(in.Label, unicode.IsControl) {
		writeError(w, 400, "label must be 1 to 80 characters without control characters")
		return
	}
	if in.Kind == "" {
		in.Kind = "person"
	}
	if in.Kind != "person" && in.Kind != "group" {
		writeError(w, 400, "kind must be person or group")
		return
	}
	if in.TTL == "" {
		in.TTL = "24h"
	}
	ttl, err := time.ParseDuration(in.TTL)
	if err != nil || ttl < time.Minute || ttl > 720*time.Hour {
		writeError(w, 400, "ttl must be a duration between 1m and 720h")
		return
	}
	if in.MaxUses == 0 {
		in.MaxUses = 1
	}
	if in.MaxUses < 1 || in.MaxUses > 1000 {
		writeError(w, 400, "max_uses must be between 1 and 1000")
		return
	}
	uses := "uses"
	if in.MaxUses == 1 {
		uses = "use"
	}
	label := peertext.Field(in.Label)
	preview := fmt.Sprintf("Create an invite for %s, valid %g h, %d %s", label, ttl.Hours(), in.MaxUses, uses)
	if in.Kind == "group" {
		preview += "; create a new group named " + label
	}
	s.startP2P(w, r, &p2pAction{caller: c, tool: fleet.ToolP2PInvite, invite: in, ttl: ttl, result: AgentP2PResult{Preview: preview}})
}

func (s *Server) agentP2PJoin(w http.ResponseWriter, r *http.Request) {
	c, ok := s.p2pAgent(w, r)
	if !ok {
		return
	}
	var in AgentP2PJoinRequest
	if !decode(w, r, &in) {
		return
	}
	if in.ApprovalID != "" {
		if in.Link != "" {
			writeError(w, 400, "resume with approval_id only")
			return
		}
		s.resumeP2P(w, r, c, in.ApprovalID, fleet.ToolP2PJoin)
		return
	}
	in.Link = strings.TrimSpace(in.Link)
	pv, err := s.chats.Preview(in.Link)
	if err != nil {
		writeChatErr(w, err)
		return
	}
	preview := "Join " + peertext.Field(pv.Name) + "'s tmx"
	if pv.Kind == p2p.InviteGroup {
		preview += " (group " + peertext.Field(pv.GroupName) + ")"
	}
	s.startP2P(w, r, &p2pAction{caller: c, tool: fleet.ToolP2PJoin, link: in.Link, result: AgentP2PResult{Preview: preview}})
}

// Each new call gets one hold; only this in-memory registry can redeem it. A
// daemon restart loses all unexecuted capabilities and therefore fails closed.
func (s *Server) startP2P(w http.ResponseWriter, r *http.Request, a *p2pAction) {
	s.p2pActions.Lock()
	if s.p2pActions.actions == nil {
		s.p2pActions.actions = map[string]*p2pAction{}
	}
	count := 0
	for id, old := range s.p2pActions.actions {
		if time.Now().After(old.expires) {
			_, _ = s.f.CancelHold(old.caller, id)
			delete(s.p2pActions.actions, id)
		} else if old.caller.AgentID == a.caller.AgentID {
			count++
		}
	}
	if count >= 32 {
		s.p2pActions.Unlock()
		writeError(w, 429, "too many recent pairing requests; wait for existing requests to expire")
		return
	}
	ap, err := s.f.HoldP2P(a.caller, a.tool, a.result.Preview)
	if err != nil {
		s.p2pActions.Unlock()
		writeErr(w, err)
		return
	}
	a.expires = time.Now().Add(15 * time.Minute)
	a.result.ApprovalID = ap.ID
	a.result.State = ap.State
	a.tokenHash = sha256.Sum256([]byte(r.Header.Get("Authorization")))
	s.p2pActions.actions[ap.ID] = a
	s.p2pActions.Unlock()
	time.AfterFunc(time.Until(a.expires), func() {
		s.p2pActions.Lock()
		delete(s.p2pActions.actions, ap.ID)
		s.p2pActions.Unlock()
		_, _ = s.f.CancelHold(a.caller, ap.ID)
	})
	// An explicit standing grant runs immediately; otherwise return the preview
	// before joining or creating anything. Clients resume the same ID later.
	if ap.State == model.ApprovalAllowed {
		s.resumeP2P(w, r, a.caller, ap.ID, a.tool)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	writeJSON(w, http.StatusAccepted, a.result)
}

func (s *Server) resumeP2P(w http.ResponseWriter, r *http.Request, c fleet.Caller, id, tool string) {
	s.p2pActions.Lock()
	a := s.p2pActions.actions[id]
	s.p2pActions.Unlock()
	if a == nil || a.caller.AgentID != c.AgentID || a.tool != tool || a.tokenHash != sha256.Sum256([]byte(r.Header.Get("Authorization"))) {
		writeError(w, 404, "no matching pairing request for this agent and tool")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Now().After(a.expires) {
		_, _ = s.f.CancelHold(c, id)
		writeError(w, 410, "pairing request expired; nothing further will run")
		return
	}
	if a.done {
		writeJSON(w, 200, a.result)
		return
	}
	ap, err := s.f.WaitHold(r.Context(), c, id, 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	a.result.State = ap.State
	if ap.State != model.ApprovalAllowed {
		writeJSON(w, 200, a.result)
		return
	}
	// Recheck tier/liveness immediately before the mutation. The request body
	// cannot change the held operation, and concurrent/repeated resumes execute once.
	if err := s.f.CheckP2PAgent(c); err != nil {
		writeErr(w, err)
		return
	}
	a.done = true
	if a.tool == fleet.ToolP2PInvite {
		req := chat.InviteRequest{Kind: p2p.InviteDM, TTLMs: a.ttl.Milliseconds(), MaxUses: a.invite.MaxUses}
		if a.invite.Kind == "group" {
			var group model.Chat
			group, err = s.chats.Create(chat.CreateRequest{Kind: model.ChatGroup, Name: a.invite.Label})
			if err == nil {
				req.Kind = p2p.InviteGroup
				req.GroupID = group.ID
			}
		}
		if err == nil {
			var inv p2p.Invite
			inv, err = s.chats.CreateInvite(req)
			if err == nil {
				a.result.Invite = &inv
			}
		}
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		var joined chat.JoinResult
		joined, err = s.chats.Join(ctx, a.link)
		if err == nil {
			a.result.Joined = &joined
		}
	}
	// Cache failures too: a retry must not repeat a possibly successful mutation.
	if err != nil {
		a.result.Error = err.Error()
	}
	a.link = ""
	writeJSON(w, 200, a.result)
}
