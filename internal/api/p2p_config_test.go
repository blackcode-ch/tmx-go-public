package api_test

import (
	"net/http"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/config"
)

func savedP2PEnabled(t *testing.T, d *apitest.Daemon) bool {
	t.Helper()
	c, err := config.Load(config.Paths{Home: d.Home})
	if err != nil {
		t.Fatal(err)
	}
	return c.P2P.On()
}

// setProduction writes "profile": "production" to config.json before the daemon's own first
// load (apitest.New never calls config.Load/Save itself, so the file does not exist yet).
func setProduction(t *testing.T, d *apitest.Daemon) {
	t.Helper()
	p := config.Paths{Home: d.Home}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c.Profile = config.ProfileProduction
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
}

// effectiveDHT reflects what the daemon would actually start with (Config.EffectiveDHT), not
// just the bare on-disk "dht" value - the defaulting now happens at daemon-start time so an
// already-written config.json (like Hostb's) self-corrects with no new write.
func effectiveDHT(t *testing.T, d *apitest.Daemon) bool {
	t.Helper()
	c, err := config.Load(config.Paths{Home: d.Home})
	if err != nil {
		t.Fatal(err)
	}
	return c.EffectiveDHT()
}

// setProductionWithStaleDHTFalse matches Hostb's exact config, 3 Nov (tmx-lead): an older
// fillDefaults wrote an explicit "dht":false to disk before production honoured the field at
// all (cfc7be0/PR #176) - a value indistinguishable from a genuine owner choice by its bare
// presence alone, which is why DHTExplicit exists.
func setProductionWithStaleDHTFalse(t *testing.T, d *apitest.Daemon) {
	t.Helper()
	p := config.Paths{Home: d.Home}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c.Profile = config.ProfileProduction
	off := false
	c.P2P = &config.P2PConfig{DHT: &off} // explicit false, DHTExplicit left at its zero value (false)
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
}

// Peer-to-peer always stays off in the trial profile (fillDefaults), whatever "enabled" says -
// PUT /api/p2p/config refuses to turn it on there instead of saving a setting that can never
// take effect, and GET /api/p2p's Production field lets the UI explain why in advance.
func TestP2PConfigRefusedOutsideProduction(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)

	var st api.P2PStatus
	if s, _ := call(t, owner, "GET", "/api/p2p", nil, &st); s != 0 || st.Production {
		t.Fatalf("trial profile: %d %+v", s, st)
	}
	if s, _ := call(t, owner, "PUT", "/api/p2p/config", map[string]bool{"enabled": true}, nil); s != http.StatusConflict {
		t.Fatalf("enabling in trial: %d, want 409", s)
	}
	if savedP2PEnabled(t, d) {
		t.Fatal("config.json was written despite the refusal")
	}
	// Disabling (already the trial default) is never refused - it changes nothing either way.
	if s, _ := call(t, owner, "PUT", "/api/p2p/config", map[string]bool{"enabled": false}, nil); s != 0 {
		t.Fatalf("disabling in trial: %d", s)
	}
}

// Turning peer-to-peer on or off writes config.json only - the running node (here, none: this
// daemon started with no P2P.Options) never changes, since *p2p.Host has no live start/stop of
// its own (cli/daemon.go's startP2P builds it once, before the API even exists).
func TestP2PConfigGateOffSavesAtOnce(t *testing.T) {
	d := apitest.New(t)
	setProduction(t, d)
	owner := tc.New(d.URL, d.OwnerToken)

	var st api.P2PStatus
	if s, _ := call(t, owner, "PUT", "/api/p2p/config", map[string]bool{"enabled": true}, &st); s != 0 {
		t.Fatalf("enable: %d", s)
	}
	if !savedP2PEnabled(t, d) {
		t.Fatal("config.json was not updated")
	}
	if st.Enabled {
		t.Fatal("the running state changed without a restart")
	}
	if !st.SavedEnabled || !st.RestartToApply {
		t.Fatalf("status does not show the pending restart: %+v", st)
	}

	// GET /api/p2p independently agrees (not just the PUT's own response).
	var got api.P2PStatus
	if s, _ := call(t, owner, "GET", "/api/p2p", nil, &got); s != 0 || got.Enabled || !got.SavedEnabled || !got.RestartToApply {
		t.Fatalf("GET after enabling: %d %+v", s, got)
	}

	// Turning it off again goes back to agreeing (no restart pending).
	if s, _ := call(t, owner, "PUT", "/api/p2p/config", map[string]bool{"enabled": false}, &st); s != 0 {
		t.Fatalf("disable: %d", s)
	}
	if savedP2PEnabled(t, d) {
		t.Fatal("config.json still enabled")
	}
	if st.SavedEnabled || st.RestartToApply {
		t.Fatalf("status still shows a pending restart: %+v", st)
	}
}

