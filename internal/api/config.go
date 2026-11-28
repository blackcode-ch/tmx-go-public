package api

import (
	"net/http"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// ConfigView is the small subset of config.json the UI may read and edit.
type ConfigView struct {
	HeadHarness        string `json:"head_harness"`
	AgyBackgroundUsage *bool  `json:"agy_background_usage,omitempty"`
	// OnboardedAt: when the owner finished or closed the first-run setup (Unix ms; 0 = never, and
	// the computer's window shows it). In a PUT any other number means "now" (the daemon's clock),
	// 0 offers the setup again; nil = unchanged.
	OnboardedAt *int64 `json:"onboarded_at,omitempty"`
	// TrustedHosts: the exact HTTPS names this running daemon answers on besides 127.0.0.1
	// (config.json api.trusted_hosts as loaded at start), so the setup's phone step can show
	// the address a phone opens. Read only: a PUT never changes it.
	TrustedHosts []string `json:"trusted_hosts,omitempty"`
}

func (s *Server) configView(c config.Config) ConfigView {
	hosts := []string{}
	for _, h := range s.trustedHosts {
		if config.ValidTrustedHost(h) {
			hosts = append(hosts, config.NormalizeTrustedHost(h))
		}
	}
	return ConfigView{HeadHarness: c.HeadHarness, AgyBackgroundUsage: c.AgyBackgroundUsage, OnboardedAt: &c.OnboardedAt, TrustedHosts: hosts}
}

// MarkOnboardedIfInUse keeps the first-run setup from opening on a machine that is already in
// use: the daemon calls it once at each start (internal/cli/daemon.go). While config.json
// "onboarded_at" is 0 and the fleet has an agent besides the head, or a project besides the
// head's own General, it stores now, as if the setup had been closed; Settings and the command
// palette still open it. A new install has neither, so it is still offered the setup, also
// after a restart. It reports whether it wrote. This differs from Mustneer's tmx-go on purpose
// (head, 17 Nov 2026): there every machine gets the setup once after the update that brings it.
func MarkOnboardedIfInUse(f *fleet.Fleet) (bool, error) {
	c, err := config.Load(f.Paths())
	if err != nil || c.OnboardedAt != 0 {
		return false, err
	}
	snap, err := f.Snapshot()
	if err != nil || !fleetInUse(snap) {
		return false, err
	}
	c.OnboardedAt = time.Now().UnixMilli()
	if err := config.Save(f.Paths(), c); err != nil {
		return false, err
	}
	return true, nil
}

// fleetInUse: someone has worked here already (see MarkOnboardedIfInUse).
func fleetInUse(snap model.Snapshot) bool {
	head := ""
	if snap.Head != nil {
		head = snap.Head.ID
	}
	for _, a := range snap.Agents {
		if a.Level != model.LevelHead {
			return true
		}
	}
	for _, p := range snap.Projects {
		if head == "" || p.LeadID != head {
			return true
		}
	}
	return false
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	c, err := config.Load(s.f.Paths())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.configView(c))
}

// putConfig: PUT /api/config {head_harness?, onboarded_at?}. Switching the head to another
// harness is behind the owner gate once it is on (the head runs on it from the next daemon
// start). head_harness is the only field that asks, so one save asks at most once; a request
// that changes nothing neither asks nor writes (it cannot undo a confirmed change racing it
// either). onboarded_at only remembers that the first-run setup was shown: it needs no proof,
// and trusted_hosts in a body is ignored.
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var v ConfigView
	if !decode(w, r, &v) {
		return
	}
	c, err := config.Load(s.f.Paths())
	if err != nil {
		writeErr(w, err)
		return
	}
	if v.HeadHarness != "" {
		info, ok := harness.Get(model.Harness(v.HeadHarness))
		if !ok || !info.Supports(model.ModeChat) {
			writeError(w, http.StatusBadRequest, "the head needs a chat harness (claude or codex)")
			return
		}
		if v.HeadHarness != c.HeadHarness {
			if !s.RequireOwner(w, r, "switch the head agent's harness to "+info.Label, v) {
				return
			}
			c.HeadHarness = v.HeadHarness
			if err := config.Save(s.f.Paths(), c); err != nil {
				writeErr(w, err)
				return
			}
		}
	}
	if v.AgyBackgroundUsage != nil && (c.AgyBackgroundUsage == nil || *c.AgyBackgroundUsage != *v.AgyBackgroundUsage) {
		c.AgyBackgroundUsage = v.AgyBackgroundUsage
		if err := config.Save(s.f.Paths(), c); err != nil {
			writeErr(w, err)
			return
		}
	}
	if v.OnboardedAt != nil && (*v.OnboardedAt == 0) != (c.OnboardedAt == 0) {
		c.OnboardedAt = 0
		if *v.OnboardedAt != 0 {
			c.OnboardedAt = time.Now().UnixMilli()
		}
		if err := config.Save(s.f.Paths(), c); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.configView(c))
}
