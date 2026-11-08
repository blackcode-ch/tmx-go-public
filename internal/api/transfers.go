package api

import (
	"errors"
	"github.com/blackcode-ch/tmx-go/internal/antechamber"
	"github.com/blackcode-ch/tmx-go/internal/transfers"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Wire mutations are exclusively on the authenticated pair-token mux.
func transferProtocolPath(p string) bool {
	return p == "/api/remote" || p == "/api/transfers" || strings.HasPrefix(p, "/api/transfers/") || strings.HasPrefix(p, "/api/exports/") || strings.HasPrefix(p, "/api/relay/")
}
func (s *Server) transferPeerRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /api/remote", s.peerTransferRelay)
	m.HandleFunc("GET /api/relay/{host}/exports/{ticket}", s.peerExportRelay)
	m.HandleFunc("POST /api/transfers", s.peerTransferStart)
	m.HandleFunc("POST /api/transfers/incoming", s.peerTransferIncoming)
	m.HandleFunc("GET /api/transfers", s.peerTransferList)
	m.HandleFunc("GET /api/transfers/{id}", s.peerTransferGet)
	m.HandleFunc("GET /api/transfers/{id}/report", s.peerTransferReport)
	m.HandleFunc("POST /api/transfers/{id}/cancel", s.peerTransferCancel)
	m.HandleFunc("GET /api/exports/{ticket}", s.peerExportGet)
	m.HandleFunc("POST /api/exports/{ticket}/done", s.peerExportDone)
}

func (s *Server) peerTransferRelay(w http.ResponseWriter, r *http.Request) {
	if s.transfers == nil {
		writeError(w, 403, "only configured fleet transfers may be relayed")
		return
	}
	if !s.transferReady(w, r) {
		return
	}
	var b struct {
		Host, Method, Path string
		Body               any
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := s.transfers.RelayCall(r.Context(), strings.ToLower(b.Host), b.Method, b.Path, b.Body)
	if err != nil {
		transferError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) peerExportRelay(w http.ResponseWriter, r *http.Request) {
	if !s.transferReady(w, r) {
		return
	}
	offset := int64(0)
	if value := r.Header.Get("Range"); value != "" {
		if !strings.HasPrefix(value, "bytes=") || !strings.HasSuffix(value, "-") {
			writeError(w, 416, "only open-ended byte ranges are supported")
			return
		}
		var err error
		offset, err = strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(value, "bytes="), "-"), 10, 64)
		if err != nil || offset < 0 {
			writeError(w, 416, "invalid range")
			return
		}
	}
	res, err := s.peers.ExportStream(r.Context(), r.PathValue("host"), r.PathValue("ticket"), offset)
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 206 {
		writeError(w, res.StatusCode, "source refused export")
		return
	}
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := res.Header.Get(key); v != "" {
			w.Header().Set(key, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(res.Body, transfers.MaxFile+1))
}
func (s *Server) transferReady(w http.ResponseWriter, r *http.Request) bool {
	if peerOf(r).name == "" {
		writeError(w, 403, "peers only")
		return false
	}
	if s.transfers == nil {
		writeError(w, 501, "fleet transfers are not configured")
		return false
	}
	return true
}
func transferError(w http.ResponseWriter, err error) {
	var e *transfers.Error
	var a *antechamber.Error
	switch {
	case errors.As(err, &e):
		writeError(w, e.Status, e.Message)
	case errors.As(err, &a):
		writeError(w, a.Status, a.Error())
	default:
		writeError(w, 500, err.Error())
	}
}
func (s *Server) peerTransferStart(w http.ResponseWriter, r *http.Request) {
	if !s.transferReady(w, r) {
		return
	}
	var b transfers.Record
	if !decode(w, r, &b) {
		return
	}
	rec, err := s.transfers.Start(r.Context(), b, peerOf(r).name)
	if err != nil {
		transferError(w, err)
		return
	}
	writeJSON(w, 202, rec)
}
func (s *Server) peerTransferIncoming(w http.ResponseWriter, r *http.Request) {
	if !s.transferReady(w, r) {
		return
	}
	var b transfers.Record
	if !decode(w, r, &b) {
		return
	}
	rec, err := s.transfers.Incoming(b, peerOf(r).name)
	if err != nil {
		transferError(w, err)
		return
	}
	writeJSON(w, 202, rec)
}
func (s *Server) peerTransferList(w http.ResponseWriter, r *http.Request) {
	if !s.transferReady(w, r) {
		return
	}
	writeJSON(w, 200, map[string]any{"host": s.peers.Machine(), "transfers": s.transfers.List()})
}
func (s *Server) peerTransferGet(w http.ResponseWriter, r *http.Request) {
	if !s.transferReady(w, r) {
		return
	}
	rec, err := s.transfers.StatusAt(r.Context(), r.URL.Query().Get("host"), r.PathValue("id"))
	if err != nil {
		transferError(w, err)
		return
	}
	writeJSON(w, 200, rec)
}
func (s *Server) peerTransferCancel(w http.ResponseWriter, r *http.Request) {
	if !s.transferReady(w, r) {
		return
	}
	rec, err := s.transfers.Cancel(r.Context(), r.PathValue("id"), peerOf(r).name)
	if err != nil {
		transferError(w, err)
		return
	}
	writeJSON(w, 200, rec)
}
func (s *Server) peerTransferReport(w http.ResponseWriter, r *http.Request) {
	if !s.transferReady(w, r) {
		return
	}
	rec, err := transfers.ReadRecord(s.transfers.Root, r.PathValue("id"))
	if err != nil {
		transferError(w, err)
		return
	}
	trusted, _ := rec["trusted_backup"].(bool)
	dest, _ := rec["dest"].(string)
	if !trusted || rec["state"] != "placed" {
		writeError(w, 404, "no backup report")
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	http.ServeFile(w, r, filepath.Join(dest, "report.html"))
}
func (s *Server) peerExportGet(w http.ResponseWriter, r *http.Request) {
	if !s.transferReady(w, r) {
		return
	}
	e, err := s.transfers.Exports.Get(r.PathValue("ticket"), peerOf(r).name)
	if err != nil {
		if errors.Is(err, transfers.ErrNotYours) {
			writeError(w, 403, err.Error())
		} else {
			writeError(w, 404, err.Error())
		}
		return
	}
	f, err := os.Open(e.Path)
	if err != nil {
		writeError(w, 404, "export is gone")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/zip")
	http.ServeContent(w, r, e.Name, e.ModTime, f)
}
func (s *Server) peerExportDone(w http.ResponseWriter, r *http.Request) {
	if !s.transferReady(w, r) {
		return
	}
	if err := s.transfers.Exports.Done(r.PathValue("ticket"), peerOf(r).name); err != nil {
		writeError(w, 403, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
