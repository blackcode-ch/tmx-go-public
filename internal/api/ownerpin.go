package api

// The owner PIN (internal/ownerpin): ONE short code, the same on every machine and the same
// as the Python daemon's, for every owner step a browser takes: approving an owner
// confirmation, pairing a new device, a presence session. Agents never set, read, reset or
// submit it: every route here is the owner's (ownerOnly) or a pairing browser's, and the
// record itself goes only to the fleet's own machines (peer routes, pair-token auth).
//
// Routes (the same names as Python's Ship 1):
//   GET  /api/gate/pin                  owner: is a PIN set, how long this source must wait
//   POST /api/gate/pin {pin, old_pin}   set it: the first time only at the machine; a change
//                                       with the old PIN or at the machine (= the lost-PIN reset)
//   POST /api/gate/pin/clear            at the machine: forget every source's wrong tries
//   POST /api/gate/pin/session {pin}    a paired browser: a presence session (1 h idle, 12 h max)
//   POST /api/gate/approvals/{id}/pin   a paired browser: confirm one pending owner action
//   POST /api/gate/browser/{id}         the same (the Go UI's older name)
//   POST /api/pair/pin {pin}            a pairing browser: approve its own pairing
//   GET/POST /api/gate/pin/record       peers only: the record, newest "set" wins

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/ownerpin"
)

const (
	pinSyncFirst = 30 * time.Second
	pinSyncEvery = 5 * time.Minute
)

type pinSession struct{ start, last, pinAt time.Time }

// pinSessionOK: the device's PIN session is alive (1 h idle, 12 h max); fresh = a PIN in the
// last 5 minutes. Using it counts as activity. Caller holds devices.mu.
func (d *deviceStore) pinSessionOK(dev string, fresh bool) bool {
	p := d.pinSess[dev]
	now := d.now()
	if p == nil {
		return false
	}
	if now.Sub(p.start) >= ownerpin.SessionMax || now.Sub(p.last) >= ownerpin.SessionIdle {
		delete(d.pinSess, dev)
		return false
	}
	if fresh && now.Sub(p.pinAt) >= ownerpin.Fresh {
		return false
	}
	p.last = now
	return true
}

// pinProven records a right PIN from dev. Caller holds devices.mu.
func (d *deviceStore) pinProven(dev string) {
	now := d.now()
	p := d.pinSess[dev]
	if p == nil || now.Sub(p.start) >= ownerpin.SessionMax || now.Sub(p.last) >= ownerpin.SessionIdle {
		p = &pinSession{start: now}
		d.pinSess[dev] = p
	}
	p.last, p.pinAt = now, now
}

// atMachine: the owner on this machine itself (loopback, not proxied, the signed-in UI).
func atMachine(r *http.Request) bool {
	c, ok := caller(r)
	return ok && c.Owner && !c.Companion && c.AgentID == "" && localRequest(r) && validUISession(r.Header.Get(UISessionHeader))
}

// pinSource is who the backoff counts against: one paired device, all pairing browsers of a
// host together (a cookie is free to drop), or the local UI.
func pinSource(r *http.Request, device string) string {
	switch {
	case device != "":
		return "browser:" + requestHost(r) + ":" + device
	case localRequest(r):
		return "local:" + gateCaller(r)
	default:
		return "pair:" + requestHost(r)
	}
}

// verifyPIN checks the PIN (slow: never under devices.mu) and writes the error answer.
func (s *Server) verifyPIN(w http.ResponseWriter, source, pin string) bool {
	if s.pin == nil {
		writeError(w, 503, "the owner PIN is unavailable on this daemon")
		return false
	}
	err := s.pin.Verify(source, pin)
	var wait *ownerpin.WaitError
	switch {
	case err == nil:
		return true
	case errors.As(err, &wait):
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Wait.Seconds())+1))
		writeJSON(w, 429, map[string]any{"error": err.Error(), "wait_s": int(wait.Wait.Seconds()) + 1})
	case errors.Is(err, ownerpin.ErrBusy):
		w.Header().Set("Retry-After", "1")
		writeJSON(w, 429, map[string]any{"error": err.Error(), "wait_s": 1})
	case errors.Is(err, ownerpin.ErrWrong):
		s.deviceEvent("pin_failed", "")
		writeError(w, 403, "wrong PIN")
	case errors.Is(err, ownerpin.ErrNotSet):
		writeError(w, 409, err.Error())
	default:
		writeError(w, 503, "the owner PIN can't be read; set it again at the machine")
	}
	return false
}

