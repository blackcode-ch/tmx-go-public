package api

import (
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/skill"
)

// skillRoutes: GET /skill.md, the skill file for AI agents (internal/skill). Public like the
// UI (outside /api/, so the guard asks no token): it holds no secrets.
func (s *Server) skillRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /skill.md", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(skill.Content())
	})
}
