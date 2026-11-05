package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/uploads"
)

// An upload travels as JSON with base64 content, so every UI transport (fetch, the desktop
// bridge, the mock) carries it the same way. 20 MB of file is about 27 MB of base64.
const maxUploadBody = uploads.MaxFileBytes/3*4 + 64<<10

type uploadView struct {
	uploads.File
	Image bool   `json:"image"`
	Data  string `json:"data,omitempty"` // base64, only with GET ?data=1
}

func view(f uploads.File) uploadView { return uploadView{File: f, Image: f.Image()} }

// POST /api/agents/{id}/uploads {name, type, data}: store a file for a later message.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if s.uploads == nil {
		writeError(w, http.StatusNotImplemented, "uploads are not available")
		return
	}
	a, err := s.f.Agent(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Data string `json:"data"`
	}
	if !decodeMax(w, r, &req, maxUploadBody) {
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "data is not valid base64")
		return
	}
	f, err := s.uploads.Save(a.ID, req.Name, req.Type, data)
	switch {
	case errors.Is(err, uploads.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	case errors.Is(err, uploads.ErrEmpty), errors.Is(err, uploads.ErrNotFound):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, view(f))
}

// GET /api/uploads/{id}[?data=1]: an upload's details, with its content as base64 when asked.
func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) {
	if s.uploads == nil {
		writeError(w, http.StatusNotImplemented, "uploads are not available")
		return
	}
	id := r.PathValue("id")
	if r.URL.Query().Get("data") != "1" {
		f, err := s.uploads.Get(id)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, view(f))
		return
	}
	f, b, err := s.uploads.Read(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	v := view(f)
	v.Data = base64.StdEncoding.EncodeToString(b)
	writeJSON(w, http.StatusOK, v)
}

// attach resolves upload ids for a message to agentID and returns text with the note that
// lists them. Every id must be an upload made for that same agent.
func (s *Server) attach(agentID, text string, ids []string) (string, error) {
	if len(ids) == 0 {
		return text, nil
	}
	if s.uploads == nil {
		return "", errors.New("uploads are not available")
	}
	if len(ids) > uploads.MaxPerMessage {
		return "", fmt.Errorf("a message carries at most %d files", uploads.MaxPerMessage)
	}
	files := make([]uploads.File, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if seen[id] {
			continue
		}
		seen[id] = true
		f, err := s.uploads.Get(id)
		if err != nil || f.AgentID != agentID {
			return "", fmt.Errorf("no upload %q for this agent", id)
		}
		files = append(files, f)
	}
	return uploads.WithNote(text, files), nil
}
