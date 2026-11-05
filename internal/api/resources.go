package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/slots"
)

// Resource control routes (DESIGN.md "Resources").
func (s *Server) resourceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/resources", s.withResources(s.getResources))
	mux.HandleFunc("PUT /api/resources/config", s.ownerOnly(s.withResources(s.putResourceConfig)))
	mux.HandleFunc("POST /api/resources/exempt", s.ownerOnly(s.withResources(s.exempt)))
	mux.HandleFunc("POST /api/agents/{id}/resume", s.act((*fleet.Fleet).ResumeAgent))

	mux.HandleFunc("GET /api/slots", s.withSlots(s.listSlots))
	mux.HandleFunc("POST /api/slots", s.withSlots(s.acquireSlot))
	mux.HandleFunc("POST /api/slots/{id}/wait", s.withSlots(s.waitSlot))
	mux.HandleFunc("POST /api/slots/{id}/renew", s.withSlots(s.renewSlot))
	mux.HandleFunc("DELETE /api/slots/{id}", s.withSlots(s.releaseSlot))
}

func (s *Server) withResources(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.res == nil {
			writeError(w, http.StatusNotImplemented, "resource control is not running in this daemon")
			return
		}
		h(w, r)
	}
}

func (s *Server) withSlots(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.slots == nil {
			writeError(w, http.StatusNotImplemented, "slots are not running in this daemon")
			return
		}
		h(w, r)
	}
}

// start is POST /api/agents/{id}/start; ?force=1 starts anyway (owner), ?no_wait=1 refuses
// instead of queueing. The same fields are accepted as a JSON body.
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	var o fleet.StartOptions
	if r.ContentLength > 0 && !decode(w, r, &o) {
		return
	}
	q := r.URL.Query()
	o.Force = o.Force || truthy(q.Get("force"))
	o.NoWait = o.NoWait || truthy(q.Get("no_wait"))
	c, _ := caller(r)
	a, err := s.f.StartAgentWith(c, r.PathValue("id"), o)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func truthy(v string) bool { return v == "1" || strings.EqualFold(v, "true") }

func (s *Server) getResources(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.res.View())
}

func (s *Server) putResourceConfig(w http.ResponseWriter, r *http.Request) {
	var c config.Resources
	if !decode(w, r, &c) {
		return
	}
	out, err := s.res.SetConfig(c)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// exempt is POST /api/resources/exempt {agent, hours}: no brakes for that agent for hours
// (0 = lift the exemption).
func (s *Server) exempt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent string  `json:"agent"`
		Hours float64 `json:"hours"`
	}
	if !decode(w, r, &req) {
		return
	}
	a, err := s.f.Agent(req.Agent)
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.Hours < 0 || req.Hours > 24*30 {
		writeError(w, http.StatusBadRequest, "an exemption lasts from 0 to 720 hours")
		return
	}
	var until int64
	if req.Hours > 0 {
		until = time.Now().Add(time.Duration(req.Hours * float64(time.Hour))).UnixMilli()
	}
	out, err := s.res.SetExempt(a.Name, until)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// holder maps the caller to a slot holder (agents by name, the owner as itself).
func (s *Server) holder(r *http.Request) (slots.Holder, error) {
	c, _ := caller(r)
	if c.Owner {
		return slots.Holder{}, nil
	}
	a, err := s.f.Agent(c.AgentID)
	if err != nil {
		return slots.Holder{}, err
	}
	return slots.Holder{AgentID: a.ID, Name: a.Name}, nil
}

func writeSlotErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, slots.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, slots.ErrForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, slots.ErrUnknownKind):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, slots.ErrTooMany):
		writeError(w, http.StatusTooManyRequests, err.Error())
	default:
		writeErr(w, err)
	}
}

func (s *Server) listSlots(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.slots.View())
}

func (s *Server) acquireSlot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    string `json:"kind"`
		Command string `json:"command"`
		PID     int    `json:"pid"`
	}
	if !decode(w, r, &req) {
		return
	}
	h, err := s.holder(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	l, err := s.slots.Acquire(req.Kind, h, req.Command, req.PID)
	if err != nil {
		writeSlotErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

// waitSlot renews a lease and waits (long poll, ?timeout= seconds, default 15, at most 30)
// until it is held.
func (s *Server) waitSlot(w http.ResponseWriter, r *http.Request) {
	h, err := s.holder(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	secs, _ := strconv.Atoi(r.URL.Query().Get("timeout"))
	if secs <= 0 {
		secs = 15
	}
	l, err := s.slots.Wait(r.Context(), r.PathValue("id"), h, time.Duration(min(secs, 30))*time.Second)
	if err != nil {
		writeSlotErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) renewSlot(w http.ResponseWriter, r *http.Request) {
	h, err := s.holder(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	l, err := s.slots.Renew(r.PathValue("id"), h)
	if err != nil {
		writeSlotErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) releaseSlot(w http.ResponseWriter, r *http.Request) {
	h, err := s.holder(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.slots.Release(r.PathValue("id"), h); err != nil {
		writeSlotErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
