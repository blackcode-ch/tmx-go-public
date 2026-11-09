package api

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Matrix's first screen is sent gzipped to a client that accepts it, and plain otherwise.
func TestGzippedJSON(t *testing.T) {
	big := make([]map[string]string, 2000)
	for i := range big {
		big[i] = map[string]string{"id": "i-0000000000", "title": "Buy two laptops online for the office", "status": "active"}
	}
	h := gzipped(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, big) })
	plain, _ := json.Marshal(big)

	for _, accept := range []string{"gzip", "br, gzip;q=0.8", "deflate, GZIP"} {
		req := httptest.NewRequest("GET", "/api/matrix/mine", nil)
		req.Header.Set("Accept-Encoding", accept)
		w := httptest.NewRecorder()
		h(w, req)
		if w.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
			t.Fatalf("Accept-Encoding %q: headers %v", accept, w.Header())
		}
		if w.Body.Len()*10 > len(plain) {
			t.Fatalf("Accept-Encoding %q: %d bytes for %d of JSON", accept, w.Body.Len(), len(plain))
		}
		zr, err := gzip.NewReader(w.Body)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(zr)
		if err != nil || strings.TrimSpace(string(got)) != string(plain) {
			t.Fatalf("Accept-Encoding %q: the body doesn't decompress to the JSON (%v)", accept, err)
		}
	}

	for _, accept := range []string{"", "br", "gzip;q=0", "gzip; q=0.0"} {
		req := httptest.NewRequest("GET", "/api/matrix/mine", nil)
		if accept != "" {
			req.Header.Set("Accept-Encoding", accept)
		}
		w := httptest.NewRecorder()
		h(w, req)
		if w.Header().Get("Content-Encoding") != "" || strings.TrimSpace(w.Body.String()) != string(plain) {
			t.Fatalf("Accept-Encoding %q: compressed anyway", accept)
		}
	}

	// No body, no encoding: a 204 stays empty.
	empty := gzipped(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest("DELETE", "/api/matrix/links/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	empty(w, req)
	if w.Code != http.StatusNoContent || w.Header().Get("Content-Encoding") != "" || w.Body.Len() != 0 {
		t.Fatalf("204: %d %v %d bytes", w.Code, w.Header(), w.Body.Len())
	}
}

func splitVaryTokens(h http.Header) []string {
	var out []string
	for _, line := range h.Values("Vary") {
		for _, tok := range strings.Split(line, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				out = append(out, tok)
			}
		}
	}
	return out
}

func countVaryAcceptEncoding(h http.Header) int {
	n := 0
	for _, tok := range splitVaryTokens(h) {
		if strings.EqualFold(tok, "Accept-Encoding") {
			n++
		}
	}
	return n
}

func TestGzipPlainAndCompressedVaryHeader(t *testing.T) {
	payload := []map[string]string{{"id": "i-001", "title": "Preserve cache variation", "status": "active"}}
	h := gzipped(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, payload)
	})
	plainJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		accept     string
		wantGzip   bool
	}{
		{"no Accept-Encoding", "", false},
		{"br encoding", "br", false},
		{"gzip refused q=0", "gzip;q=0", false},
		{"gzip accepted", "gzip", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/matrix/mine", nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			w := httptest.NewRecorder()
			h(w, req)

			if got := countVaryAcceptEncoding(w.Header()); got != 1 {
				t.Fatalf("Accept-Encoding %q: Vary=%v, want 1 Accept-Encoding token, got %d", tc.accept, w.Header().Values("Vary"), got)
			}

			if tc.wantGzip {
				if got := w.Header().Get("Content-Encoding"); got != "gzip" {
					t.Fatalf("Content-Encoding = %q, want gzip", got)
				}
				zr, err := gzip.NewReader(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(zr)
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(string(body)) != string(plainJSON) {
					t.Fatalf("decompressed JSON %q != plain JSON %q", body, plainJSON)
				}
			} else {
				if got := w.Header().Get("Content-Encoding"); got != "" {
					t.Fatalf("Content-Encoding = %q, want empty on plain response", got)
				}
				if strings.TrimSpace(w.Body.String()) != string(plainJSON) {
					t.Fatalf("plain JSON %q != expected %q", w.Body.String(), plainJSON)
				}
			}
		})
	}
}

