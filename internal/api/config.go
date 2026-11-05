package api

import (
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// ConfigView is the small subset of config.json the UI may read and edit.
type ConfigView struct {
	HeadHarness string `json:"head_harness"`
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	c, err := config.Load(s.f.Paths())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ConfigView{HeadHarness: c.HeadHarness})
}

// putConfig: PUT /api/config {head_harness}. Switching the head to another harness is behind the
// owner gate once it is on (the head runs on it from the next daemon start). head_harness is the
// only field, so one save asks at most once; a request that changes nothing neither asks nor
// writes (it cannot undo a confirmed change racing it either).
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
	writeJSON(w, http.StatusOK, ConfigView{HeadHarness: c.HeadHarness})
}
