package api

import (
	"context"
	"encoding/base64"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
)

// Voice transcription (DESIGN.md "Voice"). Owner only, except reading stored transcripts,
// which every local agent may (MCP transcripts / transcript_read). Without the service every
// route answers 501.
func (s *Server) voiceRoutes(mux *http.ServeMux) {
	o := func(h http.HandlerFunc) http.HandlerFunc { return s.ownerOnly(s.withVoice(h)) }
	mux.HandleFunc("GET /api/voice", o(s.getVoice))
	mux.HandleFunc("PUT /api/voice", o(s.putVoice))
	mux.HandleFunc("POST /api/voice/setup", o(s.voiceSetup))
	mux.HandleFunc("POST /api/voice/models/{name}", o(s.voiceGetModel))
	mux.HandleFunc("DELETE /api/voice/models/{name}", o(s.voiceRemoveModel))
	mux.HandleFunc("POST /api/transcribe", o(s.transcribeReceipt(s.transcribeNow)))
	mux.HandleFunc("GET /api/transcripts", s.ownerOrAgent(s.withVoice(s.listTranscripts)))
	mux.HandleFunc("GET /api/transcripts/{id}", s.ownerOrAgent(s.withVoice(s.getTranscript)))
	mux.HandleFunc("DELETE /api/transcripts/{id}", o(s.deleteTranscript))
}

func (s *Server) withVoice(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.voice == nil {
			writeError(w, http.StatusNotImplemented, "voice transcription is not available")
			return
		}
		h(w, r)
	}
}

// ownerOrAgent admits the owner and this machine's agents (not Companion's link).
func (s *Server) ownerOrAgent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, _ := caller(r); !c.Owner && c.AgentID == "" {
			writeError(w, http.StatusForbidden, "only the owner and this machine's agents can read transcripts")
			return
		}
		h(w, r)
	}
}

// writeVoiceErr maps the service's errors to statuses.
func writeVoiceErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, transcribe.ErrNoSpeech):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, transcribe.ErrInvalid):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), transcribe.ErrInvalid.Error()+": "))
	case errors.Is(err, transcribe.ErrNotSetUp):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, transcribe.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "the transcription was stopped")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) getVoice(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.voice.Status())
}

// PUT /api/voice {model?, language?, threads?, vad?, auto_transcribe?}
func (s *Server) putVoice(w http.ResponseWriter, r *http.Request) {
	var req transcribe.SettingsPatch
	if !decode(w, r, &req) {
		return
	}
	st, err := s.voice.Configure(req)
	if err != nil {
		writeVoiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// POST /api/voice/setup: downloads the engine, the chosen model and the voice activity model
// (what is missing) in the background; progress in GET /api/voice and "voice.status".
func (s *Server) voiceSetup(w http.ResponseWriter, r *http.Request) {
	st, err := s.voice.Setup()
	if err != nil {
		writeVoiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

func (s *Server) voiceGetModel(w http.ResponseWriter, r *http.Request) {
	st, err := s.voice.GetModel(r.PathValue("name"))
	if err != nil {
		writeVoiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

func (s *Server) voiceRemoveModel(w http.ResponseWriter, r *http.Request) {
	st, err := s.voice.RemoveModel(r.PathValue("name"))
	if err != nil {
		writeVoiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// maxDictationBody: two minutes of 16 kHz mono PCM16 (3.84 MB) in base64, with room to spare.
const maxDictationBody = 8 << 20

// POST /api/transcribe: transcribes now and answers {text, language, segments}; nothing is
// stored. JSON {data: base64 WAV (at most 2 minutes), language?} is dictation; a raw audio body
// (Content-Type audio/* or application/octet-stream; WAV, MP3, FLAC or Ogg, at most 512 MB;
// ?language=) is a file (tmx transcribe FILE). The answer waits for the job's turn.
func (s *Server) transcribeNow(w http.ResponseWriter, r *http.Request) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "audio/") || ct == "application/octet-stream" {
		res, err := s.voice.TranscribeFile(r.Context(), http.MaxBytesReader(w, r.Body, transcribe.MaxFileBytes), r.URL.Query().Get("language"))
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "the file is larger than 512 MB")
			return
		}
		if err != nil {
			writeVoiceErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	var req struct {
		Data     string `json:"data"`
		Language string `json:"language"`
	}
	if !decodeMax(w, r, &req, maxDictationBody) {
		return
	}
	wav, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil || len(wav) == 0 {
		writeError(w, http.StatusBadRequest, "data is not a base64 WAV")
		return
	}
	var hosts transcribe.ConfiguredHosts
	if r.Header.Get(transcriptionLocalOnly) == "" && s.f != nil {
		hosts = &configuredVoiceHosts{paths: s.f.Paths(), ownPort: s.port, requestID: r.Header.Get("Idempotency-Key")}
	}
	res, err := s.voice.DictateWithHosts(r.Context(), wav, req.Language, hosts)
	if err != nil {
		writeVoiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// GET /api/transcripts?q=&source_prefix=&limit= → Transcript[], newest first
func (s *Server) listTranscripts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := s.voice.List(store.TranscriptQuery{Q: q.Get("q"), SourcePrefix: q.Get("source_prefix"), Limit: limit})
	if err != nil {
		writeVoiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getTranscript(w http.ResponseWriter, r *http.Request) {
	t, err := s.voice.Get(r.PathValue("id"))
	if err != nil {
		writeVoiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteTranscript(w http.ResponseWriter, r *http.Request) {
	if err := s.voice.Delete(r.PathValue("id")); err != nil {
		writeVoiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
