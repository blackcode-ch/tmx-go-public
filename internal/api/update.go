package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

// Updates (DESIGN.md "Updates"): owner only. Without an updater every route answers 501.
// Changing the settings or the token and installing by hand are behind the owner gate once it
// is on (DESIGN.md "Safety"); reading and checking are not. The background updater installs
// without any request, so automatic updates never wait for a confirmation.
func (s *Server) updateRoutes(mux *http.ServeMux) {
	o := func(h http.HandlerFunc) http.HandlerFunc { return s.ownerOnly(s.withUpdater(h)) }
	mux.HandleFunc("GET /api/update", o(s.getUpdate))
	mux.HandleFunc("PUT /api/update", o(s.putUpdate))
	mux.HandleFunc("POST /api/update/check", o(s.checkUpdate))
	mux.HandleFunc("POST /api/update/install", o(s.installUpdate))
	mux.HandleFunc("PUT /api/update/token", o(s.putUpdateToken))
	mux.HandleFunc("GET /api/update/keys", o(s.getUpdateKeys))
	mux.HandleFunc("PUT /api/update/keys", o(s.putUpdateKeys))
	mux.HandleFunc("PUT /api/update/drill", o(s.putUpdateDrill))
}

// putUpdateDrill arms the forced-rollback drill {arm: true} (or takes it back): the next install
// is rolled back on purpose once healthy, restoring the previous binary AND database. Always a
// fresh owner proof, as a re-pin: it is a deliberate rollback of the machine.
func (s *Server) putUpdateDrill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arm bool `json:"arm"`
	}
	if !decode(w, r, &req) {
		return
	}
	reason := "take back the update rollback drill"
	if req.Arm {
		reason = "arm an update rollback drill: the next update is rolled back on purpose once healthy (previous binary and database come back; changes since the update are lost)"
	}
	if !s.requireOwner(w, r, gate.Action{Reason: reason, Key: actionKey(r, req), Always: true, Fresh: true}) {
		return
	}
	st, err := s.updater.ArmDrill(req.Arm)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) withUpdater(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.updater == nil {
			writeError(w, http.StatusNotImplemented, "updates are not available")
			return
		}
		h(w, r)
	}
}

func (s *Server) getUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.updater.Status())
}

// PUT /api/update {auto?, channel?}; gated. A request naming neither changes nothing: it
// neither asks nor writes.
func (s *Server) putUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Auto    *bool   `json:"auto"`
		Channel *string `json:"channel"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Auto == nil && req.Channel == nil {
		writeJSON(w, http.StatusOK, s.updater.Status())
		return
	}
	if req.Auto != nil && *req.Auto {
		// v1: a lead installs each machine (staged rollout); config forces auto off at every load,
		// so saying yes here would only be undone silently.
		writeError(w, http.StatusBadRequest, "automatic updates are off in this version: install a release with tmx update now (or POST /api/update/install)")
		return
	}
	var what []string
	if req.Auto != nil {
		what = append(what, "turn automatic updates "+map[bool]string{true: "on", false: "off"}[*req.Auto])
	}
	if req.Channel != nil {
		switch *req.Channel {
		case config.ChannelStable:
			what = append(what, "take updates from stable fleet releases")
		case config.ChannelLatest:
			what = append(what, "take updates from fleet releases including prereleases")
		default: // refused before anyone is asked
			writeError(w, http.StatusBadRequest, `the channel is "stable" or "latest"`)
			return
		}
	}
	if !s.RequireOwner(w, r, strings.Join(what, " and "), req) {
		return
	}
	st, err := s.updater.Configure(req.Auto, req.Channel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// POST /api/update/check: asks GitHub now (up to 30 s). Not gated: it only looks (with
// automatic updates on, this action only discovers releases and never installs).
func (s *Server) checkUpdate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.updater.Check(ctx))
}

// POST /api/update/install {now, tag?}: downloads and installs the newest release, or exactly
// tag (a published fleet release on this channel, newer than the running version; checked
// before answering: 409 if not); now = without waiting for the agents to be idle (a turn in
// progress is cut off). Gated. Progress: GET or the "update.status" event.
func (s *Server) installUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Now bool   `json:"now"`
		Tag string `json:"tag,omitempty"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	what := "the newest release"
	if req.Tag != "" {
		if v, ok := update.ParseVersion(req.Tag); !ok || v.Ahead != 0 {
			writeError(w, http.StatusBadRequest, "not a fleet release tag: "+req.Tag)
			return
		}
		what = "the release " + req.Tag
	}
	reason := "install " + what + " once no agent is busy"
	if req.Now {
		reason = "install " + what + " now and restart, cutting off agents' steps in progress"
	}
	if !s.RequireOwner(w, r, reason, req) {
		return
	}
	if req.Tag != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		st, err := s.updater.InstallTag(ctx, req.Tag, req.Now)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, st)
		return
	}
	s.updater.Install(req.Now)
	writeJSON(w, http.StatusAccepted, s.updater.Status())
}

// PUT /api/update/token {token}: the GitHub token for a private release repository ("" removes
// it). Gated; the token binds the confirmation to this request (the action key is a digest of
// the body) but is never part of the reason the prompt shows.
func (s *Server) putUpdateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &req) {
		return
	}
	reason := "save a GitHub token for updates"
	if strings.TrimSpace(req.Token) == "" {
		reason = "remove the GitHub token for updates"
	}
	if !s.RequireOwner(w, r, reason, req) {
		return
	}
	if err := s.updater.SetToken(req.Token); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.updater.Status())
}

// GET /api/update/keys: the pinned release keys (public keys only), the require policy and the
// last applied rotation serial.
func (s *Server) getUpdateKeys(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.updater.Keys())
}

// PUT /api/update/keys {fleet_keys, require}: the owner's re-pin (DESIGN.md "Updates", lost or
// compromised key), replacing the whole set. It always needs a fresh proof of the owner on this
// machine: also while the gate is off and inside the grace window, and a daemon without a gate
// refuses it, since whoever can pin a key can install any binary. An invalid set is refused
// before anyone is asked.
func (s *Server) putUpdateKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FleetKeys []config.FleetKey `json:"fleet_keys"`
		Require   []string          `json:"require"`
	}
	if !decode(w, r, &req) {
		return
	}
	if _, err := (config.UpdateConfig{}).WithPins(req.FleetKeys, req.Require); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cur := map[string]bool{}
	for _, k := range s.updater.Keys().FleetKeys {
		cur[k.ID+" "+keyFingerprint(k)] = true
	}
	var pin, keep []string
	for _, k := range req.FleetKeys {
		d := k.ID + " " + keyFingerprint(k)
		if cur[d] {
			keep = append(keep, d)
			delete(cur, d)
		} else {
			pin = append(pin, d)
		}
	}
	var drop []string
	for d := range cur {
		drop = append(drop, d)
	}
	sort.Strings(drop)
	reason := "replace the keys updates are trusted with: pin " + list(pin) + ", drop " + list(drop) + ", keep " + list(keep)
	if !s.requireOwner(w, r, gate.Action{Reason: reason, Key: actionKey(r, req), Always: true, Fresh: true}) {
		return
	}
	got, err := s.updater.SetKeys(req.FleetKeys, req.Require)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// keyFingerprint is the short form the tmx-go page publishes: hex sha256(raw key)[:12].
func keyFingerprint(k config.FleetKey) string {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(k.Key))
	if err != nil {
		return "?"
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])[:12]
}

func list(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}
