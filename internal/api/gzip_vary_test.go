package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestGzipVaryResponses(t *testing.T) {
	const body = "{\"ok\":true}\n"
	tests := []struct {
		name, accept string
		before, handlerVary []string
		wantVary []string
		compressed bool
	}{
		{name: "plain no accept", wantVary: []string{"Accept-Encoding"}},
		{name: "plain br", accept: "br", wantVary: []string{"Accept-Encoding"}},
		{name: "plain q zero", accept: "gzip;q=0", wantVary: []string{"Accept-Encoding"}},
		{name: "gzip", accept: "gzip", wantVary: []string{"Accept-Encoding"}, compressed: true},
		{name: "existing lines", before: []string{"Origin", "Accept-Language, X-Test"}, wantVary: []string{"Origin", "Accept-Language, X-Test", "Accept-Encoding"}},
		{name: "existing lines gzip", accept: "gzip", before: []string{"Origin", "Accept-Language"}, wantVary: []string{"Origin", "Accept-Language", "Accept-Encoding"}, compressed: true},
		{name: "existing case fold", before: []string{"Origin", "acCEPT-enCODING, X-Test"}, wantVary: []string{"Origin", "acCEPT-enCODING, X-Test"}},
		{name: "wildcard", before: []string{"*"}, wantVary: []string{"*"}},
		{name: "handler Vary", handlerVary: []string{"Origin"}, wantVary: []string{"Origin", "Accept-Encoding"}},
		{name: "handler gzip dedupe", accept: "gzip", handlerVary: []string{"X-Test, ACCEPT-encoding"}, wantVary: []string{"X-Test, ACCEPT-encoding"}, compressed: true},
		{name: "handler wildcard", accept: "gzip", handlerVary: []string{"*"}, wantVary: []string{"*"}, compressed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := gzipped(func(w http.ResponseWriter, _ *http.Request) {
				for _, v := range tt.handlerVary {
					w.Header().Add("Vary", v)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.accept != "" {
				req.Header.Set("Accept-Encoding", tt.accept)
			}
			rec := httptest.NewRecorder()
			for _, v := range tt.before {
				rec.Header().Add("Vary", v)
			}
			h(rec, req)
			res := rec.Result()
			defer res.Body.Close()
			if got := res.Header.Values("Vary"); !reflect.DeepEqual(got, tt.wantVary) {
				t.Fatalf("Vary = %q, want %q", got, tt.wantVary)
			}
			if compressed := res.Header.Get("Content-Encoding") == "gzip"; compressed != tt.compressed {
				t.Fatalf("compressed = %v, want %v", compressed, tt.compressed)
			}
			var read io.Reader = res.Body
			if tt.compressed {
				z, err := gzip.NewReader(res.Body)
				if err != nil {
					t.Fatal(err)
				}
				defer z.Close()
				read = z
			}
			got, err := io.ReadAll(read)
			if err != nil || string(got) != body {
				t.Fatalf("decoded body = %q, error = %v, want %q", got, err, body)
			}
		})
	}
}

func TestGzipVarySpecialResponses(t *testing.T) {
	for _, code := range []int{http.StatusNoContent, http.StatusNotModified} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			h := gzipped(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Add("Vary", "Origin")
				w.WriteHeader(code)
			})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != code || rec.Body.Len() != 0 || rec.Header().Get("Content-Encoding") != "" {
				t.Fatalf("status=%d length=%d headers=%v", rec.Code, rec.Body.Len(), rec.Header())
			}
			if got := rec.Result().Header.Values("Vary"); !reflect.DeepEqual(got, []string{"Origin", "Accept-Encoding"}) {
				t.Fatalf("Vary=%q", got)
			}
		})
	}
	t.Run("already encoded", func(t *testing.T) {
		h := gzipped(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "br")
			_, _ = io.WriteString(w, "preencoded")
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Header().Get("Content-Encoding") != "br" || rec.Body.String() != "preencoded" {
			t.Fatalf("unexpected encoded response: %v %q", rec.Header(), rec.Body.String())
		}
		if got := rec.Result().Header.Get("Vary"); got != "Accept-Encoding" {
			t.Fatalf("Vary=%q", got)
		}
	})
	t.Run("Upgrade bypass", func(t *testing.T) {
		h := gzipped(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "upgrade")
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("Upgrade", "websocket")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Header().Get("Vary") != "" || rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "upgrade" {
			t.Fatalf("upgrade changed: %v %q", rec.Header(), rec.Body.String())
		}
	})
}
