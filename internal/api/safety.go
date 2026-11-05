package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// ConfirmHeader names a confirmation the daemon verified (the retry after a 428). It is a
// reference to the daemon's own record, bound to one request and usable once, never a proof.
const (
	ConfirmHeader   = "X-Tmx-Confirmation"
	UISessionHeader = "X-Tmx-UI-Session"
	GraceHeader     = "X-Tmx-Grace"
	ConfirmParam    = "confirmation" // the same as a query parameter
)

func (s *Server) safetyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/safety", s.ownerOnly(s.getSafety))
	mux.HandleFunc("PUT /api/safety/guard", s.ownerOnly(s.putGuard))
	mux.HandleFunc("DELETE /api/safety/guard/always/{id}", s.ownerOnly(s.revokeAlways))
	mux.HandleFunc("PUT /api/safety/guard/rules/{id}", s.ownerOnly(s.putRuleState))
	mux.HandleFunc("GET /api/gate", s.ownerOnly(s.getGate))
	mux.HandleFunc("PUT /api/gate", s.ownerOnly(s.putGate))
	mux.HandleFunc("PUT /api/gate/grace", s.ownerOnly(s.putGrace))
	mux.HandleFunc("POST /api/gate/lock", s.ownerOnly(s.lockGate))
	mux.HandleFunc("GET /api/gate/confirmations/{id}", s.ownerOnly(s.getConfirmation))
	mux.HandleFunc("DELETE /api/gate/confirmations/{id}", s.ownerOnly(s.cancelConfirmation))

	mux.HandleFunc("POST /api/guard/holds", s.agentOnly(s.postHold))
	mux.HandleFunc("GET /api/guard/holds/{id}", s.agentOnly(s.waitHold))
	mux.HandleFunc("DELETE /api/guard/holds/{id}", s.agentOnly(s.cancelHold))
	mux.HandleFunc("POST /api/guard/holds/{id}/withdraw", s.agentOnly(s.withdrawHold))
	mux.HandleFunc("POST /api/guard/holds/withdraw", s.agentOnly(s.withdrawHolds))
	mux.HandleFunc("POST /api/guard/blocks", s.agentOnly(s.postBlock))
	mux.HandleFunc("GET /api/guard/policy", s.agentOnly(s.getGuardPolicy))
}

// getGuardPolicy exposes only the host browser capability to authenticated hooks.
// Mutations remain on the owner-only, owner-gated guard settings route.
// It also gives the owner's rule switches, which the hook applies to each call.
func (s *Server) getGuardPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		AllowBrowserAutomation bool                     `json:"allow_browser_automation"`
		States                 map[string]guard.Outcome `json:"states"`
	}{s.f.GuardSettings().AllowBrowserAutomation, s.f.GuardStates()})
}

// RuleView is one row of Settings > Safety > Rules: the rule, its current state and whether it
// has a switch (fixed carve-outs do not).
type RuleView struct {
	guard.Rule
	State      guard.Outcome `json:"state"`
	Switchable bool          `json:"switchable"`
}

func (s *Server) ruleViews() []RuleView {
	states := s.f.GuardStates()
	var out []RuleView
	for _, r := range guard.Rules() {
		out = append(out, RuleView{Rule: r, State: states[r.ID], Switchable: true})
	}
	for _, r := range guard.FixedRules() {
		out = append(out, RuleView{Rule: r, State: r.Outcome})
	}
	return out
}

// putRuleState: PUT /api/safety/guard/rules/{id} {state}. The owner only, with a fresh proof
// every time (the PIN on a phone, at the machine the PIN or Windows Hello), whichever way it goes.
func (s *Server) putRuleState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		State guard.Outcome `json:"state"`
	}
	if !decode(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	rule, ok := guard.RuleByID(id)
	if !ok || rule.Fixed || !guard.ValidState(req.State) {
		writeError(w, http.StatusBadRequest, "unknown or fixed rule, or a state other than block, hold or allow")
		return
	}
	if !s.requireOwner(w, r, gate.Action{Reason: "set the guard rule “" + rule.Name + "” to " + string(req.State),
		Key: actionKey(r, map[string]any{"id": id, "state": req.State}), Always: true, Fresh: true}) {
		return
	}
	c, _ := caller(r)
	out, err := s.f.SetRuleState(c, id, req.State)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"guard": out, "rules": s.ruleViews()})
}

func (s *Server) agentOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, _ := caller(r); c.Owner {
			writeError(w, http.StatusForbidden, "only agents use this route")
			return
		}
		h(w, r)
	}
}

