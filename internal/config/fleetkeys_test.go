package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

func testCOSE(t *testing.T, alg webauthncose.COSEAlgorithmIdentifier) string {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: int64(webauthncose.EllipticKey), Algorithm: int64(alg)},
		Curve:         int64(webauthncose.P256), XCoord: k.X.FillBytes(make([]byte, 32)), YCoord: k.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestPinnedKeys(t *testing.T) {
	ed := base64.StdEncoding.EncodeToString(make([]byte, 32))
	ed2 := base64.StdEncoding.EncodeToString(append(make([]byte, 31), 1))
	wa := testCOSE(t, webauthncose.AlgES256)
	edKey := func(id string) FleetKey { return FleetKey{ID: id, Type: KeyEd25519, Key: ed} }
	waKey := FleetKey{ID: "passkey", Type: KeyWebAuthn, Key: wa, RPID: "tmx.example", Origin: "https://tmx.example"}
	withWA := func(f func(*FleetKey)) []FleetKey { k := waKey; f(&k); return []FleetKey{k} }
	for _, tc := range []struct {
		name    string
		cfg     UpdateConfig
		want    string // "" = valid; else part of the error
		nKeys   int
		require string
	}{
		{"nothing pinned", UpdateConfig{}, "", 0, "ed25519"},
		{"legacy key", UpdateConfig{FleetPublicKey: ed}, "", 1, "ed25519"},
		{"legacy duplicate of an entry", UpdateConfig{FleetPublicKey: ed, FleetKeys: []FleetKey{edKey("k1")}}, "", 1, "ed25519"},
		{"legacy id clash", UpdateConfig{FleetPublicKey: ed2, FleetKeys: []FleetKey{edKey(LegacyFleetKeyID)}}, "pinned twice", 0, ""},
		{"legacy invalid", UpdateConfig{FleetPublicKey: "PLACEHOLDER"}, "base64", 0, ""},
		{"webauthn + ed25519, 2 of 2", UpdateConfig{FleetKeys: []FleetKey{edKey("k1"), waKey}, Require: []string{"ed25519", "webauthn"}}, "", 2, "ed25519,webauthn"},
		{"webauthn only", UpdateConfig{FleetKeys: []FleetKey{waKey}, Require: []string{"webauthn"}}, "", 1, "webauthn"},
		{"webauthn only, default require", UpdateConfig{FleetKeys: []FleetKey{waKey}}, "needs at least one pinned ed25519", 0, ""},
		{"require without its key", UpdateConfig{FleetKeys: []FleetKey{edKey("k1")}, Require: []string{"webauthn"}}, "needs at least one pinned webauthn", 0, ""},
		{"explicit require, nothing pinned", UpdateConfig{Require: []string{"ed25519"}}, "needs at least one", 0, ""},
		{"unknown require", UpdateConfig{FleetKeys: []FleetKey{edKey("k1")}, Require: []string{"rsa"}}, "unknown signature type", 0, ""},
		{"require twice", UpdateConfig{FleetKeys: []FleetKey{edKey("k1")}, Require: []string{"ed25519", "ed25519"}}, "twice", 0, ""},
		{"too many", UpdateConfig{FleetKeys: []FleetKey{edKey("a"), edKey("b"), edKey("c"), edKey("d"), edKey("e")}}, "at most 4", 0, ""},
		{"duplicate id", UpdateConfig{FleetKeys: []FleetKey{edKey("a"), edKey("a")}}, "pinned twice", 0, ""},
		{"unsafe id", UpdateConfig{FleetKeys: []FleetKey{edKey("a:b")}}, "id", 0, ""},
		{"empty id", UpdateConfig{FleetKeys: []FleetKey{edKey("")}}, "id", 0, ""},
		{"upper-case id", UpdateConfig{FleetKeys: []FleetKey{edKey("Key")}}, "id", 0, ""},
		{"unknown type", UpdateConfig{FleetKeys: []FleetKey{{ID: "a", Type: "rsa", Key: ed}}}, "unknown key type", 0, ""},
		{"ed25519 short", UpdateConfig{FleetKeys: []FleetKey{{ID: "a", Type: KeyEd25519, Key: base64.StdEncoding.EncodeToString(make([]byte, 31))}}}, "32 bytes", 0, ""},
		{"ed25519 with origin", UpdateConfig{FleetKeys: []FleetKey{{ID: "a", Type: KeyEd25519, Key: ed, Origin: "https://x"}}}, "webauthn keys", 0, ""},
		{"webauthn not COSE", UpdateConfig{FleetKeys: withWA(func(k *FleetKey) { k.Key = ed }), Require: []string{"webauthn"}}, "COSE", 0, ""},
		{"webauthn not ES256", UpdateConfig{FleetKeys: withWA(func(k *FleetKey) { k.Key = testCOSE(t, webauthncose.AlgES384) }), Require: []string{"webauthn"}}, "", 0, ""},
		{"webauthn no rp_id", UpdateConfig{FleetKeys: withWA(func(k *FleetKey) { k.RPID = "" }), Require: []string{"webauthn"}}, "rp_id", 0, ""},
		{"webauthn no origin", UpdateConfig{FleetKeys: withWA(func(k *FleetKey) { k.Origin = "" }), Require: []string{"webauthn"}}, "origin", 0, ""},
		{"webauthn http origin", UpdateConfig{FleetKeys: withWA(func(k *FleetKey) { k.Origin = "http://tmx.example" }), Require: []string{"webauthn"}}, "origin", 0, ""},
		{"webauthn origin with path", UpdateConfig{FleetKeys: withWA(func(k *FleetKey) { k.Origin = "https://tmx.example/" }), Require: []string{"webauthn"}}, "origin", 0, ""},
		{"webauthn origin outside rp_id", UpdateConfig{FleetKeys: withWA(func(k *FleetKey) { k.Origin = "https://eviltmx.example" }), Require: []string{"webauthn"}}, "not within", 0, ""},
		{"webauthn origin below rp_id", UpdateConfig{FleetKeys: withWA(func(k *FleetKey) { k.Origin = "https://sign.tmx.example:8443" }), Require: []string{"webauthn"}}, "", 1, "webauthn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys, req, err := tc.cfg.PinnedKeys()
			if tc.name == "webauthn not ES256" { // any error will do (the library or our check)
				if err == nil {
					t.Fatal("an ES384 key accepted")
				}
				return
			}
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %v, want %q", err, tc.want)
				}
				return
			}
			if err != nil || len(keys) != tc.nKeys || strings.Join(req, ",") != tc.require {
				t.Fatalf("keys=%d require=%v err=%v", len(keys), req, err)
			}
		})
	}
}

