package api

import (
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func (s *Server) agentFileRoutes(mux *http.ServeMux) {
	for _, base := range []string{"/api/agents/{id}/", "/api/sessions/{id}/"} {
		mux.HandleFunc("GET "+base+"files", s.agentFiles)
		mux.HandleFunc("GET "+base+"download", s.agentDownload)
		mux.HandleFunc("GET "+base+"file", s.agentFilePreview)
		mux.HandleFunc("POST "+base+"files", s.ownerOnly(s.upload))
		mux.HandleFunc("POST "+base+"image", s.ownerOnly(s.agentImage))
		mux.HandleFunc("GET "+base+"chat", s.agentChat)
		mux.HandleFunc("GET "+base+"attach", s.agentAttach)
		mux.HandleFunc("POST "+base+"open", s.agentOpen)
		mux.HandleFunc("POST "+base+"reveal", s.agentReveal)
	}
}

// relativeAgentPath does lexical validation; os.Root performs the actual
// traversal-resistant open, including symlinks and Windows reparse points.
func relativeAgentPath(root, path string) (string, error) {
	if path == "" {
		return ".", nil
	}
	if filepath.IsAbs(path) {
		var err error
		path, err = filepath.Rel(root, path)
		if err != nil {
			return "", os.ErrPermission
		}
	}
	path = filepath.FromSlash(strings.ReplaceAll(path, "\\", "/"))
	if !filepath.IsLocal(path) || strings.ContainsAny(path, ":\x00") {
		return "", os.ErrPermission
	}
	return path, nil
}

func (s *Server) agentFileRoot(w http.ResponseWriter, r *http.Request, path string) (*os.Root, string, bool) {
	c, _ := caller(r)
	a, err := s.f.AgentForFiles(c, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return nil, "", false
	}
	folder := a.Cwd
	switch r.URL.Query().Get("root") {
	case "", "cwd":
	case "uploads":
		if s.uploads == nil {
			writeError(w, 404, "no uploads folder")
			return nil, "", false
		}
		folder = s.uploads.AgentDir(a.ID)
	default:
		writeError(w, 400, "root must be cwd or uploads")
		return nil, "", false
	}
	name, err := relativeAgentPath(folder, path)
	if err != nil {
		writeError(w, 403, "path is outside the agent folder")
		return nil, "", false
	}
	var root *os.Root
	if r.URL.Query().Get("root") == "uploads" {
		root, err = s.uploads.OpenAgentRoot(a.ID)
	} else {
		root, err = os.OpenRoot(folder)
	}
	if err != nil {
		fileError(w, err)
		return nil, "", false
	}
	return root, name, true
}

func fileError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, 404, "no such file or folder")
		return
	}
	writeError(w, 403, "file is unavailable or outside the agent folder")
}

func (s *Server) agentFiles(w http.ResponseWriter, r *http.Request) {
	root, path, ok := s.agentFileRoot(w, r, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	defer root.Close()
	directory, err := root.OpenRoot(path)
	if err != nil {
		fileError(w, err)
		return
	}
	defer directory.Close()
	f, err := directory.Open(".")
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	rows, err := f.ReadDir(1001)
	if err != nil && !errors.Is(err, io.EOF) {
		writeError(w, 404, "no such folder")
		return
	}
	truncated := len(rows) > 1000
	if truncated {
		rows = rows[:1000]
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name() < rows[j].Name() })
	items := []map[string]any{}
	for _, row := range rows {
		// Do not follow links or expose their destinations while listing.
		if row.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := row.Info()
		if err != nil {
			continue
		}
		items = append(items, map[string]any{"name": row.Name(), "path": filepath.ToSlash(filepath.Join(path, row.Name())), "dir": row.IsDir(), "size": info.Size(), "modified": info.ModTime().UnixMilli()})
	}
	writeJSON(w, 200, map[string]any{"path": filepath.ToSlash(path), "files": items, "truncated": truncated})
}

func (s *Server) agentDownload(w http.ResponseWriter, r *http.Request) {
	root, path, ok := s.agentFileRoot(w, r, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	defer root.Close()
	f, err := openAgentFile(root, path)
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, 400, "download requires a regular file")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func (s *Server) agentFilePreview(w http.ResponseWriter, r *http.Request) {
	root, path, ok := s.agentFileRoot(w, r, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	defer root.Close()
	f, err := openAgentFile(root, path)
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, 400, "preview requires a regular file")
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil {
		fileError(w, err)
		return
	}
	truncated := len(data) > 256<<10
	if truncated {
		data = data[:256<<10]
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		writeError(w, 415, "use download for binary files")
		return
	}
	writeJSON(w, 200, map[string]any{"path": filepath.ToSlash(path), "text": string(data), "truncated": truncated})
}

func (s *Server) agentImage(w http.ResponseWriter, r *http.Request) {
	if s.uploads == nil {
		writeError(w, 501, "uploads are unavailable")
		return
	}
	var req struct {
		Name string `json:"name"`
		MIME string `json:"mime"`
		Data string `json:"data"`
	}
	if !decodeMax(w, r, &req, maxUploadBody) {
		return
	}
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp"}[req.MIME]
	if ext == "" {
		writeError(w, 400, "mime must be PNG, JPEG, GIF or WebP")
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil || len(data) == 0 {
		writeError(w, 400, "data must be nonempty base64")
		return
	}
	if http.DetectContentType(data) != req.MIME {
		writeError(w, 415, "image content does not match mime")
		return
	}
	a, err := s.f.Agent(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if a.Harness == model.HarnessShell {
		writeError(w, 403, "image paste is for harness prompts, not shells")
		return
	}
	if req.Name == "" {
		req.Name = "image." + ext
	}
	f, err := s.uploads.Save(a.ID, req.Name, req.MIME, data)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	// Go chat mode retains the upload for the existing send/uploads workflow.
	if a.Mode == model.ModeTerminal {
		c, _ := caller(r)
		if err := s.f.TypeText(c, a.ID, strconv.Quote(f.Path)+" ", false); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, 201, view(f))
}

func (s *Server) agentChat(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	a, err := s.f.AgentForFiles(c, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var cursor int64
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 0 {
			writeError(w, 400, "cursor must be a nonnegative sequence number")
			return
		}
	}
	if conv := r.URL.Query().Get("conv"); conv != "" && conv != a.SessionID {
		cursor = 0
	}
	items, err := s.f.Timeline(a.ID, cursor, 200)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(items) > 0 {
		cursor = items[len(items)-1].Seq
	}
	writeJSON(w, 200, map[string]any{"name": a.Name, "state": a.State, "conv": a.SessionID, "items": items, "cursor": cursor})
}

func (s *Server) agentAttach(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	if _, err := s.f.AgentForFiles(c, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	s.terminal(w, r)
}