// RequireOwner asks the owner gate before a sensitive action. It returns true when the
// request may go ahead (the gate is off, or the request names a confirmed proof for exactly
// this action). Otherwise it has answered: 428 with the confirmation to wait for (the daemon
// is prompting the owner on this machine), or why it cannot ask. reason completes "tmx is
// trying to ..."; payload is the decoded request body (it binds the proof to it).
func (s *Server) RequireOwner(w http.ResponseWriter, r *http.Request, reason string, payload any) bool {
	return s.requireOwner(w, r, gate.Action{Reason: reason, Key: actionKey(r, payload)})
}

func (s *Server) requireOwner(w http.ResponseWriter, r *http.Request, a gate.Action) bool {
	c, ok := caller(r)
	if !ok || !c.Owner || c.Companion || c.AgentID != "" {
		writeError(w, http.StatusForbidden, "only the owner can do this")
		return false
	}
	if s.gate == nil {
		if a.Always {
			writeError(w, http.StatusNotImplemented, "this daemon has no owner gate")
			return false
		}
		return true
	}
	// Only the owner's own token may pass on the grace window. Gated routes are owner-only
	// anyway; this keeps it so if one ever is not.
	a.Owner = true
	a.Caller = gateCaller(r)
	a.UI = validUISession(r.Header.Get(UISessionHeader))
	a.Session = r.Header.Get(GraceHeader)
	id := r.Header.Get(ConfirmHeader)
	if id == "" {
		id = r.URL.Query().Get(ConfirmParam) // the desktop bridge cannot set headers
	}
	if !localHost(r.Host) && (s.gate.Enabled() || a.Always) {
		return s.requireBrowserOwner(w, r, a, id)
	}
	err := s.gate.Require(a, id)
	var need *gate.NeedConfirmation
	switch {
	case err == nil:
		return true
	case errors.As(err, &need):
		writeJSON(w, http.StatusPreconditionRequired, map[string]any{
			"error":        "Owner confirmation needed: " + need.Confirmation.MethodLabel + " on this machine.",
			"confirmation": need.Confirmation,
		})
	case errors.Is(err, gate.ErrBusy):
		writeError(w, http.StatusConflict, "another confirmation is waiting on this machine; answer or cancel it first")
	case errors.Is(err, gate.ErrTooMany):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, gate.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, err.Error()+". Recovery: "+s.gate.Recovery())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
	return false
}

func actionKey(r *http.Request, payload any) string {
	h := sha256.New()
	h.Write([]byte(r.Method + " " + r.URL.Path + "\n"))
	if payload != nil {
		b, _ := json.Marshal(payload)
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// SafetyInfo is GET /api/safety.
type SafetyInfo struct {
	Guard  guard.Settings `json:"guard"`
	Rules  []RuleView     `json:"rules"`
	Gate   gate.Status    `json:"gate"`
	Levels []model.Level  `json:"levels"`
}

func (s *Server) gateStatus(r *http.Request) gate.Status {
	if s.gate == nil {
		return gate.Status{Method: "none", MethodLabel: "not available", Unavailable: "this daemon has no owner gate", Gated: []string{}, GraceSteps: []int{}}
	}
	st := s.gate.StatusForSession(gateCaller(r), r.Header.Get(GraceHeader))
	if !localHost(r.Host) {
		st.Method, st.MethodLabel, st.GraceUntil = "pin", "Your owner PIN", 0
		st.HelloShortcut = false // Windows Hello is offered only at the machine itself (#187)
		st.Available = s.pin != nil && s.pin.IsSet() && s.pin.Err() == nil && s.devices != nil
		st.Unavailable = ""
		if !st.Available {
			st.Unavailable = "Set your owner PIN at the machine first."
		}
	}
	return st
}

func (s *Server) getSafety(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, SafetyInfo{Guard: s.f.GuardSettings(), Rules: s.ruleViews(), Gate: s.gateStatus(r),
		Levels: []model.Level{model.LevelHead, model.LevelLead, model.LevelWorker}})
}

func (s *Server) putGuard(w http.ResponseWriter, r *http.Request) {
	var next guard.Settings
	if !decode(w, r, &next) {
		return
	}
	if next.OffFor == nil {
		next.OffFor = []string{}
	}
	if s.f.GuardSettings().Weaker(next) && !s.RequireOwner(w, r, "relax agent safety settings", next) {
		return
	}
	c, _ := caller(r)
	out, err := s.f.SetGuardSettings(c, next)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getGate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.gateStatus(r))
}

