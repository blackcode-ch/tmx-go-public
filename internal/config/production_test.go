package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Peer-to-peer answers the owner's own config (3 Nov: the pair-token fleet transport doesn't cover
// person-to-person contacts/chats, which is what this is for); automatic updates stay off
// regardless (no signed release path yet - a separate concern).
func TestProductionProfileHonoursP2PKeepsUpdatesOff(t *testing.T) {
	var c Config
	err := json.Unmarshal([]byte(`{"profile":"production","p2p":{"enabled":true,"dht":true,"mdns":true,"nat_port_map":true},"whatsapp":{"enabled":true},"companion":{"enabled":true},"update":{"auto":true}}`), &c)
	if err != nil {
		t.Fatal(err)
	}
	c.fillDefaults()
	if !c.Production() {
		t.Fatal("profile lost")
	}
	if !c.P2P.On() || !c.P2P.DHTOn() || !c.P2P.MDNSOn() || !c.P2P.NATPortMapOn() {
		t.Fatal("production ignored the owner's peer-to-peer config")
	}
	if *c.Update.Resolved().Auto {
		t.Fatal("production enabled automatic updates")
	}
	if !c.WhatsApp.On() || !c.Companion.On() {
		t.Fatal("production ignored the owner's WhatsApp/Companion switches")
	}
}

func TestProductionProfileDefaultsOff(t *testing.T) {
	c := Config{Profile: ProfileProduction}
	c.fillDefaults()
	if c.WhatsApp.On() || c.Companion.On() || c.P2P.On() || c.Port != DefaultPort {
		t.Fatalf("production defaults: %+v", c)
	}
}

// Hostb's exact config, 3 Nov: production, p2p.enabled true, an explicit on-disk "dht":false
// left by older fillDefaults code (cfc7be0/PR #176, before production honoured the field at all)
// - indistinguishable from a genuine owner choice by its bare presence, which is why
// EffectiveDHT defaults it on anyway unless DHTExplicit says the owner really chose it.
func TestEffectiveDHTOverridesPioneersStaleFalse(t *testing.T) {
	off := false
	c := Config{Profile: ProfileProduction, P2P: &P2PConfig{Enabled: boolPtr(true), DHT: &off, MDNS: &off, NATPortMap: &off}}
	if c.EffectiveDHT() != true {
		t.Fatal("EffectiveDHT did not override the stale on-disk dht:false")
	}
}

func TestEffectiveDHTRespectsExplicitChoice(t *testing.T) {
	off := false
	c := Config{Profile: ProfileProduction, P2P: &P2PConfig{Enabled: boolPtr(true), DHT: &off, DHTExplicit: true}}
	if c.EffectiveDHT() != false {
		t.Fatal("EffectiveDHT overrode a genuine owner choice")
	}
}

func TestEffectiveDHTStaysOffOutsideProduction(t *testing.T) {
	c := Config{Profile: ProfileTrial, P2P: &P2PConfig{Enabled: boolPtr(true)}}
	c.fillDefaults()
	if c.EffectiveDHT() != false {
		t.Fatal("EffectiveDHT defaulted DHT on in trial")
	}
}

func TestEffectiveDHTStaysOffWhenP2PDisabled(t *testing.T) {
	c := Config{Profile: ProfileProduction, P2P: &P2PConfig{}}
	if c.EffectiveDHT() != false {
		t.Fatal("EffectiveDHT defaulted DHT on while p2p itself is off")
	}
}

func boolPtr(b bool) *bool { return &b }

func TestLoadProfiles(t *testing.T) {
	for _, tc := range []struct {
		profile string
		ok      bool
	}{{"", true}, {"trial", true}, {"production", true}, {"Production", false}, {"prod", false}, {"dev", false}} {
		t.Run(tc.profile, func(t *testing.T) {
			p := testPaths(t)
			t.Setenv(EnvPort, "")
			b, _ := json.Marshal(map[string]any{"profile": tc.profile})
			if err := os.WriteFile(p.ConfigFile(), b, 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(p)
			if tc.ok != (err == nil) {
				t.Fatalf("profile %q: err=%v", tc.profile, err)
			}
			if err != nil && !strings.Contains(err.Error(), "production") {
				t.Fatalf("error does not name the valid profiles: %v", err)
			}
			if tc.ok && c.Profile != map[string]string{"": "trial"}[tc.profile]+tc.profile {
				t.Fatalf("profile = %q", c.Profile)
			}
		})
	}
}

// The port rule is the same in every profile: 8777 (Python tmx) and 8787 (tmx-go v0.1) are
// never listened on; 8787 may still be a Host, 8777 never.
func TestPortRuleExplicit(t *testing.T) {
	for _, profile := range []string{ProfileTrial, ProfileProduction} {
		for _, port := range []int{PortPythonV2, PortLegacyHTTP, 43911, 4001} {
			p := testPaths(t)
			t.Setenv(EnvPort, "")
			b, _ := json.Marshal(map[string]any{"profile": profile, "port": port})
			if err := os.WriteFile(p.ConfigFile(), b, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err == nil {
				t.Errorf("%s accepted listen port %d", profile, port)
			}
		}
	}
	if ValidListenPort(PortPythonV2) || ValidListenPort(PortLegacyHTTP) || !ValidListenPort(DefaultPort) {
		t.Fatal("ValidListenPort")
	}
	if ValidHostPort(PortPythonV2) || !ValidHostPort(PortLegacyHTTP) {
		t.Fatal("ValidHostPort")
	}
}
