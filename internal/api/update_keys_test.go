package api_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

func pubKey(t *testing.T) (string, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(pub)
	return base64.StdEncoding.EncodeToString(pub), hex.EncodeToString(h[:])[:12]
}

// The owner's re-pin: owner only, refused when invalid before anyone is asked, asked even with
// the gate off, bound to the exact set, and saved only once confirmed.
func TestUpdateKeysRepin(t *testing.T) {
	d, owner, agent, _ := configAndUpdateDaemon(t)
	p1, _ := pubKey(t)
	p2, f2 := pubKey(t)
	d.SetUpdateSettings(config.UpdateConfig{FleetKeys: []config.FleetKey{{ID: "k1", Type: "ed25519", Key: p1}}, KeysSerial: 3})

	for _, m := range []string{"GET", "PUT"} {
		if s, _ := call(t, agent, m, "/api/update/keys", map[string]any{}, nil); s != http.StatusForbidden {
			t.Fatalf("agent %s: %d", m, s)
		}
	}
	var got update.PinnedKeys
	if s, _ := call(t, owner, "GET", "/api/update/keys", nil, &got); s != 0 || len(got.FleetKeys) != 1 || got.FleetKeys[0].Key != p1 || got.KeysSerial != 3 {
		t.Fatalf("get: %d %+v", s, got)
	}

	k := func(id, key string) map[string]string {
		return map[string]string{"id": id, "type": "ed25519", "key": key}
	}
	five := []any{}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		p, _ := pubKey(t)
		five = append(five, k(id, p))
	}
	for name, bad := range map[string]any{
		"empty":        map[string]any{"fleet_keys": []any{}},
		"bad key":      map[string]any{"fleet_keys": []any{k("k2", "nope")}},
		"five keys":    map[string]any{"fleet_keys": five},
		"require type": map[string]any{"fleet_keys": []any{k("k2", p2)}, "require": []string{"webauthn"}},
	} {
		if s, _ := call(t, owner, "PUT", "/api/update/keys", bad, nil); s != http.StatusBadRequest {
			t.Errorf("%s: %d", name, s)
		}
	}
	if n := len(d.GateAuth.Prompts()); n != 0 {
		t.Fatalf("an invalid set asked the owner (%d prompts)", n)
	}

	body := map[string]any{"fleet_keys": []any{k("k2", p2)}}
	s, conf := call(t, owner, "PUT", "/api/update/keys", body, nil) // the gate is off: it asks anyway
	if s != http.StatusPreconditionRequired || !strings.Contains(conf.Reason, "pin k2 "+f2) || !strings.Contains(conf.Reason, "drop k1 ") {
		t.Fatalf("without proof: %d %q", s, conf.Reason)
	}
	if ks := d.UpdateSettings().FleetKeys; len(ks) != 1 || ks[0].ID != "k1" {
		t.Fatalf("changed before the owner confirmed: %+v", ks)
	}
	proof := confirmed(t, owner, conf)
	other := map[string]any{"fleet_keys": []any{k("k1", p1), k("k2", p2)}}
	s, again := call(t, proof, "PUT", "/api/update/keys", other, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("a proof for one set let another through: %d", s)
	}
	confirmed(t, owner, again)
	if s, _ := call(t, proof, "PUT", "/api/update/keys", body, &got); s != 0 || len(got.FleetKeys) != 1 || got.FleetKeys[0].ID != "k2" || got.KeysSerial != 3 {
		t.Fatalf("with proof: %d %+v", s, got)
	}
	if ks := d.UpdateSettings().FleetKeys; len(ks) != 1 || ks[0].Key != p2 {
		t.Fatalf("not saved: %+v", ks)
	}
}

// v1 refuses turning automatic installs on, and an install of a tag that is not a fleet release
// tag, both before anyone is asked. A valid tag asks with the tag in the reason, then is checked
// (here: a dev build, updates off = 409).
func TestUpdateAutoOnAndInstallTag(t *testing.T) {
	d, owner, _, _ := configAndUpdateDaemon(t)
	if s, _ := call(t, owner, "PUT", "/api/update", map[string]bool{"auto": true}, nil); s != http.StatusBadRequest {
		t.Fatalf("auto on: %d", s)
	}
	for _, tag := range []string{"v1.0.0", "fleet-v1.0.0-2-gabcdef0", "latest"} {
		if s, _ := call(t, owner, "POST", "/api/update/install", map[string]any{"tag": tag}, nil); s != http.StatusBadRequest {
			t.Errorf("tag %q: %d", tag, s)
		}
	}
	if n := len(d.GateAuth.Prompts()); n != 0 {
		t.Fatalf("refused requests asked the owner (%d prompts)", n)
	}
	gateOn(t, owner)
	body := map[string]any{"tag": "fleet-v0.2.0", "now": true}
	s, conf := call(t, owner, "POST", "/api/update/install", body, nil)
	if s != http.StatusPreconditionRequired || !strings.Contains(conf.Reason, "install the release fleet-v0.2.0 now") {
		t.Fatalf("without proof: %d %q", s, conf.Reason)
	}
	proof := confirmed(t, owner, conf)
	if s, _ := call(t, proof, "POST", "/api/update/install", body, nil); s != http.StatusConflict {
		t.Fatalf("with proof on a dev build: %d", s)
	}
}

// The rollback drill: owner only, asked even with the gate off, armed only once confirmed, and
// shown in the update status.
func TestUpdateDrillArm(t *testing.T) {
	_, owner, agent, _ := configAndUpdateDaemon(t)
	arm := map[string]bool{"arm": true}
	if s, _ := call(t, agent, "PUT", "/api/update/drill", arm, nil); s != http.StatusForbidden {
		t.Fatalf("agent: %d", s)
	}
	s, conf := call(t, owner, "PUT", "/api/update/drill", arm, nil)
	if s != http.StatusPreconditionRequired || !strings.Contains(conf.Reason, "rollback drill") {
		t.Fatalf("without proof: %d %q", s, conf.Reason)
	}
	var st update.Status
	if call(t, owner, "GET", "/api/update", nil, &st); st.DrillArmed {
		t.Fatal("armed before the owner confirmed")
	}
	proof := confirmed(t, owner, conf)
	if s, _ := call(t, proof, "PUT", "/api/update/drill", arm, &st); s != 0 || !st.DrillArmed {
		t.Fatalf("with proof: %d %+v", s, st)
	}
	off := map[string]bool{"arm": false}
	s, conf = call(t, owner, "PUT", "/api/update/drill", off, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("disarm without proof: %d", s)
	}
	proof = confirmed(t, owner, conf)
	var after update.Status // a fresh one: drill_armed is left out when false
	if s, _ := call(t, proof, "PUT", "/api/update/drill", off, &after); s != 0 || after.DrillArmed {
		t.Fatalf("disarm: %d %+v", s, after)
	}
}
