package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/peers"
)

// The Python fleet interop (DESIGN.md "Python fleet interop"): the fleet's other machines,
// still on the Python tmx daemon, read this daemon's agents and hand over mail through the
// protocol routes below, exactly as they do with one another, using a pair token (X-TMX-Token
// + X-TMX-From), never the owner's bearer token. The guard's network rule applies first: a peer
// arrives over the tailnet through tailscale serve (a trusted https host), or from this machine.
//
// A trusted ("full" role) peer also reaches the ordinary /api/ routes, same standing as the
// owner there (Python's auth(): a pair-token caller is one shared "peer" role, checked by each
// route like any other caller, never a separate allowlist) - so an architect or lead on another
// machine can launch/screen/type/keys/restart agents here. servePeer sets that caller and falls
// through to the same mux as everyone else; only the Python-shaped protocol paths below keep
// their own dedicated handlers.

// peerProtocolPaths are the Python-wire-shaped routes: their own handlers, Python's reply shape.
var peerProtocolPaths = map[string]bool{"/api/sessions": true, "/api/messages/incoming": true, "/api/peers/pair-token": true, "/api/gate/pin/record": true, "/api/peer/summarize-local": true}

var wireIDRx = regexp.MustCompile(`^[0-9a-f]{12,32}$`)

type peerCtxKey struct{}

// peerCaller is an authenticated peer machine (always by its pair token).
type peerCaller struct {
	name string
}

func peerOf(r *http.Request) peerCaller {
	p, _ := r.Context().Value(peerCtxKey{}).(peerCaller)
	return p
}

// peerHeaderRoute reports whether r carries a peer's credentials at all (the pair-token header,
// or Authorization when a peer uses it): peers are recognised by header, not by path, now that
// they also reach the ordinary routes.
func peerHeaderRoute(r *http.Request) bool {
	if r.Header.Get(peers.HeaderFrom) == "" {
		return false
	}
	return r.Header.Get(peers.HeaderToken) != "" || strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (s *Server) peerProtocolRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions", s.peerSessions)
	mux.HandleFunc("POST /api/messages/incoming", s.peerIncoming)
	mux.HandleFunc("POST /api/peers/pair-token", s.peerPairToken)
	mux.HandleFunc("GET /api/gate/pin/record", s.peerPinRecord)            // ownerpin.go
	mux.HandleFunc("POST /api/gate/pin/record", s.peerPinAdopt)            // ownerpin.go
	mux.HandleFunc("POST /api/peer/summarize-local", s.peerSummarizeLocal) // summaries.go
	return mux
}

// servePeer authenticates a peer machine and serves its routes: the Python-protocol paths
// through their own handlers, everything else through next (the ordinary app mux) carrying a
// peer-flavored fleet.Caller, same standing as the owner there (Python's single "peer" role;
// see the package doc above). A browser never gets here (no Origin). On-behalf identifies
// whom the peer is acting for on its OWN machine (its daemon, its operator, or one of its own
// agents - "agent:<name>") - it is never read as a request to relay/impersonate on THIS
// machine; the caller here is always, only, fleet.PeerCaller(from), whatever on-behalf says
// (tmx-lead, 3 Nov: a P1 regression - Hosta's real traffic sends on-behalf daemon, operator
// AND agent:<name> for ordinary relayed reads like /api/health, /api/sessions, relay-invites
// polling; refusing agent:<name> broke all of them).
func (s *Server) servePeer(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if s.peers == nil {
		writeError(w, http.StatusNotFound, "this daemon has no fleet peers (production profile, config.json \"peers\")")
		return
	}
	if len(r.Header.Values("Origin")) > 0 {
		writeError(w, http.StatusForbidden, "peers only")
		return
	}
	from := strings.ToLower(strings.TrimSpace(r.Header.Get(peers.HeaderFrom)))
	tok := strings.TrimSpace(r.Header.Get(peers.HeaderToken))
	if tok == "" {
		tok, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		tok = strings.TrimSpace(tok)
	}
	if tok == "" || !s.peers.Known(from) {
		writeError(w, http.StatusUnauthorized, "missing or invalid peer token")
		return
	}
	// Pair tokens only: the fleet token was retired fleet-wide on 1 Nov 2026 (the Python daemons
	// refuse it too), so a machine joins through its own pair token (tmx peers import-python).
	owner, ok := s.peers.Tokens().Identify(tok)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing or invalid peer token")
		return
	}
	if owner != from {
		writeError(w, http.StatusUnauthorized, "this token belongs to another machine")
		return
	}
	// On-behalf is informational for ordinary reads and agent-operation routes (any value ->
	// PeerCaller, logged at debug when it names neither daemon, operator nor one of this
	// peer's own parties). Owner material - the PIN sync record (GET reveals the scrypt hash,
	// POST pushes a new one) and the pair-token rotation - is different: those routes accept
	// only the machine itself (no on-behalf, or "daemon"), never a relayed agent, device or
	// operator, however that peer authenticated (go-reviewer-o/tmx-lead, 3 Nov: a relayed
	// agent or device must never read or push the owner's PIN record, or rotate the pair
	// token, through another machine's daemon).
	ob := strings.ToLower(strings.TrimSpace(r.Header.Get(peers.HeaderOnBehalf)))
	if ownerMaterialRoute(r) && ob != "" && ob != "daemon" {
		writeError(w, http.StatusForbidden, "this route is the peer machine's own: never a relayed agent, device or operator")
		return
	}
	if ob != "" && ob != "daemon" && ob != "operator" {
		s.log.Debug("p2p: peer call with an unrecognised on-behalf", "peer", from, "on_behalf", ob, "path", r.URL.Path)
	}
	if peerProtocolPaths[r.URL.Path] {
		c := peerCaller{name: from}
		s.peerMux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), peerCtxKey{}, c)))
		return
	}
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, fleet.PeerCaller(from))))
}