type pinBody struct {
	PIN    string `json:"pin"`
	OldPIN string `json:"old_pin,omitempty"`
}

func (s *Server) pinRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/gate/pin", s.ownerOnly(s.pinStatus))
	mux.HandleFunc("POST /api/gate/pin", s.ownerOnly(s.pinSet))
	mux.HandleFunc("POST /api/gate/pin/clear", s.ownerOnly(s.pinClear))
	mux.HandleFunc("POST /api/gate/pin/session", s.ownerOnly(s.pinSessionStart))
}

func (s *Server) pinStatus(w http.ResponseWriter, r *http.Request) {
	if s.pin == nil {
		writeJSON(w, 200, map[string]any{"available": false, "set": false})
		return
	}
	v := map[string]any{"available": true, "set": s.pin.IsSet(), "at_machine": atMachine(r),
		"wait_s": int(s.pin.Wait(pinSource(r, s.deviceID(r))).Seconds())}
	if err := s.pin.Err(); err != nil {
		v["error"] = "the stored PIN can't be read; set it again at the machine"
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, v)
}

// deviceID is the request's paired device, or "".
func (s *Server) deviceID(r *http.Request) string {
	d := s.devices
	if d == nil || localHost(r.Host) {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if i, ok := d.find(r); ok {
		return d.state.Devices[i].ID
	}
	return ""
}

func (s *Server) pinSet(w http.ResponseWriter, r *http.Request) {
	var req pinBody
	if !decode(w, r, &req) {
		return
	}
	if s.pin == nil {
		writeError(w, 503, "the owner PIN is unavailable on this daemon")
		return
	}
	if !ownerpin.ValidPIN(req.PIN) {
		writeError(w, 400, ownerpin.ErrFormat.Error())
		return
	}
	here := atMachine(r)
	switch {
	case here: // the owner at the machine: first set, change and the lost-PIN reset
	case !s.pin.IsSet() || s.pin.Err() != nil:
		writeError(w, 403, "set the owner PIN at the machine itself")
		return
	case req.OldPIN == "":
		writeError(w, 403, "enter your current PIN to change it (lost it? reset it at the machine)")
		return
	default:
		if !s.verifyPIN(w, pinSource(r, s.deviceID(r)), req.OldPIN) {
			return
		}
	}
	rec, err := s.pin.Set(req.PIN)
	if err != nil {
		writeError(w, 503, "could not save the PIN")
		return
	}
	s.pin.ClearBackoff()
	s.deviceEvent("pin_set", "")
	go s.pushPIN(context.Background(), rec)
	writeJSON(w, 200, map[string]any{"set": true})
}

func (s *Server) pinClear(w http.ResponseWriter, r *http.Request) {
	if !atMachine(r) {
		writeError(w, 403, "clear wrong-PIN waits at the machine itself")
		return
	}
	if s.pin != nil {
		s.pin.ClearBackoff()
	}
	s.deviceEvent("pin_backoff_cleared", "")
	writeJSON(w, 200, map[string]bool{"cleared": true})
}

func (s *Server) pinSessionStart(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w, r) {
		return
	}
	var req pinBody
	if !decode(w, r, &req) {
		return
	}
	dev := s.deviceID(r)
	if dev == "" {
		writeError(w, 403, "pair this device first")
		return
	}
	if !s.verifyPIN(w, pinSource(r, dev), req.PIN) {
		return
	}
	d := s.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.find(r); !ok { // revoked while the PIN was checked
		writeError(w, 403, "pair this device first")
		return
	}
	d.pinProven(dev)
	writeJSON(w, 200, map[string]any{"idle_s": int(ownerpin.SessionIdle.Seconds()), "max_s": int(ownerpin.SessionMax.Seconds()), "fresh_s": int(ownerpin.Fresh.Seconds())})
}

