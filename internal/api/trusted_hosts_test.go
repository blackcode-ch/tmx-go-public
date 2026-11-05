package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

func TestTrustedHostsGuard(t *testing.T) {
	const host = "trial.example.ts.net"
	for _, tc := range []struct {
		name   string
		change func(*http.Request, *Server)
		want   int
	}{
		{"owner happy path", nil, 204},
		{"empty default", func(r *http.Request, s *Server) { s.trustedHosts = nil }, 403},
		{"unlisted host", func(r *http.Request, s *Server) { r.Host = "other.example.ts.net" }, 403},
		{"host suffix", func(r *http.Request, s *Server) { r.Host += ".evil.test" }, 403},
		{"default host port", func(r *http.Request, s *Server) { r.Host += ":443" }, 204},
		{"host case", func(r *http.Request, s *Server) { r.Host = "Trial.example.ts.net" }, 204},
		{"wildcard config", func(r *http.Request, s *Server) {
			s.trustedHosts = []string{"*.example.ts.net"}
			r.Host = "*.example.ts.net"
		}, 403},
		{"LAN peer forged headers", func(r *http.Request, s *Server) {
			r.RemoteAddr = "192.0.2.1:1234"
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
		}, 403},
		{"missing peer", func(r *http.Request, s *Server) { r.RemoteAddr = "" }, 403},
		{"hostname peer", func(r *http.Request, s *Server) { r.RemoteAddr = "localhost:1234" }, 403},
		{"IPv6 loopback", func(r *http.Request, s *Server) { r.RemoteAddr = "[::1]:1234" }, 204},
		{"HTTP proxy", func(r *http.Request, s *Server) { r.Header.Set("X-Forwarded-Proto", "http") }, 403},
		{"missing proto", func(r *http.Request, s *Server) { r.Header.Del("X-Forwarded-Proto") }, 403},
		{"multiple protos", func(r *http.Request, s *Server) { r.Header.Add("X-Forwarded-Proto", "https") }, 403},
		{"proto list", func(r *http.Request, s *Server) { r.Header.Set("X-Forwarded-Proto", "http, https") }, 403},
		{"HTTP origin", func(r *http.Request, s *Server) { r.Header.Set("Origin", "http://"+host) }, 403},
		{"foreign origin", func(r *http.Request, s *Server) { r.Header.Set("Origin", "https://other.example.ts.net") }, 403},
		{"local origin", func(r *http.Request, s *Server) { r.Header.Set("Origin", "http://localhost:8797") }, 403},
		{"Wails origin", func(r *http.Request, s *Server) { r.Header.Set("Origin", "wails://wails") }, 403},
		{"null origin", func(r *http.Request, s *Server) { r.Header.Set("Origin", "null") }, 403},
		{"origin port", func(r *http.Request, s *Server) { r.Header.Set("Origin", "https://"+host+":443") }, 403},
		{"origin path", func(r *http.Request, s *Server) { r.Header.Set("Origin", "https://"+host+"/") }, 403},
		{"duplicate origin", func(r *http.Request, s *Server) { r.Header.Add("Origin", "https://"+host) }, 403},
		{"write without origin", func(r *http.Request, s *Server) { r.Header.Del("Origin") }, 403},
		{"GET without origin", func(r *http.Request, s *Server) { r.Method = "GET"; r.Header.Del("Origin") }, 204},
		{"websocket without origin", func(r *http.Request, s *Server) {
			r.Method = "GET"
			r.Header.Set("Upgrade", "websocket")
			r.Header.Del("Origin")
		}, 403},
		{"websocket origin", func(r *http.Request, s *Server) { r.Method = "GET"; r.Header.Set("Upgrade", "websocket") }, 204},
		{"missing owner token", func(r *http.Request, s *Server) { r.Header.Del("Authorization") }, 401},
		{"invalid token", func(r *http.Request, s *Server) { r.Header.Set("Authorization", "Bearer invalid") }, 401},
		{"query token on HTTP", func(r *http.Request, s *Server) { r.Header.Del("Authorization"); r.URL.RawQuery = "token=test-owner" }, 401},
		{"websocket query token", func(r *http.Request, s *Server) {
			r.Method = "GET"
			r.Header.Set("Upgrade", "websocket")
			r.Header.Del("Authorization")
			r.URL.RawQuery = "token=test-owner"
		}, 401},
		{"websocket header token", func(r *http.Request, s *Server) {
			r.Method = "GET"
			r.Header.Set("Upgrade", "websocket")
			r.Header.Del("Authorization")
			r.Header.Set("Sec-WebSocket-Protocol", "tmx, bearer.test-owner")
		}, 204},
		{"websocket query despite bearer", func(r *http.Request, s *Server) {
			r.Method = "GET"
			r.Header.Set("Upgrade", "websocket")
			r.URL.RawQuery = "token=test-owner"
		}, 401},
		{"preflight", func(r *http.Request, s *Server) { r.Method = "OPTIONS"; r.Header.Del("Authorization") }, 204},
		{"local unchanged", func(r *http.Request, s *Server) {
			r.Host = "localhost:8797"
			r.Header.Del("Origin")
			r.Header.Del("X-Forwarded-Proto")
		}, 204},
		{"remote origin on local host", func(r *http.Request, s *Server) { r.Host = "localhost:8797" }, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{f: &fleet.Fleet{}, trustedHosts: []string{host}, owner: "test-owner", port: 8797}
			r := httptest.NewRequest("POST", "http://"+host+"/api/protected", nil)
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("Origin", "https://"+host)
			r.Header.Set("Authorization", "Bearer test-owner")
			if tc.change != nil {
				tc.change(r, s)
			}
			w := httptest.NewRecorder()
			s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c, ok := caller(r); !ok || !c.Owner {
					t.Error("handler reached without owner")
				}
				w.WriteHeader(204)
			})).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
			if tc.want == 403 && w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("refused request granted CORS")
			}
		})
	}
}

func TestTrustedHostPorts(t *testing.T) {
	const host = "trial.example.ts.net"
	for _, tc := range []struct {
		name, entry, authority, origin string
		want                           int
	}{
		{"listed port", host + ":8443", host + ":8443", "https://" + host + ":8443", 204},
		{"different port", host + ":8443", host + ":8444", "https://" + host + ":8444", 403},
		{"bare request", host + ":8443", host, "https://" + host, 403},
		{"bare entry", host, host + ":8443", "https://" + host + ":8443", 403},
		{"missing origin port", host + ":8443", host + ":8443", "https://" + host, 403},
		{"different origin port", host + ":8443", host + ":8443", "https://" + host + ":8444", 403},
		{"origin path", host + ":8443", host + ":8443", "https://" + host + ":8443/", 403},
		{"origin userinfo", host + ":8443", host + ":8443", "https://user@" + host + ":8443", 403},
		{"origin case", host + ":8443", host + ":8443", "https://TRIAL.example.ts.net:8443", 403},
		{"normalized entry", "TRIAL.example.ts.net:443", host, "https://" + host, 204},
		{"normalized port host", host + ":8443", "TRIAL.example.ts.net:8443", "https://" + host + ":8443", 204},
		{"malformed port", host + ":8443", host + ":08443", "https://" + host + ":08443", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{trustedHosts: []string{tc.entry}, owner: "test-owner"}
			r := httptest.NewRequest("POST", "http://"+host+"/api/protected", nil)
			r.Host = tc.authority
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Authorization", "Bearer test-owner")
			w := httptest.NewRecorder()
			s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
			if tc.want == 403 && w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("refused request granted CORS")
			}
		})
	}
}
