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

func TestGzipPreservesVaryForAllEncodingRequests(t *testing.T) {
	plain := []byte(`{"ok":true}`)
	h := gzipped(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(plain)
	})

	for _, accept := range []string{"", "br", "gzip;q=0", "gzip"} {
		t.Run("accept-"+strings.ReplaceAll(accept, "=", "-"), func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if accept != "" {
				req.Header.Set("Accept-Encoding", accept)
			}
			w := httptest.NewRecorder()
			h(w, req)

			if !strings.EqualFold(w.Header().Get("Vary"), "Accept-Encoding") {
				t.Fatalf("Vary = %q", w.Header().Get("Vary"))
			}
			if accept != "gzip" {
				if got := w.Header().Get("Content-Encoding"); got != "" {
					t.Fatalf("Content-Encoding = %q", got)
				}
				if got := w.Body.Bytes(); string(got) != string(plain) {
					t.Fatalf("body = %q, want %q", got, plain)
				}
				return
			}
			if got := w.Header().Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("Content-Encoding = %q", got)
			}
			zr, err := gzip.NewReader(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(zr)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(plain) {
				t.Fatalf("decompressed body = %q, want %q", got, plain)
			}
		})
	}
}

func TestGzipPreservesFinalVaryHeaders(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(http.Header)
		wantTokens []string
	}{
		{
			name: "set",
			setup: func(header http.Header) {
				header.Set("Vary", "Origin")
			},
			wantTokens: []string{"Origin", "Accept-Encoding"},
		},
		{
			name: "add",
			setup: func(header http.Header) {
				header.Add("Vary", "Cookie")
			},
			wantTokens: []string{"Cookie", "Accept-Encoding"},
		},
		{
			name: "case insensitive existing token",
			setup: func(header http.Header) {
				header.Add("Vary", "accept-encoding")
			},
			wantTokens: []string{"accept-encoding"},
		},
		{
			name: "multiple lines",
			setup: func(header http.Header) {
				header.Add("Vary", "Origin")
				header.Add("Vary", "Cookie")
			},
			wantTokens: []string{"Origin", "Cookie", "Accept-Encoding"},
		},
		{
			name: "wildcard",
			setup: func(header http.Header) {
				header.Set("Vary", "*")
			},
			wantTokens: []string{"*"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := gzipped(func(w http.ResponseWriter, r *http.Request) {
				test.setup(w.Header())
				w.WriteHeader(http.StatusAccepted)
			})
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Accept-Encoding", "br")
			w := httptest.NewRecorder()
			h(w, req)

			var got []string
			for _, value := range w.Header().Values("Vary") {
				for _, token := range strings.Split(value, ",") {
					got = append(got, strings.TrimSpace(token))
				}
			}
			if strings.Join(got, ",") != strings.Join(test.wantTokens, ",") {
				t.Fatalf("Vary tokens = %v, want %v", got, test.wantTokens)
			}
		})
	}
}

func TestGzipPreservesVaryBeforeWrite(t *testing.T) {
	server := httptest.NewServer(gzipped(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Origin")
	}))
	defer server.Close()

	resp, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Values("Vary"); strings.Join(got, ",") != "Origin,Accept-Encoding" {
		t.Fatalf("Vary = %v", got)
	}
}

func TestGzipPreservesEmptyResponses(t *testing.T) {
	for _, code := range []int{http.StatusNoContent, http.StatusNotModified} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			h := gzipped(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			})
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			w := httptest.NewRecorder()
			h(w, req)

			if w.Code != code || w.Body.Len() != 0 || w.Header().Get("Content-Encoding") != "" {
				t.Fatalf("response = %d, %v, %d bytes", w.Code, w.Header(), w.Body.Len())
			}
		})
	}
}

func TestGzipPreservesUpgradeBypass(t *testing.T) {
	h := gzipped(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handler", "reached")
		w.Write([]byte("upgrade"))
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	h(w, req)

	if w.Header().Get("X-Handler") != "reached" || w.Header().Get("Content-Encoding") != "" || w.Header().Get("Vary") != "" {
		t.Fatalf("upgrade response headers = %v", w.Header())
	}
	if got := w.Body.String(); got != "upgrade" {
		t.Fatalf("upgrade body = %q", got)
	}
}

func TestGzipPreservesAlreadyEncodedResponse(t *testing.T) {
	h := gzipped(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		w.Write([]byte("encoded"))
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h(w, req)

	if got := w.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q", got)
	}
	if got := w.Body.String(); got != "encoded" {
		t.Fatalf("body = %q", got)
	}
}
