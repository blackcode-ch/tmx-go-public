package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

// writeJSON marshals v fully before writing anything, so the reply always carries a
// Content-Length. encoding directly into w (json.NewEncoder(w).Encode) leaves net/http
// unable to know the body's size ahead of the headers, and past its small buffer (a few KB)
// it falls back to Transfer-Encoding: chunked - which the Python peers' HTTP client cannot
// read (found 4 Nov: Hostb looked agent-less to Hosta/Hostc once any reply, e.g.
// GET /api/sessions with enough agents, passed that size).
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeErr maps a fleet error to its status; other errors are 500 with their text.
func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch fleet.KindOf(err) {
	case fleet.KindInvalid:
		status = http.StatusBadRequest
	case fleet.KindNotFound:
		status = http.StatusNotFound
	case fleet.KindForbidden:
		status = http.StatusForbidden
	case fleet.KindConflict:
		status = http.StatusConflict
	case fleet.KindTooMany:
		status = http.StatusTooManyRequests
	}
	writeError(w, status, err.Error())
}

// decode reads a JSON body (at most MaxBody). It writes the error response itself.
func decode(w http.ResponseWriter, r *http.Request, v any) bool { return decodeMax(w, r, v, MaxBody) }

// decodeMax is decode with another size limit (uploads).
func decodeMax(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the request body is larger than %d MiB", limit>>20))
		case errors.Is(err, io.EOF):
			writeError(w, http.StatusBadRequest, "the request body is empty")
		default:
			writeError(w, http.StatusBadRequest, "the request body is not valid JSON: "+err.Error())
		}
		return false
	}
	return true
}
