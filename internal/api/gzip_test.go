package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
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
		if !varyHasAcceptEncoding(w.Header()) {
			t.Fatalf("Accept-Encoding %q: Vary %v, want Accept-Encoding", accept, w.Header().Values("Vary"))
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

// varyHasAcceptEncoding reports whether any Vary line lists Accept-Encoding.
func varyHasAcceptEncoding(h http.Header) bool {
	for _, line := range h.Values("Vary") {
		for _, raw := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(raw), "Accept-Encoding") {
				return true
			}
		}
	}
	return false
}

// Plain and gzipped replies are variants of one URL, so both advertise
// Accept-Encoding. Only the gzip request is compressed, and it decompresses
// to the same JSON the plain request returned. The cases with no usable gzip
// fail on the old middleware, which set Vary only after deciding to compress.
func TestGzipVaryPlainAndCompressed(t *testing.T) {
	payload := []map[string]string{{"id": "i-1", "title": "Cache me consistently", "status": "active"}}
	h := gzipped(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, payload) })

	plainReq := httptest.NewRequest(http.MethodGet, "/api/matrix/mine", nil)
	plainRec := httptest.NewRecorder()
	h(plainRec, plainReq)
	plainBody := append([]byte(nil), plainRec.Body.Bytes()...)

	cases := []struct {
		name       string
		accept     string // empty means the request sends no Accept-Encoding header
		compressed bool
	}{
		{name: "no Accept-Encoding", accept: "", compressed: false},
		{name: "br", accept: "br", compressed: false},
		{name: "gzip;q=0", accept: "gzip;q=0", compressed: false},
		{name: "gzip", accept: "gzip", compressed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/matrix/mine", nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			w := httptest.NewRecorder()
			h(w, req)
			if !varyHasAcceptEncoding(w.Header()) {
				t.Fatalf("Vary %v: want Accept-Encoding", w.Header().Values("Vary"))
			}
			if tc.compressed {
				if w.Header().Get("Content-Encoding") != "gzip" {
					t.Fatalf("Content-Encoding %q, want gzip", w.Header().Get("Content-Encoding"))
				}
				zr, err := gzip.NewReader(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(zr)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, plainBody) {
					t.Fatalf("decompressed %q, want the plain JSON %q", got, plainBody)
				}
				return
			}
			if w.Header().Get("Content-Encoding") != "" {
				t.Fatalf("Content-Encoding %q, want none", w.Header().Get("Content-Encoding"))
			}
			if !bytes.Equal(w.Body.Bytes(), plainBody) {
				t.Fatalf("body %q, want the plain JSON %q", w.Body.Bytes(), plainBody)
			}
		})
	}
}

