package config

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestTrialProfileDefaults(t *testing.T) {
	var c Config
	c.fillDefaults()
	b, _ := json.Marshal(c)
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	if fields["profile"] != "trial" {
		t.Errorf("profile = %v", fields["profile"])
	}
	if c.P2P.On() || c.P2P.DHTOn() || c.P2P.MDNSOn() || c.P2P.NATPortMapOn() || c.WhatsApp.On() || c.Companion.On() || *c.Update.Resolved().Auto {
		t.Error("external services enabled by default")
	}
	if c.Port != 8797 || c.URL() != "http://127.0.0.1:8797" {
		t.Error("API is not isolated")
	}
}

func TestTrialDisablesLegacyExternalServices(t *testing.T) {
	var c Config
	err := json.Unmarshal([]byte(`{"p2p":{"enabled":true,"dht":true,"mdns":true,"nat_port_map":true},"whatsapp":{"enabled":true},"companion":{"enabled":true},"update":{"auto":true}}`), &c)
	if err != nil {
		t.Fatal(err)
	}
	c.fillDefaults()
	if c.P2P.On() || c.P2P.DHTOn() || c.P2P.MDNSOn() || c.P2P.NATPortMapOn() || c.WhatsApp.On() || c.Companion.On() || *c.Update.Resolved().Auto {
		t.Fatal("legacy config escaped trial isolation")
	}
}

func TestTrialHomeIsolation(t *testing.T) {
	t.Setenv(EnvHome, "")
	h, err := defaultHome()
	if err != nil || filepath.Base(h) != "tmxg-trial" {
		t.Fatalf("home = %q, %v", h, err)
	}
}

func TestDefaultConfigPrivate(t *testing.T) {
	p := &P2PConfig{}
	if p.On() || p.DHTOn() || p.MDNSOn() || p.NATPortMapOn() {
		t.Fatal("empty P2P config is public")
	}
}
