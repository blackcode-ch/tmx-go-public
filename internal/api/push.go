package api

import (
	"context"
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/push"
)

type phonePush interface {
	PublicKey() (string, error)
	Enabled(string) bool
	Subscribe(context.Context, string, push.Subscription) error
	Unsubscribe(string) error
	NotifyApproval(context.Context, string) error
}

func (s *Server) initPush() {
	if s.f == nil || s.f.Paths().Home == "" {
		return
	}
	s.push = push.Open(s.f.Paths().SecretsDir(), s.pushDeviceActive, s.log)
	s.f.SetApprovalNotifier(func(ctx context.Context, ap model.Approval) (string, error) {
		return "webpush", s.push.NotifyApproval(ctx, ap.ID)
	})
}

func (s *Server) pushDeviceActive(id string) bool {
	if s.devices == nil {
		return false
	}
	s.devices.mu.Lock()
	defer s.devices.mu.Unlock()
	if s.devices.err != nil {
		return false
	}
	for _, dev := range s.devices.state.Devices {
		if dev.ID == id && !dev.Recovery && s.devices.now().Before(dev.Expires) {
			return true
		}
	}
	return false
}

func (s *Server) pushRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/push/key", s.ownerOnly(s.pushKey))
	mux.HandleFunc("POST /api/push/subscribe", s.ownerOnly(s.pushSubscribe))
	mux.HandleFunc("POST /api/push/unsubscribe", s.ownerOnly(s.pushUnsubscribe))
}

func (s *Server) pushDevice(w http.ResponseWriter, r *http.Request) string {
	if s.push == nil {
		writeError(w, 503, "phone notifications unavailable")
		return ""
	}
	if !s.browserReady(w, r) {
		return ""
	}
	s.devices.mu.Lock()
	defer s.devices.mu.Unlock()
	i, ok := s.devices.find(r)
	if !ok || s.devices.state.Devices[i].Recovery {
		writeError(w, 403, "enable notifications from a paired owner phone")
		return ""
	}
	return s.devices.state.Devices[i].ID
}

func (s *Server) pushKey(w http.ResponseWriter, r *http.Request) {
	dev := s.pushDevice(w, r)
	if dev == "" {
		return
	}
	key, err := s.push.PublicKey()
	if err != nil {
		writeError(w, 503, "phone notifications unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"public_key": key, "enabled": s.push.Enabled(dev)})
}

func (s *Server) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	dev := s.pushDevice(w, r)
	if dev == "" {
		return
	}
	var body struct {
		Subscription push.Subscription `json:"subscription"`
	}
	if !decode(w, r, &body) {
		return
	}
	// Reuse the existing fresh owner PIN ceremony, binding it to this exact
	// subscription. A host bearer token alone can never enroll a phone endpoint.
	if !s.requireOwner(w, r, gate.Action{Reason: "enable phone notifications", Key: actionKey(r, body), Fresh: true, Always: true}) {
		return
	}
	if err := s.push.Subscribe(r.Context(), dev, body.Subscription); err != nil {
		writeError(w, 400, "could not enable notifications: subscription invalid or unavailable")
		return
	}
	writeJSON(w, 201, map[string]bool{"enabled": true})
}

func (s *Server) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	dev := s.pushDevice(w, r)
	if dev == "" {
		return
	}
	if err := s.push.Unsubscribe(dev); err != nil {
		writeError(w, 503, "could not disable notifications")
		return
	}
	writeJSON(w, 200, map[string]bool{"enabled": false})
}