// confirmWithPIN confirms one pending owner action (requireBrowserOwner's 428) with the PIN.
// The confirmed action must then be retried within 5 minutes, once.
func (s *Server) confirmWithPIN(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w, r) {
		return
	}
	var req pinBody
	if !decode(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	d := s.devices
	match := func() (*browserCeremony, string) {
		i, ok := d.find(r)
		if !ok {
			return nil, ""
		}
		dev := d.state.Devices[i].ID
		c := d.ceremonies[id]
		if c == nil || c.Kind != "action" || c.Host != requestHost(r) || c.Caller != gateCaller(r) || c.Device != dev || c.Confirmed {
			return nil, dev
		}
		return c, dev
	}
	d.mu.Lock()
	d.gc()
	c, dev := match()
	d.mu.Unlock()
	if c == nil {
		writeError(w, 403, "invalid or expired confirmation; try the action again")
		return
	}
	if !s.verifyPIN(w, pinSource(r, dev), req.PIN) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gc()
	if c2, _ := match(); c2 != c {
		writeError(w, 403, "invalid or expired confirmation; try the action again")
		return
	}
	c.Confirmed = true // this one action only: a presence session is POST /api/gate/pin/session
	c.Expires = d.now().Add(ownerpin.Fresh)
	s.deviceEvent("pin_verified", dev)
	writeJSON(w, 200, map[string]string{"id": id, "state": "confirmed"})
}

// pairWithPIN approves this browser's own pending pairing with the owner PIN: scan the QR,
// type the PIN, done (no code to approve at the host).
func (s *Server) pairWithPIN(w http.ResponseWriter, r *http.Request) {
	if !s.browserReady(w, r) {
		return
	}
	var req pinBody
	if !decode(w, r, &req) {
		return
	}
	d := s.devices
	d.mu.Lock()
	d.gc()
	code, p := d.pairFor(r)
	d.mu.Unlock()
	if p == nil {
		writeError(w, 400, "pairing expired; start again")
		return
	}
	if !s.verifyPIN(w, pinSource(r, ""), req.PIN) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gc()
	if c2, p2 := d.pairFor(r); p2 != p || c2 != code {
		writeError(w, 400, "pairing expired; start again")
		return
	}
	p.Approved = true
	s.deviceEvent("pair_approved_pin", "")
	writeJSON(w, 200, map[string]bool{"approved": true})
}

// ---- sync between the fleet's machines (newest "set" wins) ----

type pinRecordBody struct {
	Record *ownerpin.Record `json:"record"`
}

func (s *Server) peerPinRecord(w http.ResponseWriter, r *http.Request) {
	if s.pin == nil {
		writeJSON(w, 200, pinRecordBody{})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, pinRecordBody{Record: s.pin.Record()})
}

func (s *Server) peerPinAdopt(w http.ResponseWriter, r *http.Request) {
	var req pinRecordBody
	if !decode(w, r, &req) {
		return
	}
	if s.pin == nil || req.Record == nil {
		writeError(w, 400, "no record")
		return
	}
	ok, err := s.pin.Adopt(*req.Record)
	if err != nil {
		writeError(w, 400, "invalid PIN record")
		return
	}
	if ok {
		s.log.Info("owner PIN adopted from a peer", "peer", peerOf(r).name)
	}
	writeJSON(w, 200, map[string]bool{"adopted": ok})
}

func (s *Server) pushPIN(ctx context.Context, rec ownerpin.Record) {
	if s.peers == nil {
		return
	}
	for _, host := range s.peers.Names() {
		if err := s.peers.Call(ctx, host, http.MethodPost, "/api/gate/pin/record", pinRecordBody{Record: &rec}, nil); err != nil {
			s.log.Warn("owner PIN push failed (the peer pulls it within 5 min)", "peer", host, "err", err)
		}
	}
}

func (s *Server) pullPIN(ctx context.Context) {
	if s.peers == nil || s.pin == nil {
		return
	}
	for _, host := range s.peers.Names() {
		var res pinRecordBody
		if err := s.peers.Call(ctx, host, http.MethodGet, "/api/gate/pin/record", nil, &res); err != nil || res.Record == nil {
			continue
		}
		if ok, _ := s.pin.Adopt(*res.Record); ok {
			s.log.Info("owner PIN adopted from a peer", "peer", host)
		}
	}
}

func (s *Server) pinSyncLoop(ctx context.Context) {
	if s.peers == nil || s.pin == nil || len(s.peers.Names()) == 0 {
		return
	}
	t := time.NewTimer(pinSyncFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pullPIN(ctx)
			t.Reset(pinSyncEvery)
		}
	}
}