// An invalid pinned set must never stop Load (and so the daemon): updates fail closed instead.
func TestLoadToleratesInvalidFleetKeys(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	ed := base64.StdEncoding.EncodeToString(make([]byte, 32))
	for _, body := range []string{
		`{"update":{"fleet_keys":[{"id":"a","type":"ed25519","key":"AAAA"}]}}`,
		`{"update":{"fleet_public_key":"PLACEHOLDER"}}`,
		`{"update":{"fleet_public_key":"` + ed + `","require":["webauthn"]}}`,
	} {
		if err := os.WriteFile(p.ConfigFile(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := Load(p)
		if err != nil {
			t.Fatalf("%s: Load failed: %v", body, err)
		}
		if _, _, err := c.Update.PinnedKeys(); err == nil {
			t.Fatalf("%s: invalid set accepted", body)
		}
	}
	if err := os.WriteFile(p.ConfigFile(), []byte(`{"update":{"fleet_public_key":"`+ed+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if keys, _, err := c.Update.PinnedKeys(); err != nil || len(keys) != 1 || keys[0].ID != LegacyFleetKeyID {
		t.Fatalf("legacy key not read: %+v %v", keys, err)
	}
}

// A wrong JSON SHAPE for fleet_keys or require (not just a wrong value) must not stop the daemon
// either: Load succeeds, updates turn off with a reason, and a save writes the value back as it was
// (security-review #111, the lockout rule).
func TestFleetKeysWrongShapeNeverStopsLoad(t *testing.T) {
	for name, update := range map[string]string{
		"require is a string":   `{"require":"webauthn"}`,
		"fleet_keys is object":  `{"fleet_keys":{}}`,
		"fleet_keys is number":  `{"fleet_keys":7,"require":["ed25519"]}`,
		"both wrong":            `{"fleet_keys":"x","require":{"a":1}}`,
		"key entry wrong shape": `{"fleet_keys":[{"id":5}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := testPaths(t)
			t.Setenv(EnvPort, "")
			if err := os.WriteFile(p.ConfigFile(), []byte(`{"update":`+update+`}`), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(p)
			if err != nil {
				t.Fatalf("Load stopped on a bad update shape: %v", err)
			}
			if _, _, err := c.Update.PinnedKeys(); err == nil {
				t.Fatal("a wrong shape left updates on")
			}
			if err := Save(p, c); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(p.ConfigFile())
			var back struct{ Update map[string]json.RawMessage }
			var orig map[string]json.RawMessage
			_ = json.Unmarshal(b, &back)
			_ = json.Unmarshal([]byte(update), &orig)
			for k, v := range orig {
				if !jsonEqual(back.Update[k], v) {
					t.Fatalf("%s was not written back as it was: %s, want %s", k, back.Update[k], v)
				}
			}
		})
	}
}

func TestUpdateConfigRoundTripUnchanged(t *testing.T) {
	in := `{"channel":"stable","fleet_public_key":"","fleet_keys":[{"id":"k1","type":"ed25519","key":"` +
		base64.StdEncoding.EncodeToString(make([]byte, 32)) + `"}],"require":["ed25519"]}`
	var u UpdateConfig
	if err := json.Unmarshal([]byte(in), &u); err != nil {
		t.Fatal(err)
	}
	if len(u.FleetKeys) != 1 || u.FleetKeys[0].ID != "k1" || len(u.Require) != 1 {
		t.Fatalf("%+v", u)
	}
	if _, _, err := u.PinnedKeys(); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	_ = json.Unmarshal([]byte(in), &a)
	_ = json.Unmarshal(out, &b)
	delete(a, "fleet_public_key") // omitempty
	if !jsonEqual(mustJSON(a), mustJSON(b)) {
		t.Fatalf("round trip changed it:\n%s\n%s", in, out)
	}
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return string(mustJSON(x)) == string(mustJSON(y))
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
