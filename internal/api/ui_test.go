package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

// fixtureUI mimics the shape of a real ui/dist build closely enough to exercise serveUI.
func fixtureUI() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html><title>tmx</title>")},
		"manifest.webmanifest":   {Data: []byte(`{"name":"Tiamatrix"}`)},
		"icon-192.png":           {Data: []byte("\x89PNG\r\n\x1a\n" + "fake-192")},
		"icon-512.png":           {Data: []byte("\x89PNG\r\n\x1a\n" + "fake-512")},
		"icon-512-maskable.png":  {Data: []byte("\x89PNG\r\n\x1a\n" + "fake-512-maskable")},
		"apple-touch-icon.png":   {Data: []byte("\x89PNG\r\n\x1a\n" + "fake-180")},
		"assets/index-abc123.js": {Data: []byte("console.log('hi')")},
	}
}

func TestServeUIManifestAndIcons(t *testing.T) {
	h := serveUI(fixtureUI(), true)
	for _, tc := range []struct {
		path string
		ct   string
	}{
		{"/manifest.webmanifest", "application/manifest+json"},
		{"/icon-192.png", "image/png"},
		{"/icon-512.png", "image/png"},
		{"/icon-512-maskable.png", "image/png"},
		{"/apple-touch-icon.png", "image/png"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("status %d, want 200", w.Code)
			}
			if ct := w.Header().Get("Content-Type"); ct != tc.ct {
				t.Fatalf("Content-Type %q, want %q", ct, tc.ct)
			}
		})
	}
}

// TestUIAssetsReachUnauthenticated proves the manifest and icons pass through the real auth guard
// without a token: Chrome fetches them unauthenticated when checking installability.
func TestUIAssetsReachUnauthenticated(t *testing.T) {
	const host = "trial.example.ts.net"
	s := &Server{f: &fleet.Fleet{}, trustedHosts: []string{host}, owner: "test-owner", port: 8797}
	guarded := s.guard(serveUI(fixtureUI(), true))
	for _, path := range []string{"/manifest.webmanifest", "/icon-192.png", "/icon-512.png", "/icon-512-maskable.png"} {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+host+path, nil)
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("X-Forwarded-Proto", "https")
			// No Authorization header: this is the unauthenticated fetch Chrome makes.
			w := httptest.NewRecorder()
			guarded.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("status %d, want 200 (manifest/icons must be reachable without login)", w.Code)
			}
		})
	}
	t.Run("/api/ still requires a token", func(t *testing.T) {
		r := httptest.NewRequest("GET", "http://"+host+"/api/snapshot", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("status %d, want 401", w.Code)
		}
	})
}
