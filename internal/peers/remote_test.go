package peers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

func TestRemoteWireAndErrors(t *testing.T) {
	status, response := 201, `{"id":"receipt"}`
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get(HeaderOnBehalf) != "agent:worker" || r.Header.Get(HeaderFrom) != "hostc" || r.Header.Get(HeaderToken) == "" {
			t.Error("missing authenticated provenance")
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"id":"123456789abc"}` || r.URL.RequestURI() != "/api/tasks?view=1" || r.Method != "POST" {
			t.Error("request changed")
		}
		if r.Header.Get("X-TMX-Presence-Fresh") != "" {
			t.Error("invented owner presence")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	defer srv.Close()
	c := New(config.Config{Profile: config.ProfileProduction, Machine: "hostc", Peers: []config.Peer{{Name: "hosta", URL: srv.URL}}}, config.Paths{Home: t.TempDir()}, nil)
	c.SetTransport(srv.Client().Transport)
	if _, err := c.Tokens().Store("hosta", NewPairToken(), "test"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status int
		body   string
		want   int
		code   string
	}{
		{201, `{"id":"receipt"}`, 200, ""},
		{202, `{"state":"queued"}`, 200, ""},
		{409, `{"error":"conflict","code":"task_conflict","extra":1}`, 409, "task_conflict"},
		{403, `{"error":"policy","code":"forbidden"}`, 403, "forbidden"},
		{401, `{"error":"pair token"}`, 502, "peer_auth_failed"},
		{403, `{"error":"pair token","code":"pair_required"}`, 502, "peer_auth_failed"},
		{403, `{"error":"pair token","code":"not_approved"}`, 502, "peer_auth_failed"},
		{503, `{"error":"busy","code":"busy"}`, 503, "busy"},
		{200, `not json`, 502, "peer_response_invalid"},
		{200, `"` + strings.Repeat("a", maxReply) + `"`, 502, "peer_response_invalid"},
	} {
		status, response = tc.status, tc.body
		got, body := c.Remote(context.Background(), "HOSTA", "post", "/api/tasks?view=1", "agent:worker", json.RawMessage(`{"id":"123456789abc"}`))
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		if got != tc.want || (tc.code != "" && out["code"] != tc.code) {
			t.Fatalf("status %d: got %d %s", status, got, body)
		}
	}
	before := calls
	for _, path := range []string{"https://evil/api/tasks", "//evil/api/tasks", "/other", "/api/tasks\r\nX: y"} {
		if got, _ := c.Remote(context.Background(), "hosta", "GET", path, "operator", nil); got != 400 {
			t.Errorf("path %q: %d", path, got)
		}
	}
	if got, _ := c.Remote(context.Background(), "absent", "GET", "/api/tasks", "operator", nil); got != 404 {
		t.Fatal(got)
	}
	if calls != before {
		t.Fatal("invalid request reached peer")
	}
}

func TestRemoteNeverFollowsRedirect(t *testing.T) {
	reached := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer dest.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", dest.URL)
		w.WriteHeader(302)
		_, _ = io.WriteString(w, `{"error":"redirect"}`)
	}))
	defer srv.Close()
	c := New(config.Config{Profile: config.ProfileProduction, Machine: "hostc", Peers: []config.Peer{{Name: "hosta", URL: srv.URL}}}, config.Paths{Home: t.TempDir()}, nil)
	if _, err := c.Tokens().Store("hosta", NewPairToken(), "test"); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Remote(context.Background(), "hosta", "GET", "/api/tasks", "operator", nil); got != 302 || reached {
		t.Fatal("followed redirect", got)
	}
}
