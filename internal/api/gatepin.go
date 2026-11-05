package api

// The owner PIN at the machine itself (#187, architect 3 Nov 17:21): the PIN is THE owner
// factor on every device. Paired browsers already answer owner confirmations with it
// (requireBrowserOwner + POST /api/gate/approvals/{id}/pin); these routes let the local page
// do the same, and make Windows Hello an optional shortcut there.
//
//   POST /api/gate/confirmations/{id}/pin {pin}  the owner on this machine: answer one pending
//                                                gate confirmation with the PIN
//   PUT  /api/gate/hello {enabled}               the owner on this machine: also offer the OS
//                                                prompt (Windows Hello / Touch ID); off needs a PIN

import (
	"errors"
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/gate"
)

func (s *Server) gatePINRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/gate/confirmations/{id}/pin", s.ownerOnly(s.confirmGateWithPIN))
	mux.HandleFunc("PUT /api/gate/hello", s.ownerOnly(s.putHelloShortcut))
}

// localOwner: the owner's own token on a loopback, unproxied request. Not only the signed-in UI
// (atMachine): any local owner client must be able to answer when Windows Hello is off.
func localOwner(r *http.Request) bool {
	c, ok := caller(r)
	return ok && c.Owner && !c.Companion && c.AgentID == "" && c.Peer == "" && localRequest(r)
}

func (s *Server) confirmGateWithPIN(w http.ResponseWriter, r *http.Request) {
	if !localOwner(r) {
		writeError(w, http.StatusForbidden, "answer with the PIN on this machine; a paired device uses its own confirmation")
		return
	}
	if s.gate == nil {
		writeError(w, http.StatusNotImplemented, "this daemon has no owner gate")
		return
	}
	var req pinBody
	if !decode(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	// Check the confirmation first, so a stale ID never costs a PIN try.
	if c, ok := s.gate.Get(id); !ok || c.State != gate.StatePending || !c.PIN || !s.gate.OwnsConfirmation(id, gateCaller(r)) {
		writeError(w, http.StatusForbidden, "invalid or expired confirmation; try the action again")
		return
	}
	if !s.verifyPIN(w, pinSource(r, ""), req.PIN) {
		return
	}
	if err := s.gate.ConfirmWithPIN(id, gateCaller(r)); err != nil {
		writeError(w, http.StatusForbidden, "invalid or expired confirmation; try the action again")
		return
	}
	s.deviceEvent("pin_verified", "")
	c, _ := s.gate.Get(id)
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) putHelloShortcut(w http.ResponseWriter, r *http.Request) {
	if !localOwner(r) {
		writeError(w, http.StatusForbidden, "change Windows Hello on this machine")
		return
	}
	if s.gate == nil {
		writeError(w, http.StatusNotImplemented, "this daemon has no owner gate")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	on := *req.Enabled
	if !on && (s.pin == nil || !s.pin.IsSet()) {
		writeError(w, http.StatusConflict, gate.ErrNeedPIN.Error())
		return
	}
	if on != s.gate.HelloShortcut() {
		reason := "stop offering Windows Hello at this machine (the PIN stays)"
		if on {
			reason = "also accept Windows Hello at this machine"
		}
		if !s.requireOwner(w, r, gate.Action{Reason: reason, Key: actionKey(r, req), Always: true, Fresh: true}) {
			return
		}
		if err := s.gate.SetHelloShortcut(on); err != nil {
			if errors.Is(err, gate.ErrNeedPIN) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, s.gateStatus(r))
}
