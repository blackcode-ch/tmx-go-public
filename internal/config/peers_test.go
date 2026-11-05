package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPeerURL(t *testing.T) {
	for _, bad := range []string{"http://hosta.example.ts.net", "https://100.64.1.2", "https://192.168.1.3:8777",
		"https://u:p@hosta.example.ts.net", "https://hosta.example.ts.net/api", "https://hosta.example.ts.net?x=1",
		"https://hosta", "hosta.example.ts.net", ""} {
		if _, err := PeerURL(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for raw, want := range map[string]string{
		"https://Hosta.Example.ts.net":       "https://hosta.example.ts.net",
		"https://hosta.example.ts.net/":      "https://hosta.example.ts.net",
		"https://hosta.example.ts.net:443":   "https://hosta.example.ts.net",
		"https://hosta.example.ts.net:10443": "https://hosta.example.ts.net:10443",
	} {
		if got, err := PeerURL(raw); err != nil || got != want {
			t.Errorf("%q = %q, %v", raw, got, err)
		}
	}
}

func TestPeersNeedProduction(t *testing.T) {
	load := func(cfg map[string]any) (Config, error) {
		p := testPaths(t)
		t.Setenv(EnvPort, "")
		b, _ := json.Marshal(cfg)
		if err := os.WriteFile(p.ConfigFile(), b, 0600); err != nil {
			t.Fatal(err)
		}
		return Load(p)
	}
	peers := []map[string]string{{"name": "Hosta", "url": "https://hosta.example.ts.net/"}}
	if _, err := load(map[string]any{"machine": "hostb", "peers": peers}); err == nil {
		t.Fatal("the trial accepted peers")
	}
	c, err := load(map[string]any{"profile": "production", "machine": "hostb", "peers": peers})
	if err != nil || len(c.Peers) != 1 || c.Peers[0].Name != "hosta" || c.Peers[0].URL != "https://hosta.example.ts.net" {
		t.Fatalf("%+v %v", c.Peers, err)
	}
	for _, bad := range [][]map[string]string{
		{{"name": "hostb", "url": "https://p.example.ts.net"}},
		{{"name": "hosta", "url": "https://a.example.ts.net"}, {"name": "hosta", "url": "https://b.example.ts.net"}},
		{{"name": "bad name", "url": "https://a.example.ts.net"}},
		{{"name": "hosta", "url": "http://hosta.example.ts.net"}},
	} {
		if _, err := load(map[string]any{"profile": "production", "machine": "hostb", "peers": bad}); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}
