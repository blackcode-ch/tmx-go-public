package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/coder/websocket"
)

func TestTrustedHostPreservesCompanionBoundary(t *testing.T) {
	d := apitest.New(t)
	link := linkClient(t, d)
	s := api.New(api.Options{Fleet: d.Fleet, OwnerToken: d.OwnerToken, Companion: d.Companion, TrustedHosts: []string{"trial.example.ts.net"}})
	for _, tc := range []struct {
		host, header string
		want         int
	}{
		{"trial.example.ts.net", "X-Forwarded-Proto", 403},
		{"localhost", "Forwarded", 403},
		{"localhost", "X-Forwarded-For", 403},
		{"localhost", "X-Forwarded-Host", 403},
		{"localhost", "", 200},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/api/companion/link/fleet", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+link.Token)
		if tc.header != "" {
			r.Header.Set(tc.header, "https")
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.host, tc.header, w.Code, tc.want)
		}
	}
}

func TestTrustedHostWebSocketHeaderHandshake(t *testing.T) {
	d := apitest.New(t)
	s := api.New(api.Options{Fleet: d.Fleet, OwnerToken: d.OwnerToken, TrustedHosts: []string{"trial.example.ts.net"}})
	// Simulate the TLS-terminating proxy, preserving the external Host.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "trial.example.ts.net"
		r.Header.Set("X-Forwarded-Proto", "https")
		s.Handler().ServeHTTP(w, r)
	}))
	defer proxy.Close()
	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, strings.Replace(proxy.URL, "http:", "ws:", 1)+"/api/events", &websocket.DialOptions{
		HTTPHeader:   http.Header{"Origin": []string{"https://trial.example.ts.net"}},
		Subprotocols: []string{"tmx", "bearer." + d.OwnerToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != "tmx" {
		t.Fatal("server must select only the public protocol")
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
}
