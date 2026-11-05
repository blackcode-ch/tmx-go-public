package api

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
)

const transcriptionReceiptTTL = 10 * time.Minute

var transcriptionKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Bound retained response memory as well as the number of receipts.
type transcriptionResponse struct {
	header   http.Header
	code     int
	body     bytes.Buffer
	tooLarge bool
}

func (r *transcriptionResponse) Header() http.Header { return r.header }
func (r *transcriptionResponse) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}
func (r *transcriptionResponse) Write(p []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	if r.tooLarge || len(p) > (1<<20)-r.body.Len() {
		r.tooLarge = true
		return 0, errors.New("transcription response too large")
	}
	return r.body.Write(p)
}

type transcriptionReceipt struct {
	fingerprint string
	done        chan struct{}
	finished    time.Time
	code        int
	body        []byte
	contentType string
}

// transcribeReceipt coalesces concurrent retries and replays their response, including
// failures. Receipts live for ten minutes in this server process (not across restarts).
// Capacity is bounded; active/unexpired receipts are never evicted to admit new work.
// The owner-only wrapper must run first. Audio is spooled privately and removed, never
// retained in the receipt. Use the same Idempotency-Key and identical body on retries.
func (s *Server) transcribeReceipt(next http.HandlerFunc) http.HandlerFunc {
	var mu sync.Mutex
	receipts := map[string]*transcriptionReceipt{}
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			key = model.NewID()
		}
		if !transcriptionKey.MatchString(key) {
			writeError(w, http.StatusBadRequest, "invalid Idempotency-Key")
			return
		}
		w.Header().Set("Idempotency-Key", key)
		r.Header.Set("Idempotency-Key", key)
		w.Header().Set("X-Transcription-Receipt", key)
		w.Header().Set("Cache-Control", "no-store")
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		limit := int64(maxDictationBody)
		if strings.HasPrefix(ct, "audio/") || ct == "application/octet-stream" {
			limit = transcribe.MaxFileBytes
		}
		f, err := os.CreateTemp("", "tmx-transcription-*")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot prepare transcription")
			return
		}
		defer os.Remove(f.Name())
		defer f.Close()
		hash := sha256.New()
		fmt.Fprintf(hash, "%s\x00%s\x00%t\x00", ct, r.URL.RawQuery, r.Header.Get(transcriptionLocalOnly) != "")
		n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(r.Body, limit+1))
		if n > limit {
			writeError(w, http.StatusRequestEntityTooLarge, "transcription body is too large")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "cannot read transcription")
			return
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot read transcription")
			return
		}
		fingerprint := fmt.Sprintf("%x", hash.Sum(nil))
		mu.Lock()
		for id, receipt := range receipts {
			if !receipt.finished.IsZero() && time.Since(receipt.finished) >= transcriptionReceiptTTL {
				delete(receipts, id)
			}
		}
		if receipt, ok := receipts[key]; ok {
			mu.Unlock()
			if receipt.fingerprint != fingerprint {
				writeError(w, http.StatusConflict, "Idempotency-Key already used for different audio or options")
				return
			}
			select {
			case <-receipt.done:
				w.Header().Set("Idempotency-Replayed", "true")
				w.Header().Set("Content-Type", receipt.contentType)
				w.WriteHeader(receipt.code)
				_, _ = w.Write(receipt.body)
			case <-r.Context().Done():
				writeError(w, http.StatusServiceUnavailable, "the transcription was stopped")
			}
			return
		}
		if len(receipts) >= 128 {
			mu.Unlock()
			writeError(w, http.StatusServiceUnavailable, "transcription receipts are full; try later")
			return
		}
		receipt := &transcriptionReceipt{fingerprint: fingerprint, done: make(chan struct{})}
		receipts[key] = receipt
		mu.Unlock()
		r.Body = io.NopCloser(f)
		rr := &transcriptionResponse{header: make(http.Header)}
		func() {
			defer func() {
				if recover() != nil {
					rr = &transcriptionResponse{header: make(http.Header)}
					writeError(rr, http.StatusInternalServerError, "transcription could not complete")
					if s.log != nil {
						s.log.Error("transcription handler panicked")
					}
				}
			}()
			next(rr, r)
		}()
		if rr.tooLarge {
			rr = &transcriptionResponse{header: make(http.Header)}
			writeError(rr, http.StatusInternalServerError, "transcription response exceeds receipt limit")
		}
		if rr.code == 0 {
			rr.code = http.StatusOK
		}
		mu.Lock()
		receipt.code, receipt.body, receipt.contentType = rr.code, rr.body.Bytes(), rr.Header().Get("Content-Type")
		receipt.finished = time.Now()
		close(receipt.done)
		mu.Unlock()
		w.Header().Set("Content-Type", receipt.contentType)
		w.WriteHeader(receipt.code)
		_, _ = w.Write(receipt.body)
	}
}
