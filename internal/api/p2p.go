package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

// p2pRoutes: the network layer's own owner routes (DESIGN.md "Peer-to-peer network").
// GET /api/p2p is the status; /api/p2p/net/* are low-level diagnostics (invites, join and
// raw envelopes without the chat layer) used by `tmx p2p` for live checks. The chat layer
// owns the user-facing invite/contact/chat routes.
func (s *Server) p2pRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/p2p/agent/invite", s.agentOnly(s.agentP2PInvite))
	mux.HandleFunc("POST /api/p2p/agent/join", s.agentOnly(s.agentP2PJoin))
	mux.HandleFunc("GET /api/p2p", s.ownerOnly(s.p2pStatus))
	mux.HandleFunc("PUT /api/p2p/config", s.ownerOnly(s.putP2PConfig))
	mux.HandleFunc("POST /api/p2p/restart", s.ownerOnly(s.p2pRestartNow))
	mux.HandleFunc("PUT /api/p2p/name", s.ownerOnly(s.withNode(s.p2pSetName)))
	mux.HandleFunc("GET /api/p2p/net/invites", s.ownerOnly(s.withNode(s.p2pInvites)))
	mux.HandleFunc("POST /api/p2p/net/invites", s.ownerOnly(s.withNode(s.p2pCreateInvite)))
	mux.HandleFunc("DELETE /api/p2p/net/invites/{id}", s.ownerOnly(s.withNode(s.p2pRevokeInvite)))
	mux.HandleFunc("POST /api/p2p/net/preview", s.ownerOnly(s.withNode(s.p2pPreview)))
	mux.HandleFunc("POST /api/p2p/net/join", s.ownerOnly(s.withNode(s.p2pJoin)))
	mux.HandleFunc("POST /api/p2p/net/send", s.ownerOnly(s.withNode(s.p2pSend)))
	mux.HandleFunc("GET /api/p2p/net/peers", s.ownerOnly(s.withNode(s.p2pPeers)))
	mux.HandleFunc("GET /api/p2p/net/log", s.ownerOnly(s.withNode(s.p2pLog)))
}

// P2PStatus is GET /api/p2p: the node's Status (the RUNNING state - what this process actually
// started with) plus its display name and, separately, SavedEnabled: config.json's current
// "p2p.enabled" (what the NEXT restart will start with). The two differ exactly while a save
// from PUT /api/p2p/config is waiting on a restart to take effect. Production is false in the
// trial profile, where peer-to-peer always stays off (fillDefaults) whatever "enabled" says -
// PUT /api/p2p/config refuses to turn it on there, and the UI should explain why rather than
// just letting the owner flip a switch that can never take effect.
type P2PStatus struct {
	p2p.Status
	Name           string `json:"name"`
	SavedEnabled   bool   `json:"saved_enabled"`
	RestartToApply bool   `json:"restart_to_apply"`
	Production     bool   `json:"production"`
}

func (s *Server) p2pStatus(w http.ResponseWriter, r *http.Request) {
	saved, production := false, false
	if c, err := config.Load(s.f.Paths()); err == nil {
		saved, production = c.P2P.On(), c.Production()
	}
	if s.p2p == nil {
		writeJSON(w, http.StatusOK, P2PStatus{Status: p2p.Status{Enabled: false, Addrs: []string{}, Reachability: p2p.Offline},
			SavedEnabled: saved, RestartToApply: saved, Production: production})
		return
	}
	out := P2PStatus{Status: s.p2p.Status(), Name: s.p2p.DisplayName(), SavedEnabled: saved, RestartToApply: !saved, Production: production}
	if out.Addrs == nil {
		out.Addrs = []string{}
	}
	writeJSON(w, http.StatusOK, out)
}

// PUT /api/p2p/name {name}: the name other people see in invites and chats (1–60 characters).
// It applies at once to new invites and messages, and is saved to config.json.
func (s *Server) p2pSetName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.Join(strings.Fields(req.Name), " ")
	if name == "" || utf8.RuneCountInString(name) > 60 || strings.ContainsFunc(name, unicode.IsControl) {
		writeError(w, http.StatusBadRequest, "the name must be 1 to 60 characters")
		return
	}
	if s.saveP2PName != nil {
		if err := s.saveP2PName(name); err != nil {
			writeError(w, http.StatusInternalServerError, "saving config.json: "+err.Error())
			return
		}
	}
	s.p2p.SetDisplayName(name)
	if s.chats != nil {
		s.chats.NameChanged()
	}
	s.p2pStatus(w, r)
}

