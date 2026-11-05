package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/chat"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
)

// Recordings (DESIGN.md "Recorder"). Everything that starts, feeds, changes or plays a
// recording is owner-only; agents may list and read recordings (the owner's decision) and
// then read the files at the paths given. A <video src> plays with a ticket instead of the
// owner token, which never goes in a URL.
func (s *Server) recordingRoutes(mux *http.ServeMux) {
	o, on := s.ownerOnly, s.recOn
	mux.HandleFunc("GET /api/recordings", on(s.listRecordings)) // owner, agents
	mux.HandleFunc("POST /api/recordings", o(on(s.beginRecording)))
	mux.HandleFunc("GET /api/recordings/active", o(on(s.activeRecording)))
	mux.HandleFunc("GET /api/recordings/{id}", on(s.getRecording)) // owner, agents
	mux.HandleFunc("PATCH /api/recordings/{id}", o(on(s.renameRecording)))
	mux.HandleFunc("DELETE /api/recordings/{id}", o(on(s.deleteRecording)))
	mux.HandleFunc("POST /api/recordings/{id}/state", o(on(s.recordingState)))
	mux.HandleFunc("PUT /api/recordings/{id}/segments/{n}", o(on(s.putChunk)))
	mux.HandleFunc("POST /api/recordings/{id}/chunks", o(on(s.postChunk)))
	mux.HandleFunc("GET /api/recordings/{id}/segments/{n}", on(s.segmentFile)) // the owner, or a ticket
	mux.HandleFunc("POST /api/recordings/{id}/ticket", o(on(s.recordingTicket)))
	mux.HandleFunc("POST /api/recordings/{id}/share", o(on(s.shareRecording)))
	mux.HandleFunc("POST /api/recordings/{id}/transcribe", o(on(s.transcribeRecording)))
	mux.HandleFunc("PUT /api/recordings/{id}/frames/{ms}", o(on(s.putFrame)))
	mux.HandleFunc("GET /api/recordings/{id}/frames/{ms}", on(s.frameFile))      // the owner, or a ticket
	mux.HandleFunc("POST /api/recordings/{id}/native", o(on(s.nativeRecording))) // recordings_native.go
	mux.HandleFunc("POST /api/recordings/{id}/process", on(s.processRecording))  // only the report worker launched for it (recworker)
}

// recOn answers 501 when this daemon has no recorder.
func (s *Server) recOn(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.rec == nil {
			writeError(w, http.StatusNotImplemented, "recordings are not available on this daemon")
			return
		}
		h(w, r)
	}
}

// ticketRequest reports whether r is a playback request (a segment, or a frame's <img>) that
// carries a ticket instead of a token (auth.go lets it through; segmentFile and frameFile
// check the ticket).
func ticketRequest(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		strings.HasPrefix(r.URL.Path, "/api/recordings/") &&
		(strings.Contains(r.URL.Path, "/segments/") || strings.Contains(r.URL.Path, "/frames/")) &&
		r.Header.Get("Authorization") == "" && r.URL.Query().Get("ticket") != ""
}

func writeRecErr(w http.ResponseWriter, err error) {
	var gap *recorder.GapError
	var ve recorder.VoiceError
	switch {
	case errors.As(err, &gap):
		writeJSON(w, http.StatusConflict, map[string]any{"error": gap.Error(), "size": gap.Size})
	case errors.Is(err, recorder.ErrNoVoice), errors.As(err, &ve):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeErr(w, err)
	}
}