// ownerMaterialRoute reports whether r reads or writes owner-controlled material (the PIN
// record's scrypt hash, or the pair token itself) over the peer protocol: the one class of
// peer route where on-behalf does NOT pass through as an ordinary relayed call (see
// servePeer) - accepted only from the machine itself, never a relayed agent, device or
// operator.
func ownerMaterialRoute(r *http.Request) bool {
	switch {
	case r.URL.Path == "/api/gate/pin/record": // GET reveals the scrypt hash; POST pushes one
		return true
	case r.Method == http.MethodPost && r.URL.Path == "/api/peers/pair-token":
		return true
	}
	return false
}

// pythonState maps an agent state to the Python daemon's detector states where they differ.
func pythonState(st model.AgentState) string {
	switch st {
	case model.StateWaiting:
		return "dialog"
	case model.StateStopped, model.StateError:
		return "exited"
	}
	return string(st)
}

// buildCommit is the VCS revision this binary was built from ("" when unknown).
func buildCommit() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range bi.Settings {
			if kv.Key == "vcs.revision" {
				return kv.Value
			}
		}
	}
	return ""
}

// peerSessions is GET /api/sessions in the Python daemon's shape: this machine's agents.
func (s *Server) peerSessions(w http.ResponseWriter, r *http.Request) {
	snap, err := s.f.Snapshot()
	if err != nil {
		writeErr(w, err)
		return
	}
	host := snap.Machine
	if s.peers != nil {
		host = s.peers.Machine()
	}
	projects := map[string]string{}
	for _, p := range snap.Projects {
		projects[p.ID] = p.Name
	}
	agents := snap.Agents
	if snap.Head != nil {
		agents = append([]model.Agent{*snap.Head}, agents...)
	}
	seen := map[string]bool{}
	rows := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		rows = append(rows, map[string]any{
			"name": a.Name, "host": host, "id": a.ID, "adapter": string(a.Harness), "state": pythonState(a.State),
			"title": a.Role, "role": a.Role, "queued_messages": a.Queued, "managed": true,
			"rooms": []string{}, "tier": string(a.Level), "cwd": a.Cwd, "project": projects[a.ProjectID],
			"conversation_id": a.SessionID, "started_at": float64(a.CreatedAt) / 1000,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"host": host, "commit": buildCommit(), "version": s.f.Version(),
		"sessions": rows, "supervisor": s.supervisorRef(host), "system": map[string]any{}})
}