func (s *Server) putGate(w http.ResponseWriter, r *http.Request) {
	if !localRequest(r) {
		writeError(w, http.StatusForbidden, "change the owner gate on the host")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	if s.gate == nil {
		writeError(w, http.StatusNotImplemented, "this daemon has no owner gate")
		return
	}
	if req.Enabled != s.gate.Enabled() {
		// Turning it on needs a proof even though it is off: whoever enables it must be able to pass it.
		reason := "turn off the owner gate"
		if req.Enabled {
			reason = "turn on the owner gate"
		}
		// Fresh: changing the gate itself asks also inside the grace window.
		if !s.requireOwner(w, r, gate.Action{Reason: reason, Key: actionKey(r, req), Always: req.Enabled, Fresh: true}) {
			return
		}
		if err := s.gate.SetEnabled(req.Enabled); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.gateStatus(r))
}

// putGrace: PUT /api/gate/grace {grace_minutes}: how long the gate stops asking after a proof
// (one of gate.GraceSteps; 0 = off). A longer window, turning it on from off included, always
// needs a fresh proof: inside the window, and while the gate is off too (the admin handover
// asks then and uses the window). A shorter one or off never asks.
func (s *Server) putGrace(w http.ResponseWriter, r *http.Request) {
	if !localRequest(r) {
		writeError(w, http.StatusForbidden, "change the owner gate on the host")
		return
	}
	var req struct {
		GraceMinutes *int `json:"grace_minutes"`
	}
	if !decode(w, r, &req) {
		return
	}
	if s.gate == nil {
		writeError(w, http.StatusNotImplemented, "this daemon has no owner gate")
		return
	}
	if req.GraceMinutes == nil || !gate.ValidGrace(*req.GraceMinutes) {
		writeError(w, http.StatusBadRequest, gate.ErrGraceStep.Error()+" (grace_minutes: 0, 5, 15, 30, 60, 120, 240, 480 or 1440)")
		return
	}
	n := *req.GraceMinutes
	if n > s.gate.GraceMinutes() {
		reason := "stop asking for " + gate.GraceLabel(n) + " after each confirmation"
		if !s.requireOwner(w, r, gate.Action{Reason: reason, Key: actionKey(r, req), Always: true, Fresh: true}) {
			return
		}
	}
	if err := s.gate.SetGrace(n); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.gateStatus(r))
}

// lockGate: POST /api/gate/lock ends the grace window now (Settings "Lock now", tmx gate lock).
// It only protects more, so it never asks.
func (s *Server) lockGate(w http.ResponseWriter, r *http.Request) {
	if s.gate == nil {
		writeError(w, http.StatusNotImplemented, "this daemon has no owner gate")
		return
	}
	s.gate.Lock()
	writeJSON(w, http.StatusOK, s.gateStatus(r))
}

// wireGate publishes every change of the gate (on, off, the grace window opened, changed or
// locked) as gate.updated so every client's Settings and confirm sheet follow. The event has no
// data: agents see the bus too, and GET /api/gate is the owner's.
func (s *Server) wireGate() {
	if s.gate == nil || s.f == nil {
		return
	}
	s.gate.SetNotify(func(gate.Status) { s.f.Publish(model.BusEvent{Type: gate.EvUpdated}) })
}

func waitParam(r *http.Request, max time.Duration) time.Duration {
	ms, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	return min(time.Duration(max0(ms))*time.Millisecond, max)
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func (s *Server) getConfirmation(w http.ResponseWriter, r *http.Request) {
	if s.gate == nil {
		writeError(w, http.StatusNotFound, "no such confirmation")
		return
	}
	if !s.gate.OwnsConfirmation(r.PathValue("id"), gateCaller(r)) {
		writeError(w, http.StatusNotFound, "no such confirmation")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), waitParam(r, 30*time.Second))
	defer cancel()
	c, ok := s.gate.Wait(ctx, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such confirmation")
		return
	}
	_, credential, _ := s.gate.SessionProof(c.ID, gateCaller(r))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		gate.Confirmation
		Grace string `json:"grace_credential,omitempty"`
	}{c, credential})
}

func (s *Server) cancelConfirmation(w http.ResponseWriter, r *http.Request) {
	if s.gate == nil || !s.gate.OwnsConfirmation(r.PathValue("id"), gateCaller(r)) || !s.gate.Cancel(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "no such confirmation")
		return
	}
	c, _ := s.gate.Get(r.PathValue("id"))
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) postHold(w http.ResponseWriter, r *http.Request) {
	var req fleet.HoldRequest
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	ap, err := s.f.Hold(c, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ap)
}

func (s *Server) waitHold(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	ap, err := s.f.WaitHold(r.Context(), c, r.PathValue("id"), waitParam(r, 60*time.Second))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ap)
}

func (s *Server) cancelHold(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	ap, err := s.f.CancelHold(c, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ap)
}

// withdrawHold: the agent takes back one of its own pending holds (it won't run the command).
func (s *Server) withdrawHold(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	ap, err := s.f.WithdrawHold(c, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ap)
}