func (s *Server) listRecordings(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.rec.List(limit)
	if err != nil {
		writeRecErr(w, err)
		return
	}
	s.withSummaries(list) // summaries.go
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getRecording(w http.ResponseWriter, r *http.Request) {
	rec, err := s.rec.Get(r.PathValue("id"))
	if err != nil {
		writeRecErr(w, err)
		return
	}
	rec = s.withSummary(rec) // summaries.go
	writeJSON(w, http.StatusOK, rec)
}

// POST /api/recordings {kind, mic, mime, title?, source?, system_audio?}: the owner's window
// began capturing (source native: the macOS app writes the files; recordings_native.go).
func (s *Server) beginRecording(w http.ResponseWriter, r *http.Request) {
	var req recorder.BeginRequest
	if !decode(w, r, &req) {
		return
	}
	rec, err := s.rec.Begin(req)
	if err != nil {
		writeRecErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

// GET /api/recordings/active: {recording (or null), settings}; every client shows the
// indicator from it.
func (s *Server) activeRecording(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"recording": s.rec.Active(), "settings": s.rec.Settings()})
}

func (s *Server) renameRecording(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title       *string `json:"title"`
		Sensitivity *string `json:"sensitivity"` // "clinic" | "" (recorder.SetSensitivity)
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Title == nil && req.Sensitivity == nil {
		writeError(w, http.StatusBadRequest, "nothing to change: give title and/or sensitivity")
		return
	}
	var rec model.Recording
	var err error
	if req.Title != nil {
		rec, err = s.rec.Rename(r.PathValue("id"), *req.Title)
	}
	if err == nil && req.Sensitivity != nil {
		rec, err = s.rec.SetSensitivity(r.PathValue("id"), *req.Sensitivity)
	}
	if err != nil {
		writeRecErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) deleteRecording(w http.ResponseWriter, r *http.Request) {
	if err := s.rec.Delete(r.PathValue("id")); err != nil {
		writeRecErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /api/recordings/{id}/state {state: paused|recording|stopped|heartbeat}; heartbeat (the
// window's, every 5 s) changes nothing but keeps the recording from being marked interrupted.
func (s *Server) recordingState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		State string `json:"state"`
	}
	if !decode(w, r, &req) {
		return
	}
	rec, err := s.rec.SetState(r.PathValue("id"), req.State)
	if err != nil {
		writeRecErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// PUT /api/recordings/{id}/segments/{n}?track=media|pcm&offset=O&dur=MS: raw bytes (the http
// transport). Answers {size}; 409 {error, size} for a gap.
func (s *Server) putChunk(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n, err1 := strconv.Atoi(r.PathValue("n"))
	off, err2 := strconv.ParseInt(q.Get("offset"), 10, 64)
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, "segment and offset must be numbers")
		return
	}
	dur, _ := strconv.ParseInt(q.Get("dur"), 10, 64)
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, recorder.MaxChunkBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a chunk is at most %d MB", recorder.MaxChunkBytes>>20))
		return
	}
	s.chunk(w, r.PathValue("id"), n, recorder.Track(q.Get("track")), off, dur, data)
}

// POST /api/recordings/{id}/chunks {segment, track, offset, dur_ms, data}: the same as JSON
// with base64 (the desktop bridge carries only text).
func (s *Server) postChunk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Segment int    `json:"segment"`
		Track   string `json:"track"`
		Offset  int64  `json:"offset"`
		DurMS   int64  `json:"dur_ms"`
		Data    string `json:"data"`
	}
	if !decodeMax(w, r, &req, recorder.MaxChunkBytes/3*4+64<<10) {
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "data is not valid base64")
		return
	}
	s.chunk(w, r.PathValue("id"), req.Segment, recorder.Track(req.Track), req.Offset, req.DurMS, data)
}

func (s *Server) chunk(w http.ResponseWriter, id string, n int, track recorder.Track, off, dur int64, data []byte) {
	size, err := s.rec.Append(id, n, track, off, dur, data)
	if err != nil {
		writeRecErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"size": size})
}