// Enabling peer-to-peer defaults DHT on (Config.EffectiveDHT, checked at daemon start) -
// without it, host.go never engages AutoRelay/AutoNATv2 (gated on DHT || static relays), so a
// NATed machine gets a peer ID nobody can dial.
func TestP2PConfigEnablingDefaultsDHTOn(t *testing.T) {
	d := apitest.New(t)
	setProduction(t, d)
	owner := tc.New(d.URL, d.OwnerToken)

	if s, _ := call(t, owner, "PUT", "/api/p2p/config", map[string]bool{"enabled": true}, nil); s != 0 {
		t.Fatalf("enable: %d", s)
	}
	if !effectiveDHT(t, d) {
		t.Fatal("DHT was not defaulted on")
	}
}

// Hostb's exact config, 3 Nov: an explicit "dht":false already on disk, written by older
// fillDefaults code before production honoured the field at all (cfc7be0/PR #176) - bare
// presence of "dht":false cannot by itself mean a genuine owner choice, so enabling still
// defaults DHT on (Config.EffectiveDHT, via DHTExplicit rather than a nil check). The on-disk
// value itself is left untouched; the default takes effect at daemon-start time, not by PUT
// rewriting it, since Hostb's guard refuses config.json writes from an agent.
func TestP2PConfigEnablingOverridesAStaleDHTFalse(t *testing.T) {
	d := apitest.New(t)
	setProductionWithStaleDHTFalse(t, d)
	owner := tc.New(d.URL, d.OwnerToken)

	if effectiveDHT(t, d) {
		t.Fatal("test setup: DHT should start false")
	}
	if s, _ := call(t, owner, "PUT", "/api/p2p/config", map[string]bool{"enabled": true}, nil); s != 0 {
		t.Fatalf("enable: %d", s)
	}
	if !effectiveDHT(t, d) {
		t.Fatal("a stale on-disk dht:false blocked the default - Hostb's exact bug")
	}
}

// A genuine owner choice (DHTExplicit true) is never overridden, even to false.
func TestP2PConfigRespectsAnExplicitDHTChoice(t *testing.T) {
	d := apitest.New(t)
	setProduction(t, d)
	p := config.Paths{Home: d.Home}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	off := false
	c.P2P.DHT, c.P2P.DHTExplicit = &off, true
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	owner := tc.New(d.URL, d.OwnerToken)

	if s, _ := call(t, owner, "PUT", "/api/p2p/config", map[string]bool{"enabled": true}, nil); s != 0 {
		t.Fatalf("enable: %d", s)
	}
	if effectiveDHT(t, d) {
		t.Fatal("an explicit owner choice to keep DHT off was overridden")
	}
}

// PUT /api/p2p/config also accepts an explicit dht field (owner + PIN), for the owner to turn
// DHT off (or back on) themselves - that choice is marked DHTExplicit and always respected.
func TestP2PConfigAcceptsExplicitDHT(t *testing.T) {
	d := apitest.New(t)
	setProduction(t, d)
	owner := tc.New(d.URL, d.OwnerToken)

	if s, _ := call(t, owner, "PUT", "/api/p2p/config", map[string]any{"enabled": true, "dht": false}, nil); s != 0 {
		t.Fatalf("enable with dht:false: %d", s)
	}
	if effectiveDHT(t, d) {
		t.Fatal("an explicit dht:false in the request was not honoured")
	}
	p := config.Paths{Home: d.Home}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.P2P.DHTExplicit {
		t.Fatal("the explicit choice was not recorded as DHTExplicit")
	}

	if s, _ := call(t, owner, "PUT", "/api/p2p/config", map[string]any{"enabled": true, "dht": true}, nil); s != 0 {
		t.Fatalf("flip to dht:true: %d", s)
	}
	if !effectiveDHT(t, d) {
		t.Fatal("an explicit dht:true in the request was not honoured")
	}
}

