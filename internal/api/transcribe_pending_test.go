package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/transcribe"
)

func TestPendingTranscriptionReturnsRecoverableID(t *testing.T) {
	w := httptest.NewRecorder()
	writeVoiceErr(w, transcribe.PendingError{ID: "retained-id"})
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 202 || got["state"] != "pending" || got["message"] != "transcription pending" || got["transcript_id"] != "retained-id" {
		t.Fatalf("%d %v", w.Code, got)
	}
}
