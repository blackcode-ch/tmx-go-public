package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/peers"
)

func TestCompanionLocalSessionsAndFleetETag(t *testing.T) {
	d := apitest.New(t)
	link := linkClient(t, d)
	var sessions struct {
		Host     string          `json:"host"`
		Sessions []peers.Session `json:"sessions"`
	}
	if err := link.Get(context.Background(), "/api/sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	if sessions.Host == "" || len(sessions.Sessions) != 1 || sessions.Sessions[0].Rooms == nil || sessions.Sessions[0].Tier == "" {
		t.Fatalf("local sessions: %+v", sessions)
	}
	if status, _ := call(t, tc.New(d.URL, ""), "GET", "/api/sessions", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous sessions: %d", status)
	}
	token, err := config.CompanionToken(config.Paths{Home: d.Home}, false)
	if err != nil {
		t.Fatal(err)
	}
	get := func(etag string) (int, string, int) {
		t.Helper()
		req, _ := http.NewRequest("GET", d.URL+"/api/fleet", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, res.Header.Get("ETag"), len(body)
	}
	status, etag, length := get("")
	if status != 200 || etag == "" || length == 0 {
		t.Fatalf("first: %d %q %d", status, etag, length)
	}
	if status, got, length := get("W/" + etag); status != 304 || got != etag || length != 0 {
		t.Fatalf("unchanged: %d %q %d", status, got, length)
	}
	role := "changed roster role"
	if _, err := d.Fleet.UpdateAgent(fleet.OwnerCaller, "head", fleet.AgentPatch{Role: &role}); err != nil {
		t.Fatal(err)
	}
	if status, got, length := get(etag); status != 200 || got == etag || length == 0 {
		t.Fatalf("changed: %d %q %d", status, got, length)
	}
}

func TestCompanionFleetRosterThreeHosts(t *testing.T) {
	d := apitest.New(t)
	servers := []*httptest.Server{}
	cfg := config.Config{Profile: config.ProfileProduction, Machine: "hostb"}
	for _, host := range []string{"hosta", "hostc"} {
		host := host
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"host": host, "supervisor": map[string]string{"name": "lead", "host": host},
				"sessions": []map[string]any{{"id": d.Fleet.HeadID(), "name": "worker", "host": "forged", "state": "idle", "tier": "worker", "cwd": "/work", "rooms": []string{"room-one", "room-two"}}}})
		}))
		t.Cleanup(srv.Close)
		servers = append(servers, srv)
		cfg.Peers = append(cfg.Peers, config.Peer{Name: host, URL: srv.URL})
	}
	pc := peers.New(cfg, config.Paths{Home: d.Home}, nil)
	pc.SetTransport(servers[0].Client().Transport)
	for _, host := range []string{"hosta", "hostc"} {
		if _, err := pc.Tokens().Store(host, peers.NewPairToken(), "test"); err != nil {
			t.Fatal(err)
		}
	}
	pc.PollAll(context.Background())
	base := d.WithPeers(t, pc)
	token, err := config.CompanionToken(config.Paths{Home: d.Home}, true)
	if err != nil {
		t.Fatal(err)
	}
	link := tc.New(base, token)
	var roster struct {
		Host       string               `json:"host"`
		Hosts      []map[string]any     `json:"hosts"`
		Sessions   []peers.Session      `json:"sessions"`
		Supervisor *peers.SupervisorRef `json:"supervisor"`
	}
	if err := link.Get(context.Background(), "/api/fleet", &roster); err != nil {
		t.Fatal(err)
	}
	if roster.Host != "hostb" || len(roster.Hosts) != 3 || roster.Supervisor == nil || roster.Supervisor.Host != "hostb" {
		t.Fatalf("roster: %+v", roster)
	}
	seen := map[string]int{}
	for _, a := range roster.Sessions {
		seen[a.Host]++
		if a.Rooms == nil || a.ID == "" {
			t.Fatalf("missing fields: %+v", a)
		}
		if a.Host != "hostb" && (len(a.Rooms) != 2 || a.Name != "worker" || a.ID != "worker@"+a.Host) {
			t.Fatalf("peer fields: %+v", a)
		}
	}
	if seen["hostb"] != 1 || seen["hosta"] != 1 || seen["hostc"] != 1 {
		t.Fatalf("hosts: %v", seen)
	}
	var view struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := link.Get(context.Background(), "/api/companion/link/fleet", &view); err != nil || len(view.Agents) != 3 {
		t.Fatalf("link: %+v %v", view, err)
	}
	for _, a := range view.Agents {
		if a["host"] != a["machine"] || a["rooms"] == nil {
			t.Fatalf("link identity: %v", a)
		}
	}
	if status, _ := call(t, tc.New(base, ""), "GET", "/api/fleet", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", status)
	}
	if status, _ := call(t, link, "POST", "/api/fleet", nil, nil); status != http.StatusForbidden {
		t.Fatalf("write: %d", status)
	}
	// A disconnected peer keeps its cached sessions but is explicitly offline.
	servers[1].Close()
	pc.PollAll(context.Background())
	if err := link.Get(context.Background(), "/api/fleet", &roster); err != nil {
		t.Fatal(err)
	}
	if len(roster.Sessions) != 3 || roster.Hosts[2]["online"] != false {
		t.Fatalf("stale roster: %+v", roster)
	}
}

func TestPeerSessionsSupervisorObject(t *testing.T) {
	s := newPeerSetup(t)
	var result struct {
		Supervisor *peers.SupervisorRef `json:"supervisor"`
		Sessions   []peers.Session      `json:"sessions"`
	}
	if status := peerDo(t, s.url, "GET", "/api/sessions", "hosta", s.pair, nil, nil, &result); status != 200 {
		t.Fatal(status)
	}
	if result.Supervisor == nil || result.Supervisor.Host != "hostb" {
		t.Fatalf("supervisor: %+v", result.Supervisor)
	}
	if len(result.Sessions) != 1 || result.Sessions[0].Rooms == nil {
		t.Fatalf("sessions: %+v", result.Sessions)
	}
}