// Enabling when it is already enabled, or disabling when already off, changes nothing and
// never asks (the owner gate is on in this one, to prove a same-value PUT still skips the ask).
func TestP2PConfigNoPromptForNoChange(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	gateOn(t, owner)

	n := len(d.GateAuth.Prompts())
	if s, _ := call(t, owner, "PUT", "/api/p2p/config", map[string]bool{"enabled": false}, nil); s != 0 {
		t.Fatalf("already off: %d", s)
	}
	if len(d.GateAuth.Prompts()) != n {
		t.Fatal("a same-value PUT asked the owner")
	}
}

// With the gate on, turning peer-to-peer on or off asks for a fresh proof like any other
// config.json change; the confirmed proof is bound to this exact body.
func TestP2PConfigGateOn(t *testing.T) {
	d := apitest.New(t)
	setProduction(t, d)
	owner := tc.New(d.URL, d.OwnerToken)
	gateOn(t, owner)

	s, conf := call(t, owner, "PUT", "/api/p2p/config", map[string]bool{"enabled": true}, nil)
	if s != http.StatusPreconditionRequired || conf.Reason != "turn on peer-to-peer" {
		t.Fatalf("without proof: %d %q", s, conf.Reason)
	}
	if savedP2PEnabled(t, d) {
		t.Fatal("the change happened before the owner confirmed")
	}
	proof := confirmed(t, owner, conf)
	var st api.P2PStatus
	if s, _ := call(t, proof, "PUT", "/api/p2p/config", map[string]bool{"enabled": true}, &st); s != 0 {
		t.Fatalf("with proof: %d", s)
	}
	if !savedP2PEnabled(t, d) {
		t.Fatal("confirmed, but config.json was not written")
	}
}

// Only the owner may change the setting: an agent, a Companion link, and a peer machine (its
// pair token, never the owner's bearer token) are all refused, and none of them can reach the
// restart route either.
func TestP2PConfigAndRestartOwnerOnly(t *testing.T) {
	d := apitest.New(t)
	agent := tc.New(d.URL, d.AgentToken(t, "head"))
	link := linkClient(t, d)
	ps := newPeerSetup(t)

	for _, c := range []struct {
		name string
		cl   *tc.Client
	}{{"agent", agent}, {"Companion", link}} {
		if s, _ := call(t, c.cl, "PUT", "/api/p2p/config", map[string]bool{"enabled": true}, nil); s != http.StatusForbidden {
			t.Errorf("%s PUT config: %d, want 403", c.name, s)
		}
		if s, _ := call(t, c.cl, "POST", "/api/p2p/restart", nil, nil); s != http.StatusForbidden {
			t.Errorf("%s POST restart: %d, want 403", c.name, s)
		}
	}
	if code := peerDo(t, ps.url, "PUT", "/api/p2p/config", "hosta", ps.pair, nil, map[string]bool{"enabled": true}, nil); code != http.StatusForbidden {
		t.Errorf("peer PUT config: %d, want 403", code)
	}
	if code := peerDo(t, ps.url, "POST", "/api/p2p/restart", "hosta", ps.pair, nil, nil, nil); code != http.StatusForbidden {
		t.Errorf("peer POST restart: %d, want 403", code)
	}

	// The dht field is no different: still owner-only, not a second way in for a peer or agent.
	if s, _ := call(t, agent, "PUT", "/api/p2p/config", map[string]any{"enabled": true, "dht": false}, nil); s != http.StatusForbidden {
		t.Errorf("agent PUT config with dht: %d, want 403", s)
	}
	c, err := config.Load(config.Paths{Home: d.Home})
	if err != nil {
		t.Fatal(err)
	}
	if c.P2P.DHTExplicit {
		t.Fatal("a refused agent PUT still changed dht")
	}
}

// POST /api/p2p/restart is the same graceful self-restart path POST /api/fleet-ops/restart uses;
// it answers 501 when the daemon has no restart hook (as in every other test here, which never
// wires Options.Restart), proving the route exists and is owner-gated even then.
func TestP2PRestartWithoutAHookIs501(t *testing.T) {
	d := apitest.New(t)
	owner := tc.New(d.URL, d.OwnerToken)
	if s, _ := call(t, owner, "POST", "/api/p2p/restart", nil, nil); s != http.StatusNotImplemented {
		t.Fatalf("no restart hook: %d, want 501", s)
	}
}