// PUT /api/p2p/config {enabled, dht?}: turns peer-to-peer on or off, and optionally overrides
// whether it joins the public DHT, saved to config.json's "p2p" block. Like switching the head's
// harness (PUT /api/config), this takes effect from the next daemon start, not live - *p2p.Host
// is built once at startup (cli/daemon.go's startP2P) with no running stop/restart of its own,
// so there is nothing here to hot-swap. GET /api/p2p always shows both the running state
// (Status.Enabled) and the saved one (SavedEnabled) so the owner can see when they differ;
// POST /api/p2p/restart offers to apply the change right away.
//
// dht is optional: omitting it leaves config.json's own "dht" untouched and lets
// Config.EffectiveDHT() decide at the next start (production + enabled + not explicitly chosen
// -> on by default, outbound-only: AutoRelay/AutoNATv2, host.go:238 - no inbound listener, no
// NATPortMap, no firewall prompt). Setting dht here marks it DHTExplicit: a real owner+PIN
// choice, which EffectiveDHT then always respects, even to false.
func (s *Server) putP2PConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool  `json:"enabled"`
		DHT     *bool `json:"dht,omitempty"`
	}
	if !decode(w, r, &req) {
		return
	}
	c, err := config.Load(s.f.Paths())
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.Enabled && !c.Production() {
		writeError(w, http.StatusConflict, `peer-to-peer needs the production profile (config.json "profile": "production"); it stays off in trial however this is set`)
		return
	}
	if c.P2P == nil {
		c.P2P = &config.P2PConfig{}
	}
	dhtChanged := req.DHT != nil && (c.P2P.DHT == nil || *c.P2P.DHT != *req.DHT || !c.P2P.DHTExplicit)
	if req.Enabled != c.P2P.On() || dhtChanged {
		reason := "turn off peer-to-peer"
		switch {
		case req.Enabled && dhtChanged:
			reason = "change peer-to-peer settings"
		case req.Enabled:
			reason = "turn on peer-to-peer"
		case dhtChanged:
			reason = "change peer-to-peer settings"
		}
		if !s.RequireOwner(w, r, reason, req) {
			return
		}
		c.P2P.Enabled = &req.Enabled
		if req.DHT != nil {
			c.P2P.DHT, c.P2P.DHTExplicit = req.DHT, true
		}
		if err := config.Save(s.f.Paths(), c); err != nil {
			writeErr(w, err)
			return
		}
	}
	s.p2pStatus(w, r)
}

// POST /api/p2p/restart: a graceful self-restart (the same path POST /api/fleet-ops/restart
// uses for the head/leads; agents keep running, the daemon starts itself again), offered here
// so turning peer-to-peer on or off can take effect without the owner running a command herself.
func (s *Server) p2pRestartNow(w http.ResponseWriter, r *http.Request) {
	if s.selfRestart == nil {
		writeError(w, http.StatusNotImplemented, "this daemon cannot restart itself")
		return
	}
	if !s.RequireOwner(w, r, "restart the daemon now to apply the peer-to-peer setting", nil) {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true, "back_in": "about 15 seconds"})
	go func() {
		time.Sleep(500 * time.Millisecond) // let the answer reach the owner first
		s.selfRestart()
	}()
}

func (s *Server) withNode(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.p2p == nil {
			writeError(w, http.StatusServiceUnavailable, `peer-to-peer is off on this machine (config.json "p2p": {"enabled": false})`)
			return
		}
		h(w, r)
	}
}

func (s *Server) p2pInvites(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.p2p.Invites())
}

// InviteRequest is POST /api/p2p/net/invites.
type InviteRequest struct {
	Kind      p2p.InviteKind `json:"kind"`
	GroupID   string         `json:"group_id,omitempty"`
	GroupName string         `json:"group_name,omitempty"`
	TTLMillis int64          `json:"ttl_ms,omitempty"`
	MaxUses   int            `json:"max_uses,omitempty"`
}

func (s *Server) p2pCreateInvite(w http.ResponseWriter, r *http.Request) {
	var req InviteRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Kind == "" {
		req.Kind = p2p.InviteDM
	}
	inv, err := s.p2p.CreateInvite(p2p.InviteOptions{Kind: req.Kind, GroupID: req.GroupID, GroupName: req.GroupName,
		TTLMillis: req.TTLMillis, MaxUses: req.MaxUses})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, inv)
}

func (s *Server) p2pRevokeInvite(w http.ResponseWriter, r *http.Request) {
	if err := s.p2p.RevokeInvite(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type linkRequest struct {
	Link string `json:"link"`
}

func (s *Server) p2pPreview(w http.ResponseWriter, r *http.Request) {
	var req linkRequest
	if !decode(w, r, &req) {
		return
	}
	pv, err := s.p2p.Preview(req.Link)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

func (s *Server) p2pJoin(w http.ResponseWriter, r *http.Request) {
	var req linkRequest
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	p, err := s.p2p.Join(ctx, req.Link)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, p2p.ErrUnreachable) {
			status = http.StatusBadGateway
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// SendRequest is POST /api/p2p/net/send: one raw envelope (diagnostics).
type SendRequest struct {
	PeerID string          `json:"peer_id"`
	Type   string          `json:"type"`
	ChatID string          `json:"chat_id,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
}

func (s *Server) p2pSend(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Type == "" {
		req.Type = "ping"
	}
	env := p2p.Envelope{ID: "net-" + time.Now().Format("150405.000000"), Type: req.Type, ChatID: req.ChatID, Body: req.Body}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.p2p.Send(ctx, req.PeerID, env); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, p2p.ErrUnreachable) {
			status = http.StatusBadGateway
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": env.ID, "reachability": s.p2p.Reachable(req.PeerID)})
}

type peerInfo struct {
	PeerID       string           `json:"peer_id"`
	Reachability p2p.Reachability `json:"reachability"`
}

func (s *Server) p2pPeers(w http.ResponseWriter, r *http.Request) {
	out := []peerInfo{}
	if h, ok := s.p2p.(interface{ Peers() []string }); ok {
		for _, id := range h.Peers() {
			out = append(out, peerInfo{PeerID: id, Reachability: s.p2p.Reachable(id)})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) p2pLog(w http.ResponseWriter, r *http.Request) {
	if s.p2pLogs == nil {
		writeError(w, http.StatusNotFound, `the network log is off (config.json "p2p": {"debug_drain": true} turns it on; never with the chat layer)`)
		return
	}
	writeJSON(w, http.StatusOK, s.p2pLogs.Entries())
}
