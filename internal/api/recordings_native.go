package api

import (
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/recorder"
)

// POST /api/recordings/{id}/native {segment?, duration_ms?, closed?, last?, frames?, error?}:
// the macOS app's report about a native recording, whose files it writes itself (DESIGN.md
// "Recorder", native capture). It opens, updates and closes segments, names the frames it
// wrote, ends the recording with last, and is the app's heartbeat. Answers the Recording, whose
// state and stopping the app follows.
func (s *Server) nativeRecording(w http.ResponseWriter, r *http.Request) {
	var req recorder.NativeReport
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	rec, err := s.rec.Native(r.PathValue("id"), req)
	if err != nil {
		writeRecErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}
