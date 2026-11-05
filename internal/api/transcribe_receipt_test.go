package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func receiptRequest(key, body, ct string) *http.Request {
	r := httptest.NewRequest("POST", "/api/transcribe", strings.NewReader(body))
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Idempotency-Key", key)
	return r
}
func TestTranscriptionReceiptReplayAndConflict(t *testing.T) {
	for _, ct := range []string{"application/json", "audio/wav"} {
		t.Run(ct, func(t *testing.T) {
			calls := 0
			h := (&Server{}).transcribeReceipt(func(w http.ResponseWriter, r *http.Request) {
				calls++
				writeJSON(w, 200, map[string]string{"text": "hello"})
			})
			first := httptest.NewRecorder()
			h(first, receiptRequest("request-1", "audio", ct))
			second := httptest.NewRecorder()
			h(second, receiptRequest("request-1", "audio", ct))
			if calls != 1 || first.Body.String() != second.Body.String() || second.Header().Get("Idempotency-Replayed") != "true" || first.Header().Get("X-Transcription-Receipt") != "request-1" {
				t.Fatalf("calls=%d first=%v second=%v", calls, first, second)
			}
			conflict := httptest.NewRecorder()
			h(conflict, receiptRequest("request-1", "different", ct))
			if conflict.Code != 409 || calls != 1 {
				t.Fatalf("conflict=%d calls=%d", conflict.Code, calls)
			}
		})
	}
}
func TestTranscriptionReceiptsCoalesceConcurrentRequests(t *testing.T) {
	var calls atomic.Int32
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h := (&Server{}).transcribeReceipt(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(started)
		<-release
		writeError(w, 503, "unavailable")
	})
	first := httptest.NewRecorder()
	go func() { defer close(done); h(first, receiptRequest("same", "audio", "application/json")) }()
	<-started
	second := httptest.NewRecorder()
	secondDone := make(chan struct{})
	go func() { defer close(secondDone); h(second, receiptRequest("same", "audio", "application/json")) }()
	close(release)
	<-done
	<-secondDone
	if calls.Load() != 1 || first.Code != 503 || second.Code != 503 || first.Body.String() != second.Body.String() {
		t.Fatalf("calls=%d first=%v second=%v", calls.Load(), first, second)
	}
}
func TestTranscriptionReceiptValidationAndGeneratedID(t *testing.T) {
	calls := 0
	h := (&Server{}).transcribeReceipt(func(w http.ResponseWriter, r *http.Request) { calls++; writeJSON(w, 200, struct{}{}) })
	w := httptest.NewRecorder()
	h(w, receiptRequest("bad key", "x", "application/json"))
	if w.Code != 400 || calls != 0 {
		t.Fatal(w.Code, calls)
	}
	w = httptest.NewRecorder()
	h(w, receiptRequest("", "x", "application/json"))
	if w.Code != 200 || w.Header().Get("X-Transcription-Receipt") == "" {
		t.Fatal(w)
	}
}

func TestTranscriptionReceiptBoundsResponsesAndCapacity(t *testing.T) {
	calls := 0
	h := (&Server{}).transcribeReceipt(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
	})
	w := httptest.NewRecorder()
	h(w, receiptRequest("large", "x", "application/json"))
	second := httptest.NewRecorder()
	h(second, receiptRequest("large", "x", "application/json"))
	if w.Code != 500 || second.Body.String() != w.Body.String() || calls != 1 {
		t.Fatal(w.Code, second.Code, calls)
	}
	h = (&Server{}).transcribeReceipt(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, struct{}{}) })
	for i := 0; i < 128; i++ {
		w = httptest.NewRecorder()
		h(w, receiptRequest("", "x", "application/json"))
		if w.Code != 200 {
			t.Fatal(i, w.Code)
		}
	}
	w = httptest.NewRecorder()
	h(w, receiptRequest("overflow", "x", "application/json"))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestTranscriptionReceiptPanicDoesNotStrandRetries(t *testing.T) {
	calls := 0
	h := (&Server{}).transcribeReceipt(func(http.ResponseWriter, *http.Request) { calls++; panic("fixture") })
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h(w, receiptRequest("panic", "x", "application/json"))
		if w.Code != 500 {
			t.Fatal(w.Code)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