func TestGzipExistingVaryPreservation(t *testing.T) {
	cases := []struct {
		name       string
		presetVary []string
		accept     string
		wantTokens []string
		wantAE     int
	}{
		{"single existing token plain", []string{"Origin"}, "", []string{"Origin", "Accept-Encoding"}, 1},
		{"single existing token gzip", []string{"Origin"}, "gzip", []string{"Origin", "Accept-Encoding"}, 1},
		{"multiple Vary header lines", []string{"Origin", "Accept-Language"}, "", []string{"Origin", "Accept-Language", "Accept-Encoding"}, 1},
		{"comma separated Vary line", []string{"Origin, Cookie"}, "", []string{"Origin", "Cookie", "Accept-Encoding"}, 1},
		{"duplicate lowercase accept-encoding", []string{"accept-encoding"}, "gzip", []string{"accept-encoding"}, 1},
		{"duplicate mixed-case among tokens", []string{"Origin, ACCEPT-ENCODING"}, "", []string{"Origin", "ACCEPT-ENCODING"}, 1},
		{"wildcard Vary preserved", []string{"*"}, "", []string{"*"}, 0},
		{"wildcard Vary gzip preserved", []string{"*"}, "gzip", []string{"*"}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := gzipped(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
			})
			req := httptest.NewRequest("GET", "/api/matrix/mine", nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			w := httptest.NewRecorder()
			for _, v := range tc.presetVary {
				w.Header().Add("Vary", v)
			}
			h(w, req)

			got := splitVaryTokens(w.Header())
			if len(got) != len(tc.wantTokens) {
				t.Fatalf("Vary tokens = %v, want %v", got, tc.wantTokens)
			}
			for i := range tc.wantTokens {
				if got[i] != tc.wantTokens[i] {
					t.Fatalf("Vary tokens[%d] = %q, want %q (all: %v)", i, got[i], tc.wantTokens[i], got)
				}
			}
			if ae := countVaryAcceptEncoding(w.Header()); ae != tc.wantAE {
				t.Fatalf("Accept-Encoding count = %d, want %d (Vary: %v)", ae, tc.wantAE, w.Header().Values("Vary"))
			}
		})
	}
}

func TestGzipPreservesUpgradeAlreadyEncodedAndEmptyResponses(t *testing.T) {
	t.Run("upgrade bypass", func(t *testing.T) {
		h := gzipped(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"upgrade": "ok"})
		})
		req := httptest.NewRequest("GET", "/api/matrix/mine", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("Upgrade", "websocket")
		w := httptest.NewRecorder()
		h(w, req)
		if w.Header().Get("Content-Encoding") != "" {
			t.Fatalf("expected unencoded upgrade response, got Content-Encoding=%q", w.Header().Get("Content-Encoding"))
		}
	})

	t.Run("already encoded response", func(t *testing.T) {
		h := gzipped(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "br")
			_, _ = w.Write([]byte("prebuilt-br-payload"))
		})
		req := httptest.NewRequest("GET", "/api/matrix/mine", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h(w, req)
		if w.Header().Get("Content-Encoding") != "br" || w.Body.String() != "prebuilt-br-payload" {
			t.Fatalf("already-encoded response altered: headers=%v body=%q", w.Header(), w.Body.String())
		}
		if countVaryAcceptEncoding(w.Header()) != 1 {
			t.Fatalf("Vary=%v, want 1 Accept-Encoding token", w.Header().Values("Vary"))
		}
	})

	for _, status := range []int{http.StatusNoContent, http.StatusNotModified} {
		for _, accept := range []string{"", "gzip"} {
			req := httptest.NewRequest("GET", "/api/matrix/mine", nil)
			if accept != "" {
				req.Header.Set("Accept-Encoding", accept)
			}
			w := httptest.NewRecorder()
			h := gzipped(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			})
			h(w, req)
			if w.Code != status || w.Header().Get("Content-Encoding") != "" || w.Body.Len() != 0 {
				t.Fatalf("status %d accept %q: code=%d headers=%v len=%d", status, accept, w.Code, w.Header(), w.Body.Len())
			}
			if countVaryAcceptEncoding(w.Header()) != 1 {
				t.Fatalf("status %d accept %q: Vary=%v missing Accept-Encoding", status, accept, w.Header().Values("Vary"))
			}
		}
	}
}