// POST /api/recordings/{id}/ticket → {ticket, expires_at}: plays GET …/segments/{n}?ticket=.
func (s *Server) recordingTicket(w http.ResponseWriter, r *http.Request) {
	if devicePlayback(w, r) {
		return
	}
	t, exp, err := s.rec.Ticket(r.PathValue("id"))
	if err != nil {
		writeRecErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ticket": t, "expires_at": exp.UnixMilli()})
}

// maxBridgeSegment: ?data=1 answers base64 JSON through the desktop bridge, which reads at most
// 64 MB of an answer.
const maxBridgeSegment = 40 << 20

// GET /api/recordings/{id}/segments/{n}?track=media|pcm[&ticket=T][&data=1]: one file, with
// Range (seeking) through http.ServeContent; data=1 answers {type, size, data} (base64) for the
// desktop bridge. The owner's token or a ticket for this recording.
func (s *Server) segmentFile(w http.ResponseWriter, r *http.Request) {
	id, q := r.PathValue("id"), r.URL.Query()
	if c, ok := caller(r); !ok || !c.Owner {
		if ok || !s.rec.CheckTicket(id, q.Get("ticket")) { // an agent's token, or a bad ticket
			writeError(w, http.StatusUnauthorized, "this needs the owner's token or a valid playback ticket")
			return
		}
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "the segment must be a number")
		return
	}
	path, typ, err := s.rec.SegmentFile(id, n, recorder.Track(q.Get("track")))
	if err != nil {
		writeRecErr(w, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "the file is no longer stored here")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if q.Get("data") == "1" {
		if st.Size() > maxBridgeSegment {
			writeError(w, http.StatusRequestEntityTooLarge, "this segment is too large to pass through the window's bridge")
			return
		}
		b, err := io.ReadAll(f)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": typ, "size": len(b), "data": base64.StdEncoding.EncodeToString(b)})
		return
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
}

// POST /api/recordings/{id}/share {chat_id, body?, segments?}: every segment (or the ones
// named) becomes a file of one chat message. A message carries at most 10 files of 25 MB.
func (s *Server) shareRecording(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatID   string `json:"chat_id"`
		Body     string `json:"body"`
		Segments []int  `json:"segments"`
	}
	if !decode(w, r, &req) {
		return
	}
	if s.chats == nil || !s.chats.Enabled() {
		writeChatErr(w, chat.ErrOff)
		return
	}
	rec, err := s.rec.Get(r.PathValue("id"))
	if err != nil {
		writeRecErr(w, err)
		return
	}
	if rec.State.Live() {
		writeError(w, http.StatusConflict, "the recording is still running; stop it first")
		return
	}
	segs, err := shareSegments(rec, req.Segments)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ext := filepath.Ext(segs[0].File)
	ids := make([]string, 0, len(segs))
	for i, g := range segs {
		name := rec.Title + ext
		if len(segs) > 1 {
			name = fmt.Sprintf("%s (part %d of %d)%s", rec.Title, i+1, len(segs), ext)
		}
		f, err := s.chats.UploadPath(req.ChatID, name, rec.Mime, g.File)
		if err != nil {
			writeChatErr(w, err)
			return
		}
		ids = append(ids, f.ID)
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		body = "Recording: " + rec.Title + " (" + shortDuration(rec.DurationMS) + ")"
	}
	m, err := s.chats.SendWith(fleet.OwnerCaller, req.ChatID, chat.SendRequest{Body: body, Files: ids})
	s.reply(w, http.StatusCreated, m, err)
}

// shareSegments picks the segments to share and checks the chat limits.
func shareSegments(rec model.Recording, want []int) ([]model.RecordingSegment, error) {
	var segs []model.RecordingSegment
	if len(want) == 0 {
		for _, g := range rec.Segments {
			if g.Bytes > 0 {
				segs = append(segs, g)
			}
		}
	} else {
		seen := map[int]bool{}
		for _, n := range want {
			if n < 1 || n > len(rec.Segments) {
				return nil, fmt.Errorf("the recording has no segment %d", n)
			}
			if !seen[n] {
				seen[n] = true
				segs = append(segs, rec.Segments[n-1])
			}
		}
	}
	switch {
	case len(segs) == 0:
		return nil, errors.New("the recording is empty")
	case len(segs) > chat.MaxFilesPerMessage:
		return nil, fmt.Errorf("too long to share: %d segments, and a message carries at most %d files; share some of them (segments)",
			len(segs), chat.MaxFilesPerMessage)
	}
	for _, g := range segs {
		if g.Bytes > chat.MaxFileBytes {
			return nil, fmt.Errorf("segment %d is %d MB; a chat file is at most %d MB", g.N, g.Bytes>>20, chat.MaxFileBytes>>20)
		}
	}
	return segs, nil
}

func shortDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute).Minutes()))
	default:
		return fmt.Sprintf("%d s", int(d.Round(time.Second).Seconds()))
	}
}