// peerIncoming is POST /api/messages/incoming: a peer hands over mail for an agent here.
// The peer vouches only for its own agents (sender_host must be the peer); the body must match
// its sha256; the same ID again is the same message (idempotent).
func (s *Server) peerIncoming(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ID         string  `json:"id"`
		Sender     string  `json:"sender"`
		SenderHost string  `json:"sender_host"`
		To         string  `json:"to"`
		Body       string  `json:"body"`
		ReplyTo    *string `json:"reply_to"`
		Urgent     bool    `json:"urgent"`
		SHA256     string  `json:"sha256"`
	}
	if !decodeMax(w, r, &b, fleet.MaxBodyBytes*2+4096) {
		return
	}
	p := peerOf(r)
	if b.Sender == "" || b.SenderHost == "" || b.To == "" || b.Body == "" {
		writeError(w, http.StatusBadRequest, "incoming message without sender, sender_host, to or body")
		return
	}
	if !wireIDRx.MatchString(b.ID) {
		writeError(w, http.StatusBadRequest, "id must be 12-32 lowercase hex characters")
		return
	}
	if b.SHA256 != "" {
		sum := sha256.Sum256([]byte(b.Body))
		if subtle.ConstantTimeCompare([]byte(strings.ToLower(b.SHA256)), []byte(hex.EncodeToString(sum[:]))) != 1 {
			writeError(w, http.StatusConflict, "message damaged in transit: nothing was delivered, send it again")
			return
		}
	}
	if !strings.EqualFold(b.SenderHost, p.name) {
		writeError(w, http.StatusForbidden, "sender_host must be the machine that hands the message over")
		return
	}
	in := fleet.PeerInbound{Peer: p.name, ID: b.ID, Sender: strings.ToLower(b.Sender), SenderHost: p.name,
		To: b.To, Body: b.Body, Urgent: b.Urgent}
	if b.ReplyTo != nil {
		in.ReplyTo = *b.ReplyTo
	}
	m, _, err := s.f.DeliverPeer(in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": b.ID, "state": string(m.State)})
}

// peerPairToken is POST /api/peers/pair-token: a ROTATION of the pair token, sent over the
// current one by the peer whose name sorts first (the Python rule: the generator rotates). A
// first token never arrives here: the fleet token is retired, so a machine joins through
// tmx peers import-python. The answer is the new token's fingerprint, never the token.
func (s *Server) peerPairToken(w http.ResponseWriter, r *http.Request) {
	p := peerOf(r)
	var b struct {
		Token string `json:"token"`
		Role  string `json:"role"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !(p.name < s.peers.Machine()) {
		writeError(w, http.StatusForbidden, "this machine generates the pair token it shares with "+p.name+", not the other way round")
		return
	}
	if b.Role != "" && b.Role != "full" {
		writeError(w, http.StatusBadRequest, "role: full")
		return
	}
	fp, err := s.peers.Tokens().Store(p.name, b.Token, p.name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("peer token rotated", "peer", p.name, "fingerprint", fp)
	writeJSON(w, http.StatusOK, map[string]string{"fingerprint": fp})
}

// peerRotate is POST /api/peers/{name}/rotate (the owner, PIN-gated): this machine generates a
// fresh pair token and pushes it to a peer whose name sorts after this machine's own
// (peers.Client.RotateWith - DESIGN.md's rule, the Python one: the lower-sorting name
// generates and rotates). The new token only replaces the one on disk here once the peer's own
// fingerprint confirms it received exactly that token, so a network failure or a mismatch never
// leaves the two machines holding different ideas of the current token.
func (s *Server) peerRotate(w http.ResponseWriter, r *http.Request) {
	if s.peers == nil {
		writeError(w, http.StatusNotFound, "this daemon has no fleet peers (production profile, config.json \"peers\")")
		return
	}
	name := strings.ToLower(r.PathValue("name"))
	if !s.peers.Known(name) {
		writeError(w, http.StatusNotFound, "no peer machine "+name)
		return
	}
	if !s.RequireOwner(w, r, "rotate the pair token shared with "+name, nil) {
		return
	}
	fp, err := s.peers.RotateWith(r.Context(), name)
	if err != nil {
		var refused peers.Refused
		if errors.As(err, &refused) {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeErr(w, err)
		return
	}
	s.log.Info("peer token rotated by us", "peer", name, "fingerprint", fp)
	writeJSON(w, http.StatusOK, map[string]string{"fingerprint": fp})
}

// peerStatus is GET /api/peers (the owner): what this daemon last saw of each peer.
func (s *Server) peerStatus(w http.ResponseWriter, r *http.Request) {
	if s.peers == nil {
		writeJSON(w, http.StatusOK, map[string]any{"machine": s.f.Machine(), "supervisor": s.f.SupervisorName(), "peers": []peers.Status{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"machine": s.peers.Machine(), "supervisor": s.f.SupervisorName(), "peers": s.peers.Statuses()})
}
