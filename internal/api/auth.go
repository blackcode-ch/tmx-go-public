package api

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

type ctxKey struct{}

// caller returns the authenticated caller (set by guard; zero for public routes).
func caller(r *http.Request) (fleet.Caller, bool) {
	c, ok := r.Context().Value(ctxKey{}).(fleet.Caller)
	return c, ok
}

// Wails webview origins (macOS/Linux use wails://wails, Windows http://wails.localhost).
var wailsOrigins = []string{"wails://wails", "http://wails.localhost"}

func (s *Server) allowedOrigin(origin string) bool {
	for _, o := range wailsOrigins {
		if origin == o {
			return true
		}
	}
	for _, h := range []string{"127.0.0.1", "localhost"} {
		if origin == fmt.Sprintf("http://%s:%d", h, s.port) {
			return true
		}
	}
	return false
}

// localHost rejects requests whose Host is not a loopback name (DNS rebinding).
func localHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	return h == "127.0.0.1" || h == "localhost" || h == "::1" || h == "wails.localhost"
}

// trustedProxy accepts forwarding metadata only from the local TCP peer. It
// deliberately ignores Forwarded, X-Forwarded-Host and X-Forwarded-For.
func (s *Server) trustedProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return false
	}
	proto := r.Header.Values("X-Forwarded-Proto")
	if len(proto) != 1 || proto[0] != "https" {
		return false
	}
	for _, host := range s.trustedHosts {
		if config.ValidTrustedHost(host) && config.ValidTrustedHost(r.Host) && config.NormalizeTrustedHost(r.Host) == config.NormalizeTrustedHost(host) {
			return true
		}
	}
	return false
}

// guard applies the host check and CORS to everything, and token auth to /api/ (except
// /api/health).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote := !localHost(r.Host)
		if remote && !s.trustedProxy(r) {
			writeError(w, http.StatusForbidden, "this daemon answers only on 127.0.0.1")
			return
		}
		if peerHeaderRoute(r) { // the fleet's other machines: their own auth, never a browser (peers.go)
			s.servePeer(w, r, next)
			return
		}
		origins := r.Header.Values("Origin")
		origin := r.Header.Get("Origin")
		// Navigations and same-origin GET/HEAD fetches may omit Origin. Writes
		// and websocket upgrades must carry the exact trusted HTTPS origin.
		needsOrigin := remote && (r.Method != http.MethodGet && r.Method != http.MethodHead || r.Header.Get("Upgrade") != "")
		if len(origins) > 1 || needsOrigin && origin == "" {
			writeError(w, http.StatusForbidden, "origin not allowed")
			return
		}
		if origin != "" {
			allowed := s.allowedOrigin(origin)
			if remote {
				allowed = origin == "https://"+config.NormalizeTrustedHost(r.Host)
			}
			if !allowed {
				writeError(w, http.StatusForbidden, "origin not allowed")
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, "+ConfirmHeader+", "+UISessionHeader+", "+GraceHeader)
			h.Set("Access-Control-Expose-Headers", "Idempotency-Key, X-Transcription-Receipt, Idempotency-Replayed")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Max-Age", "600")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/health" || publicPairRoute(r) {
			next.ServeHTTP(w, r)
			return
		}
		if (ticketRequest(r) || waTicketRequest(r)) && r.URL.Query().Get("ticket") != deviceMediaTicket { // legacy bearer playback tickets
			next.ServeHTTP(w, r)
			return
		}
		c, ok := s.authenticate(r)
		if !ok && strings.TrimSpace(r.Header.Get("Authorization")) == "" {
			if ctx, cleanup, valid := s.deviceSession(r); valid {
				defer cleanup()
				r = r.WithContext(ctx)
				c, ok = fleet.OwnerCaller, true
			}
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		if id, restricted := r.Context().Value(recoverySessionContext{}).(string); restricted &&
			!(r.Method == "GET" && r.URL.Path == "/api/devices/recovery" || r.Method == "DELETE" && r.URL.Path == "/api/devices/"+id) {
			writeError(w, 403, "restricted recovery session: host proof required for owner access")
			return
		}
		if c.Companion && !s.companionGuard(w, r) {
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c)))
	})
}

func (s *Server) authenticate(r *http.Request) (fleet.Caller, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		if !localHost(r.Host) && r.URL.Query().Has("token") {
			return fleet.Caller{}, false // never put credentials into remote request URLs
		}
		if !ok {
			for _, value := range r.Header.Values("Sec-WebSocket-Protocol") {
				for _, protocol := range strings.Split(value, ",") {
					if value, found := strings.CutPrefix(strings.TrimSpace(protocol), "bearer."); found {
						if tok != "" {
							return fleet.Caller{}, false
						}
						tok = value
					}
				}
			}
			if tok == "" && localHost(r.Host) {
				tok = r.URL.Query().Get("token") // legacy local clients
			}
		}
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return fleet.Caller{}, false
	}
	if s.owner != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.owner)) == 1 {
		return fleet.OwnerCaller, true
	}
	if c, ok := s.f.CallerForToken(tok); ok {
		return c, true
	}
	if s.companion != nil && s.companion.LinkTokenMatches(tok) {
		return fleet.CompanionCaller, true
	}
	return fleet.Caller{}, false
}