// POST /api/recordings/{id}/transcribe {language?}: queue every segment's WAV with the local
// transcription service → 202 {queued, sources}; 503 when voice isn't set up.
func (s *Server) transcribeRecording(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Language string `json:"language"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	n, err := s.rec.Transcribe(id, req.Language)
	if err != nil {
		writeRecErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": n, "source_prefix": "recording:" + id + "/"})
}

// PUT /api/recordings/{id}/frames/{ms}: one still JPEG of the live screen or camera recording
// at ms of recorded time, for the summaries. The body is the JPEG itself (Content-Type
// image/jpeg, the http transport) or JSON {data} with base64 (the desktop bridge). → 200
// {ms, path, bytes}; 400 not a JPEG or larger than 1280 px; 413 over 600 KB; 409 once the
// recording is finished or has 400 frames. ?key=1 (or JSON {key: true}) marks a key moment: the
// window saw the picture change.
func (s *Server) putFrame(w http.ResponseWriter, r *http.Request) {
	ms, err := strconv.ParseInt(r.PathValue("ms"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the frame's time must be a number of ms")
		return
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	limit := int64(recorder.MaxFrameBytes)
	if ct == "application/json" {
		limit = limit/3*4 + 4<<10
	} else if ct != "image/jpeg" {
		writeError(w, http.StatusUnsupportedMediaType, "a frame is image/jpeg (or JSON {data} with base64)")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a frame is at most %d KB", recorder.MaxFrameBytes>>10))
		return
	}
	data, key := body, r.URL.Query().Get("key") == "1"
	if ct == "application/json" {
		var req struct {
			Data string `json:"data"`
			Key  bool   `json:"key"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "the request body is not valid JSON: "+err.Error())
			return
		}
		key = key || req.Key
		if data, err = base64.StdEncoding.DecodeString(req.Data); err != nil {
			writeError(w, http.StatusBadRequest, "data is not valid base64")
			return
		}
	}
	if len(data) > recorder.MaxFrameBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a frame is at most %d KB", recorder.MaxFrameBytes>>10))
		return
	}
	f, err := s.rec.PutKeyFrame(r.PathValue("id"), ms, data, key)
	if err != nil {
		writeRecErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// GET /api/recordings/{id}/frames/{ms}[?ticket=T][&data=1]: one frame's JPEG; data=1 answers
// {type, size, data} (base64) for the desktop bridge. The owner's token or a ticket for this
// recording (a frame strip's <img>).
func (s *Server) frameFile(w http.ResponseWriter, r *http.Request) {
	id, q := r.PathValue("id"), r.URL.Query()
	if c, ok := caller(r); !ok || !c.Owner {
		if ok || !s.rec.CheckTicket(id, q.Get("ticket")) {
			writeError(w, http.StatusUnauthorized, "this needs the owner's token or a valid playback ticket")
			return
		}
	}
	ms, err := strconv.ParseInt(r.PathValue("ms"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the frame's time must be a number of ms")
		return
	}
	path, err := s.rec.FrameFile(id, ms)
	if err != nil {
		writeRecErr(w, err)
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "the frame is no longer stored here")
		return
	}
	if q.Get("data") == "1" {
		writeJSON(w, http.StatusOK, map[string]any{"type": "image/jpeg", "size": len(b), "data": base64.StdEncoding.EncodeToString(b)})
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

// ProcessResponse is POST /api/recordings/{id}/process: the report's state, and why when it failed. Never the
// transcript or the summary: the caller is the report worker, a cloud model that must not see client content.
type ProcessResponse struct {
	State model.SummaryState `json:"state"`
	Error string             `json:"error,omitempty"`
}

// POST /api/recordings/{id}/process → ProcessResponse: the report worker (internal/recworker) asks for the
// report the first time and is told its state each time. Any other caller is refused (403).
func (s *Server) processRecording(w http.ResponseWriter, r *http.Request) {
	if s.recw == nil {
		writeError(w, http.StatusNotImplemented, "the report worker is not available on this daemon")
		return
	}
	c, _ := caller(r)
	st, why, err := s.recw.Process(c, r.PathValue("id"))
	if err != nil {
		writeRecErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ProcessResponse{State: st, Error: why})
}