// withdrawHolds: the agent takes back all its pending holds.
func (s *Server) withdrawHolds(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	out := []model.Approval{}
	for _, ap := range s.f.PendingHolds(c) {
		if done, err := s.f.WithdrawHold(c, ap.ID); err == nil {
			out = append(out, done)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) postBlock(w http.ResponseWriter, r *http.Request) {
	var req fleet.HoldRequest
	if !decode(w, r, &req) {
		return
	}
	c, _ := caller(r)
	if err := s.f.ReportBlock(c, req); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// gateDecide asks the gate before the owner allows a guard hold. Denying never needs a proof.
func (s *Server) gateDecide(w http.ResponseWriter, r *http.Request, id string, req fleet.DecideRequest) bool {
	ap, err := s.f.ApprovalByID(id)
	if err == nil && ap.Tool == fleet.ToolProcessStart && req.Decision == "allow" {
		return s.RequireOwner(w, r, "start the agent's requested command", map[string]any{"id": id, "req": req})
	}
	if err != nil || ap.Kind != model.ApprovalGuard || (req.Decision != "allow" && req.Decision != fleet.DecisionAllowAlways) {
		return true
	}
	var info model.GuardHold
	_ = json.Unmarshal(ap.Input, &info)
	what := info.Match
	if what == "" {
		what = info.Command
	}
	name := ap.AgentID
	if a, err := s.f.Agent(ap.AgentID); err == nil {
		name = a.Name
	}
	// The owner, 3 Nov 22:30: no PIN to allow something already allowed once (same rule, tool and
	// exact command). The card still waits for the owner's own tap; only the proof is skipped.
	if req.Decision == "allow" && s.f.AllowedBefore(ap) {
		return true
	}
	if req.Decision == fleet.DecisionAllowAlways {
		// A standing allow outlives this hold: always a fresh proof (Windows Hello or the PIN),
		// even with the gate off or inside the grace window. Only the owner's own session gets here.
		who := "any agent"
		if req.Scope == "agent" {
			who = "agents like " + name
		}
		return s.requireOwner(w, r, gate.Action{Reason: "always allow “" + short(info.Command, 70) + "” for " + who,
			Key: actionKey(r, map[string]any{"id": id, "req": req}), Always: true, Fresh: true})
	}
	return s.RequireOwner(w, r, "allow “"+short(what, 70)+"” for "+name, map[string]any{"id": id, "req": req})
}

// revokeAlways: DELETE /api/safety/guard/always/{id} ends one standing allow. Like setting one,
// it needs a fresh owner proof.
func (s *Server) revokeAlways(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireOwner(w, r, gate.Action{Reason: "revoke an Allow always of the guard",
		Key: actionKey(r, map[string]string{"id": id}), Always: true, Fresh: true}) {
		return
	}
	c, _ := caller(r)
	out, err := s.f.RevokeAlways(c, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// gatePolicy asks the gate when a level newly gets bypassPermissions.
func (s *Server) gatePolicy(w http.ResponseWriter, r *http.Request, p model.Policy) bool {
	cur := s.f.Policy()
	var names []string
	for _, lv := range []model.Level{model.LevelHead, model.LevelLead, model.LevelWorker} {
		if p.Levels[lv].PermissionMode == "bypassPermissions" && cur.Levels[lv].PermissionMode != "bypassPermissions" {
			names = append(names, string(lv))
		}
	}
	if len(names) == 0 {
		return true
	}
	return s.RequireOwner(w, r, "let "+strings.Join(names, ", ")+" agents bypass permissions", p)
}

// gateProfile asks the gate when a profile newly gets bypassPermissions (a new profile, or an
// existing one whose mode was something else): profiles grant it just like the policy does.
func (s *Server) gateProfile(w http.ResponseWriter, r *http.Request, slug string, p model.Profile) bool {
	if p.PermissionMode != "bypassPermissions" {
		return true
	}
	if slug != "" {
		if cur, err := s.f.GetProfile(slug); err == nil && cur.PermissionMode == "bypassPermissions" {
			return true
		}
	}
	name := p.Name
	if name == "" {
		name = slug
	}
	return s.RequireOwner(w, r, "let "+short(name, 60)+" agents bypass permissions", p)
}

func (s *Server) gateDeleteProject(w http.ResponseWriter, r *http.Request, id string) bool {
	p, err := s.f.Project(id)
	if err != nil {
		return true // the delete answers 404 itself
	}
	return s.RequireOwner(w, r, "delete the project "+short(p.Name, 60)+" and remove its agents", map[string]string{"id": p.ID})
}

// The nonce identifies an in-memory UI instance. It grants no authority before proof.
func validUISession(s string) bool { b, err := hex.DecodeString(s); return err == nil && len(b) == 32 }
func gateCaller(r *http.Request) string {
	nonce := r.Header.Get(UISessionHeader)
	if !validUISession(nonce) {
		nonce = ""
	}
	sum := sha256.Sum256([]byte(r.Header.Get("Authorization") + "\n" + nonce + "\n" + requestHost(r) + "\n" + cookieValue(r, deviceCookie)))
	return hex.EncodeToString(sum[:])
}
