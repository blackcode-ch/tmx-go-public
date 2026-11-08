package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

func (s *Server) taskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tasks/capabilities", s.taskCapabilities)
	mux.HandleFunc("POST /api/tasks", s.postTask)
	mux.HandleFunc("GET /api/tasks/{id}", s.getTask)
}

func (s *Server) taskCapabilities(w http.ResponseWriter, r *http.Request) {
	if !taskLocalOperator(w, r) {
		return
	}
	// Optional features are omitted until implemented; Companion rejects false
	// or zero feature claims and any extra keys in this exact envelope.
	writeJSON(w, 200, map[string]any{"task_receipts": 1, "host": strings.ToLower(s.f.Machine())})
}

func taskLocalOperator(w http.ResponseWriter, r *http.Request) bool {
	if !localOwner(r) {
		writeError(w, 403, "tasks require the authenticated local operator; Companion process authentication is not enabled")
		return false
	}
	return true
}

func (s *Server) postTask(w http.ResponseWriter, r *http.Request) {
	if !taskLocalOperator(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	dec := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			writeError(w, 413, "task request is too large")
		} else {
			writeError(w, 400, "invalid task JSON")
		}
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, 400, "task request must contain one object")
		return
	}
	for k, v := range fields {
		if k != "id" && k != "to" && k != "body" && k != "from" && k != "urgent" {
			writeError(w, 400, "unknown task field")
			return
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			writeError(w, 400, "task fields cannot be null")
			return
		}
	}
	b, _ := json.Marshal(fields)
	var req fleet.TaskRequest
	if err := json.Unmarshal(b, &req); err != nil {
		writeError(w, 400, "invalid task field types")
		return
	}
	c, _ := caller(r)
	out, err := s.f.PostTask(c, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	if !taskLocalOperator(w, r) {
		return
	}
	c, _ := caller(r)
	out, err := s.f.GetTask(c, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}
