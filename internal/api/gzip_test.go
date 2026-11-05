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
