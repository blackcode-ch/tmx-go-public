package api

import (
	_ "embed"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/gate"
)

const bootstrapCookie = "__Host-tmx-bootstrap"
const bootstrapTTL = 10 * time.Minute

// This grant is deliberately NOT an ownerDevice or a general owner session.
// Only the two public bootstrap handlers understand it. Restart loses it.
type ownerBootstrap struct {
	Recovery                    bool
	Host, LinkHash, SessionHash string
	Expires                     time.Time
}

//go:embed owner_bootstrap.html
var ownerBootstrapPage string

func (s *Server) bootstrapRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/devices/recovery", s.ownerOnly(s.recoveryStatus))
	mux.HandleFunc("POST /api/devices/{id}/upgrade", s.ownerOnly(s.upgradeRecoveryDevice))
	mux.HandleFunc("POST /api/devices/bootstrap", s.ownerOnly(s.issueOwnerBootstrap))
	mux.HandleFunc("POST /api/pair/bootstrap/exchange", s.exchangeOwnerBootstrap)
	mux.HandleFunc("POST /api/pair/bootstrap/approve", s.approveOwnerBootstrap)
	mux.HandleFunc("GET /owner-bootstrap", func(w http.ResponseWriter, r *http.Request) {
		if !s.browserReady(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		_, _ = w.Write([]byte(ownerBootstrapPage))
	})
}

// Issuance is an explicit host administration operation, including recovery.
// Root token possession alone is insufficient. Normal issuance needs fresh host
// proof; unavailable-authenticator recovery needs a surviving proven UI session.
// Never expose the private link in an HTTP response or event.
func (s *Server) issueOwnerBootstrap(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticate(r)
	if !localRequest(r) ||
		!ok || !c.Owner || c.Companion || c.AgentID != "" {
		writeError(w, 403, "issue a bootstrap link from the host owner UI")
		return
	}
	var req struct {
		Host     string `json:"host"`
		Recovery bool   `json:"recovery"`
	}
	if !decode(w, r, &req) {
		return
	}
	host := config.NormalizeTrustedHost(req.Host)
	allowed := false
	for _, h := range s.trustedHosts {
		if config.ValidTrustedHost(h) && config.ValidTrustedHost(req.Host) && config.NormalizeTrustedHost(h) == host {
			allowed = true
		}
	}
	if !allowed {
		writeError(w, 400, "choose a configured trusted HTTPS authority")
		return
	}
	d := s.devices
	if d == nil || d.err != nil {
		writeError(w, 503, "device pairing unavailable")
		return
	}
	if s.gate == nil || !validUISession(r.Header.Get(UISessionHeader)) {
		writeError(w, 403, "use the host owner UI with host confirmation")
		return
	}
	if req.Recovery {
		if s.gate.Status().Available || !s.gate.RecoverySession(gateCaller(r), r.Header.Get(GraceHeader)) {
			writeError(w, 403, "recovery requires unavailable host authentication and this UI's surviving host-proof session")
			return
		}
	} else if !s.requireOwner(w, r, gate.Action{Reason: "issue a one-pair link for https://" + host, PromptReason: "pair a new owner device at https://" + host, Key: actionKey(r, req), Always: true, Fresh: true}) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.limit("bootstrap-issue", 3) {
		writeError(w, 429, "too many bootstrap requests")
		return
	}
	// Invalidate before writing: a failed reissue must not leave a former grant live.
	d.bootstrap = nil
	expires := d.now().Add(bootstrapTTL)
	if req.Recovery {
		if s.recoveryNotice == nil || s.recoveryNotice(host, expires) != nil {
			writeError(w, 503, "could not publish the required host recovery notice")
			return
		}
	}
	link := randomDeviceID()
	path := filepath.Join(filepath.Dir(d.path), "owner-bootstrap-link")
	if err := config.WriteSecretFile(path, []byte("https://"+host+"/owner-bootstrap#bootstrap="+link+"\n")); err != nil {
		writeError(w, 503, "could not write private bootstrap link")
		return
	}
	d.bootstrap = &ownerBootstrap{Host: host, LinkHash: deviceHash(link), Expires: expires, Recovery: req.Recovery}
	w.Header().Set("Cache-Control", "no-store")
	s.deviceEvent("bootstrap_issued", "")
	writeJSON(w, 201, map[string]int{"expires_in": 600})
}

