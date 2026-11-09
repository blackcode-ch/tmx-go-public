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

// varyTokens flattens every Vary header line into its comma-separated tokens.
func varyTokens(h http.Header) []string {
	var tokens []string
	for _, line := range h.Values("Vary") {
		for _, token := range strings.Split(line, ",") {
			if token = strings.TrimSpace(token); token != "" {
				tokens = append(tokens, token)
			}
		}
	}
	return tokens
}

func countVaryToken(h http.Header, want string) int {
	n := 0
	for _, token := range varyTokens(h) {
		if strings.EqualFold(token, want) {
			n++
		}
	}
	return n
}

// Every response of a wrapped handler varies with Accept-Encoding: clients
// that do not accept gzip get plain JSON for the same URL that gzip clients
// get compressed, so plain responses must advertise the variation too.
func TestGzipVaryAdvertisedOnPlainAndGzipResponses(t *testing.T) {
	payload := []map[string]string{{"id": "i-1", "title": "Cache me consistently", "status": "active"}}
	h := gzipped(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, payload) })
	plain, _ := json.Marshal(payload)

	cases := []struct {
		name       string
		accept     string // "" sends no Accept-Encoding header at all
		compressed bool
	}{
		{"no Accept-Encoding", "", false},
		{"br only", "br", false},
		{"gzip refused", "gzip;q=0", false},
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
			if countVaryToken(w.Header(), "Accept-Encoding") != 1 {
				t.Fatalf("Vary %v: want exactly one Accept-Encoding token", w.Header().Values("Vary"))
			}
			if tc.compressed {
				if w.Header().Get("Content-Encoding") != "gzip" {
					t.Fatalf("Content-Encoding %q: want gzip", w.Header().Get("Content-Encoding"))
				}
				zr, err := gzip.NewReader(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(zr)
				if err != nil || strings.TrimSpace(string(got)) != string(plain) {
					t.Fatalf("decompressed body %q does not match the plain JSON (%v)", got, err)
				}
			} else {
				if w.Header().Get("Content-Encoding") != "" {
					t.Fatalf("Content-Encoding %q: want none for a plain response", w.Header().Get("Content-Encoding"))
				}
				if strings.TrimSpace(w.Body.String()) != string(plain) {
					t.Fatalf("plain body %q does not match the JSON served to gzip clients", w.Body.String())
				}
			}
		})
	}
}

// Vary tokens that are already present survive untouched: no duplicates of
// Accept-Encoding in any letter case, multiple Vary lines keep all of their
// tokens, and a wildcard Vary is left alone.
func TestGzipVaryPreservesExistingTokens(t *testing.T) {
	h := gzipped(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
	})

	cases := []struct {
		name       string
		preset     []string // Vary lines set before the handler runs
		wantTokens []string
		wantAE     int
	}{
		{"single existing token", []string{"Origin"}, []string{"Origin", "Accept-Encoding"}, 1},
		{"multiple Vary lines", []string{"Origin", "Cookie"}, []string{"Origin", "Cookie", "Accept-Encoding"}, 1},
		{"combined line", []string{"Origin, Cookie"}, []string{"Origin", "Cookie", "Accept-Encoding"}, 1},
		{"already listed lowercase", []string{"accept-encoding"}, []string{"accept-encoding"}, 1},
		{"already listed among tokens", []string{"Origin, Accept-Encoding"}, []string{"Origin", "Accept-Encoding"}, 1},
		{"wildcard", []string{"*"}, []string{"*"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/matrix/mine", nil)
			w := httptest.NewRecorder()
			for _, line := range tc.preset {
				w.Header().Add("Vary", line)
			}
			h(w, req)
			got := varyTokens(w.Header())
			if len(got) != len(tc.wantTokens) {
				t.Fatalf("Vary tokens %v: want %v", got, tc.wantTokens)
			}
			for i, want := range tc.wantTokens {
				if got[i] != want {
					t.Fatalf("Vary tokens %v: want %v", got, tc.wantTokens)
				}
			}
			if n := countVaryToken(w.Header(), "Accept-Encoding"); n != tc.wantAE {
				t.Fatalf("Accept-Encoding appears %d times in %v: want %d", n, got, tc.wantAE)
			}
		})
	}
}

// The Upgrade bypass is unchanged: upgrade requests are never compressed.
func TestGzipUpgradeBypassPreserved(t *testing.T) {
	payload := map[string]string{"ok": "1"}
	h := gzipped(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, payload) })
	plain, _ := json.Marshal(payload)

	req := httptest.NewRequest("GET", "/api/matrix/mine", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Header().Get("Content-Encoding") != "" || strings.TrimSpace(w.Body.String()) != string(plain) {
		t.Fatalf("upgrade response was compressed: headers %v body %q", w.Header(), w.Body.String())
	}
}

// A response the handler already encoded is passed through untouched.
func TestGzipAlreadyEncodedResponsePreserved(t *testing.T) {
	h := gzipped(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		_, _ = w.Write([]byte("already-encoded"))
	})

	req := httptest.NewRequest("GET", "/api/matrix/mine", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Header().Get("Content-Encoding") != "br" || w.Body.String() != "already-encoded" {
		t.Fatalf("already-encoded response changed: headers %v body %q", w.Header(), w.Body.String())
	}
	if countVaryToken(w.Header(), "Accept-Encoding") != 1 {
		t.Fatalf("Vary %v: want exactly one Accept-Encoding token", w.Header().Values("Vary"))
	}
}

// Empty responses stay empty and unencoded, on plain and gzip requests alike.
func TestGzipEmptyResponsesStayEmpty(t *testing.T) {
	for _, code := range []int{http.StatusNoContent, http.StatusNotModified} {
		for _, accept := range []string{"", "gzip"} {
			h := gzipped(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
			req := httptest.NewRequest("GET", "/api/matrix/mine", nil)
			if accept != "" {
				req.Header.Set("Accept-Encoding", accept)
			}
			w := httptest.NewRecorder()
			h(w, req)
			if w.Code != code || w.Header().Get("Content-Encoding") != "" || w.Body.Len() != 0 {
				t.Fatalf("%d with Accept-Encoding %q: %d %v %d bytes", code, accept, w.Code, w.Header(), w.Body.Len())
			}
			if countVaryToken(w.Header(), "Accept-Encoding") != 1 {
				t.Fatalf("%d with Accept-Encoding %q: Vary %v lacks Accept-Encoding", code, accept, w.Header().Values("Vary"))
			}
		}
	}
}
