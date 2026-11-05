package api

import (
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func (s *Server) messagePinRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/message-pins", func(w http.ResponseWriter, r *http.Request) {
		c, _ := caller(r)
		pins, err := s.f.MessagePins(c)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, pins)
	})
	mux.HandleFunc("POST /api/message-pins", s.ownerOnly(func(w http.ResponseWriter, r *http.Request) {
		var p model.MessagePin
		if !decode(w, r, &p) {
			return
		}
		c, _ := caller(r)
		if err := s.f.PinMessage(c, p); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("PATCH /api/message-pins/{id}", s.ownerOnly(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			State string `json:"state"`
		}
		if !decode(w, r, &p) {
			return
		}
		c, _ := caller(r)
		if err := s.f.SetMessagePinState(c, r.PathValue("id"), p.State); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
}