func (s *Server) exchangeOwnerBootstrap(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w, r) {
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &req) {
		return
	}
	d := s.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.limit("bootstrap-exchange", 10) {
		writeError(w, 429, "too many bootstrap attempts")
		return
	}
	b := d.bootstrap
	if b == nil || req.Token == "" || b.LinkHash != deviceHash(req.Token) || b.Host != requestHost(r) || !d.now().Before(b.Expires) {
		writeError(w, 403, "bootstrap link invalid, used or expired")
		return
	}
	token := randomDeviceID()
	b.LinkHash = ""
	b.SessionHash = deviceHash(token)
	_ = os.Remove(filepath.Join(filepath.Dir(d.path), "owner-bootstrap-link")) // already spent even if removal fails
	setDeviceCookie(w, bootstrapCookie, token, int(b.Expires.Sub(d.now()).Seconds()))
	writeJSON(w, 200, map[string]bool{"ready": true, "recovery": b.Recovery})
}

func (s *Server) approveOwnerBootstrap(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w, r) {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	d := s.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.limit("pair-approve", 10) {
		writeError(w, 429, "too many pairing attempts")
		return
	}
	b := d.bootstrap
	token := cookieValue(r, bootstrapCookie)
	if b == nil || token == "" || b.SessionHash != deviceHash(token) || b.Host != requestHost(r) || !d.now().Before(b.Expires) {
		writeError(w, 403, "bootstrap session invalid, used or expired")
		return
	}
	d.gc()
	p := d.pairs[strings.ToUpper(strings.TrimSpace(req.Code))]
	if p == nil || p.Approved || p.Host != b.Host {
		writeError(w, 400, "invalid or expired pairing code")
		return
	}
	// Consume and approve under the same mutex: concurrent submissions cannot
	// authorize a second device. The normal claim cookie and enrollment apply.
	d.bootstrap = nil
	p.Approved = true
	if b.Recovery {
		p.RecoveryUntil = b.Expires
		p.Expires = minTime(p.Expires, b.Expires)
	}
	setDeviceCookie(w, bootstrapCookie, "", -1)
	s.deviceEvent("bootstrap_pair_approved", "")
	writeJSON(w, 200, map[string]bool{"approved": true})
}

func (s *Server) recoveryStatus(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w, r) {
		return
	}
	d := s.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	i, ok := d.find(r)
	if !ok {
		writeError(w, 403, "device session required")
		return
	}
	v := d.state.Devices[i]
	if !v.Recovery {
		setDeviceCookie(w, deviceCookie, cookieValue(r, deviceCookie), int(v.Expires.Sub(d.now()).Seconds()))
	}
	writeJSON(w, 200, map[string]any{"id": v.ID, "recovery": v.Recovery, "expires": v.Expires, "runbook": "Keep the independent Python tmx/SSH host route available. Repair host authentication through its existing controls. In the host Owner devices UI, confirm Upgrade with a fresh host proof before this session expires. Until then this session cannot perform owner actions. If the host UI session is lost, keep Go stopped and use the independent route; do not delete gate markers or weaken sign-in."})
}

func (s *Server) upgradeRecoveryDevice(w http.ResponseWriter, r *http.Request) {
	if !localRequest(r) || !validUISession(r.Header.Get(UISessionHeader)) {
		writeError(w, 403, "upgrade recovery on the host owner UI")
		return
	}
	d := s.devices
	if d == nil || d.err != nil {
		writeError(w, 503, "devices unavailable")
		return
	}
	id := r.PathValue("id")
	if !s.requireOwner(w, r, gate.Action{Reason: "upgrade recovery device to owner", Key: actionKey(r, id), Always: true, Fresh: true}) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, v := range d.state.Devices {
		if v.ID != id || !v.Recovery || !d.now().Before(v.Expires) {
			continue
		}
		before := d.before()
		d.state.Devices[i].Recovery = false
		d.state.Devices[i].Expires = d.now().Add(deviceTTL)
		if err := d.save(before); err != nil {
			writeError(w, 503, "could not upgrade device")
			return
		}
		s.deviceEvent("recovery_upgraded", id)
		writeJSON(w, 200, map[string]bool{"upgraded": true})
		return
	}
	writeError(w, 404, "unexpired recovery device not found")
}