// Vary lines already on the response stay as they are: multiple header lines
// are not joined, an Accept-Encoding token in any letter case is not repeated,
// and Vary: * is not given an extra token.
func TestGzipVaryPreservesExisting(t *testing.T) {
	payload := map[string]string{"ok": "1"}
	plain, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	h := gzipped(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, payload) })

	cases := []struct {
		name       string
		accept     string
		preset     []string
		want       []string
		compressed bool
	}{
		{
			name:   "single token",
			preset: []string{"Origin"},
			want:   []string{"Origin", "Accept-Encoding"},
		},
		{
			name:   "multiple lines",
			preset: []string{"Origin", "Cookie"},
			want:   []string{"Origin", "Cookie", "Accept-Encoding"},
		},
		{
			name:   "combined line",
			preset: []string{"Origin, Cookie"},
			want:   []string{"Origin, Cookie", "Accept-Encoding"},
		},
		{
			name:   "lowercase token",
			preset: []string{"accept-encoding"},
			want:   []string{"accept-encoding"},
		},
		{
			name:   "token among others",
			preset: []string{"Origin, Accept-Encoding"},
			want:   []string{"Origin, Accept-Encoding"},
		},
		{
			name:   "token on a later line",
			preset: []string{"Origin", "ACCEPT-ENCODING"},
			want:   []string{"Origin", "ACCEPT-ENCODING"},
		},
		{
			name:   "wildcard",
			preset: []string{"*"},
			want:   []string{"*"},
		},
		{
			name:   "wildcard on its own line",
			preset: []string{"Origin", "*"},
			want:   []string{"Origin", "*"},
		},
		{
			name:       "wildcard still compresses",
			accept:     "gzip",
			preset:     []string{"*"},
			want:       []string{"*"},
			compressed: true,
		},
		{
			name:       "existing token still compresses",
			accept:     "gzip",
			preset:     []string{"Origin"},
			want:       []string{"Origin", "Accept-Encoding"},
			compressed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/matrix/mine", nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			w := httptest.NewRecorder()
			for _, line := range tc.preset {
				w.Header().Add("Vary", line)
			}
			h(w, req)
			if got := w.Header().Values("Vary"); !slices.Equal(got, tc.want) {
				t.Fatalf("Vary %v, want %v", got, tc.want)
			}
			if tc.compressed {
				if w.Header().Get("Content-Encoding") != "gzip" {
					t.Fatalf("Content-Encoding %q, want gzip", w.Header().Get("Content-Encoding"))
				}
				zr, err := gzip.NewReader(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(zr)
				if err != nil || !bytes.Equal(got, plain) {
					t.Fatalf("decompressed %q, want %q (%v)", got, plain, err)
				}
				return
			}
			if w.Header().Get("Content-Encoding") != "" || !bytes.Equal(w.Body.Bytes(), plain) {
				t.Fatalf("plain response changed: headers %v body %q", w.Header(), w.Body.Bytes())
			}
		})
	}
}

// Upgrade requests stay uncompressed. Already-encoded bodies are not wrapped
// again. 204 and 304 stay empty, and still advertise the encoding variation.
func TestGzipPreservesBypassAndEmptyResponses(t *testing.T) {
	t.Run("upgrade", func(t *testing.T) {
		payload := map[string]string{"ok": "1"}
		plain, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		h := gzipped(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, payload) })
		req := httptest.NewRequest(http.MethodGet, "/api/matrix/mine", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("Upgrade", "websocket")
		w := httptest.NewRecorder()
		h(w, req)
		if w.Header().Get("Content-Encoding") != "" || !bytes.Equal(w.Body.Bytes(), plain) {
			t.Fatalf("upgrade response compressed: headers %v body %q", w.Header(), w.Body.Bytes())
		}
	})

	t.Run("already encoded", func(t *testing.T) {
		const body = "already-encoded"
		h := gzipped(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "br")
			_, _ = w.Write([]byte(body))
		})
		req := httptest.NewRequest(http.MethodGet, "/api/matrix/mine", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h(w, req)
		if w.Header().Get("Content-Encoding") != "br" || w.Body.String() != body {
			t.Fatalf("already-encoded response changed: headers %v body %q", w.Header(), w.Body.String())
		}
		if !varyHasAcceptEncoding(w.Header()) {
			t.Fatalf("Vary %v, want Accept-Encoding", w.Header().Values("Vary"))
		}
	})

	for _, code := range []int{http.StatusNoContent, http.StatusNotModified} {
		for _, accept := range []string{"", "gzip"} {
			t.Run(http.StatusText(code)+" "+accept, func(t *testing.T) {
				h := gzipped(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
				req := httptest.NewRequest(http.MethodGet, "/api/matrix/mine", nil)
				if accept != "" {
					req.Header.Set("Accept-Encoding", accept)
				}
				w := httptest.NewRecorder()
				h(w, req)
				if w.Code != code || w.Header().Get("Content-Encoding") != "" || w.Body.Len() != 0 {
					t.Fatalf("%d Accept-Encoding %q: %d %v %d bytes", code, accept, w.Code, w.Header(), w.Body.Len())
				}
				if !varyHasAcceptEncoding(w.Header()) {
					t.Fatalf("%d Accept-Encoding %q: Vary %v, want Accept-Encoding", code, accept, w.Header().Values("Vary"))
				}
			})
		}
	}
}
