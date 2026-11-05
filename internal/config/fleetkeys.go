package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

// The pinned release key set (DESIGN.md "Updates"): update.fleet_keys plus update.require.

// Fleet key types.
const (
	KeyEd25519  = "ed25519"
	KeyWebAuthn = "webauthn"
)

// LegacyFleetKeyID is the id the old update.fleet_public_key is pinned under.
const LegacyFleetKeyID = "fleet-release-v1"

// MaxFleetKeys bounds the pinned set (a rotation keeps at most the old and the new keys around).
const MaxFleetKeys = 4

// FleetKey is one pinned release key.
type FleetKey struct {
	// ID names the key in SHA256SUMS.sig ("<id>:<type>:<payload>").
	ID string `json:"id"`
	// Type is "ed25519" or "webauthn".
	Type string `json:"type"`
	// Key, standard base64: the raw 32-byte Ed25519 public key, or the credential's COSE_Key
	// (ES256 / P-256 only) for webauthn.
	Key string `json:"key"`
	// RPID and Origin (webauthn only) are what the assertion must be bound to.
	RPID   string `json:"rp_id,omitempty"`
	Origin string `json:"origin,omitempty"`
}

var (
	fleetKeyIDRx = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	rpIDRx       = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
)

// PinnedKeys returns the effective pinned keys (fleet_keys, or the legacy fleet_public_key as one
// ed25519 entry) and the require policy (default ["ed25519"]), validated. No keys is not an error:
// updates then refuse every release. Load does not call it: an invalid set must never stop the
// daemon, it only turns updates off (fail closed for updates only). u may be nil.
func (u *UpdateConfig) PinnedKeys() ([]FleetKey, []string, error) {
	if u == nil {
		return nil, []string{KeyEd25519}, nil
	}
	if u.shapeErr != nil {
		return nil, nil, u.shapeErr
	}
	keys := append([]FleetKey(nil), u.FleetKeys...)
	if legacy := strings.TrimSpace(u.FleetPublicKey); legacy != "" {
		dup := false
		for _, k := range keys {
			if k.Type == KeyEd25519 && strings.TrimSpace(k.Key) == legacy {
				dup = true
			}
		}
		if !dup {
			keys = append(keys, FleetKey{ID: LegacyFleetKeyID, Type: KeyEd25519, Key: legacy})
		}
	}
	if len(keys) > MaxFleetKeys {
		return nil, nil, fmt.Errorf("at most %d fleet keys may be pinned", MaxFleetKeys)
	}
	have := map[string]bool{}
	ids := map[string]bool{}
	for i, k := range keys {
		if err := k.Validate(); err != nil {
			return nil, nil, fmt.Errorf("fleet key %d: %w", i+1, err)
		}
		if ids[k.ID] {
			return nil, nil, fmt.Errorf("fleet key id %q is pinned twice", k.ID)
		}
		ids[k.ID], have[k.Type] = true, true
	}
	require := u.Require
	explicit := len(require) > 0
	if !explicit {
		require = []string{KeyEd25519}
	}
	seen := map[string]bool{}
	for _, t := range require {
		if t != KeyEd25519 && t != KeyWebAuthn {
			return nil, nil, fmt.Errorf("require: unknown signature type %q", t)
		}
		if seen[t] {
			return nil, nil, fmt.Errorf("require: %q listed twice", t)
		}
		seen[t] = true
		// With nothing pinned updates are simply off; an explicit policy still needs its keys.
		if !have[t] && (len(keys) > 0 || explicit) {
			return nil, nil, fmt.Errorf("require: %q needs at least one pinned %s key", t, t)
		}
	}
	return keys, append([]string(nil), require...), nil
}

// WithPins returns u with the pinned set replaced by keys and require (the owner's re-pin and a
// signed rotation statement): the legacy fleet_public_key is dropped (it is part of the set the
// new one replaces) and the result must be a valid, non-empty set.
func (u UpdateConfig) WithPins(keys []FleetKey, require []string) (UpdateConfig, error) {
	u.FleetKeys = append([]FleetKey(nil), keys...)
	u.Require = append([]string(nil), require...)
	u.FleetPublicKey = ""
	u.rawKeys, u.rawRequire, u.shapeErr = nil, nil, nil
	got, _, err := u.PinnedKeys()
	if err != nil {
		return UpdateConfig{}, err
	}
	if len(got) == 0 {
		return UpdateConfig{}, errors.New("at least one fleet key must stay pinned")
	}
	return u, nil
}

// Validate checks one key's id, type, encoding and (webauthn) binding.
func (k FleetKey) Validate() error {
	if !fleetKeyIDRx.MatchString(k.ID) {
		return fmt.Errorf("id %q: want 1-64 of a-z 0-9 . _ - (starting with a letter or digit)", k.ID)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(k.Key))
	if err != nil {
		return fmt.Errorf("%s: key is not base64", k.ID)
	}
	switch k.Type {
	case KeyEd25519:
		if len(raw) != ed25519.PublicKeySize {
			return fmt.Errorf("%s: an ed25519 key is %d bytes", k.ID, ed25519.PublicKeySize)
		}
		if k.RPID != "" || k.Origin != "" {
			return fmt.Errorf("%s: rp_id/origin are for webauthn keys", k.ID)
		}
		return nil
	case KeyWebAuthn:
		if _, err := ParseWebAuthnKey(raw); err != nil {
			return fmt.Errorf("%s: %w", k.ID, err)
		}
		return validateWebAuthnBinding(k.RPID, k.Origin)
	default:
		return fmt.Errorf("%s: unknown key type %q (want ed25519 or webauthn)", k.ID, k.Type)
	}
}

// ParseWebAuthnKey parses a credential's COSE_Key, accepting only ES256 on P-256.
func ParseWebAuthnKey(cose []byte) (webauthncose.EC2PublicKeyData, error) {
	pk, err := webauthncose.ParsePublicKey(cose)
	if err != nil {
		return webauthncose.EC2PublicKeyData{}, errors.New("key is not a COSE public key")
	}
	ec, ok := pk.(webauthncose.EC2PublicKeyData)
	if !ok || webauthncose.COSEAlgorithmIdentifier(ec.Algorithm) != webauthncose.AlgES256 ||
		webauthncose.COSEEllipticCurve(ec.Curve) != webauthncose.P256 {
		return webauthncose.EC2PublicKeyData{}, errors.New("a webauthn key must be ES256 on P-256")
	}
	return ec, nil
}

// validateWebAuthnBinding: rp_id is a lowercase host name, origin is exactly https://<host>[:port]
// and its host is rp_id or below it (the WebAuthn scoping rule).
func validateWebAuthnBinding(rpID, origin string) error {
	if rpID == "" || len(rpID) > 253 || !rpIDRx.MatchString(rpID) {
		return fmt.Errorf("rp_id %q: want a lowercase host name", rpID)
	}
	o, err := url.Parse(origin)
	if err != nil || o.Scheme != "https" || o.Host == "" || o.User != nil || o.Path != "" ||
		o.RawQuery != "" || o.Fragment != "" || o.Opaque != "" || origin != "https://"+o.Host {
		return fmt.Errorf("origin %q: want https://host[:port]", origin)
	}
	if h := o.Hostname(); h != rpID && !strings.HasSuffix(h, "."+rpID) {
		return fmt.Errorf("origin %q is not within rp_id %q", origin, rpID)
	}
	return nil
}

// UnmarshalJSON reads "update" like the default decoder, except that fleet_keys and require of the
// wrong JSON shape (e.g. "require": "webauthn", "fleet_keys": {}) don't fail the whole config: the
// lockout rule says a bad update key may turn updates off, never stop the daemon (security-review
// #111). The field stays empty, PinnedKeys reports the error, and MarshalJSON writes the original
// value back unchanged.
func (u *UpdateConfig) UnmarshalJSON(b []byte) error {
	type plain UpdateConfig
	aux := struct {
		*plain
		FleetKeys json.RawMessage `json:"fleet_keys,omitempty"`
		Require   json.RawMessage `json:"require,omitempty"`
	}{plain: (*plain)(u)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	u.FleetKeys, u.Require, u.rawKeys, u.rawRequire, u.shapeErr = nil, nil, nil, nil, nil
	if len(aux.FleetKeys) > 0 && string(aux.FleetKeys) != "null" {
		if err := json.Unmarshal(aux.FleetKeys, &u.FleetKeys); err != nil {
			u.FleetKeys, u.rawKeys = nil, append(json.RawMessage(nil), aux.FleetKeys...)
			u.shapeErr = errors.New(`"fleet_keys" must be a list of keys`)
		}
	}
	if len(aux.Require) > 0 && string(aux.Require) != "null" {
		if err := json.Unmarshal(aux.Require, &u.Require); err != nil {
			u.Require, u.rawRequire = nil, append(json.RawMessage(nil), aux.Require...)
			if u.shapeErr == nil {
				u.shapeErr = errors.New(`"require" must be a list of signature types, e.g. ["ed25519"]`)
			}
		}
	}
	return nil
}

// MarshalJSON writes "update" back; a fleet_keys/require of the wrong shape is written as it was read.
func (u UpdateConfig) MarshalJSON() ([]byte, error) {
	type plain UpdateConfig
	aux := struct {
		plain
		FleetKeys json.RawMessage `json:"fleet_keys,omitempty"`
		Require   json.RawMessage `json:"require,omitempty"`
	}{plain: plain(u)}
	var err error
	switch {
	case u.rawKeys != nil:
		aux.FleetKeys = u.rawKeys
	case len(u.FleetKeys) > 0:
		if aux.FleetKeys, err = json.Marshal(u.FleetKeys); err != nil {
			return nil, err
		}
	}
	switch {
	case u.rawRequire != nil:
		aux.Require = u.rawRequire
	case len(u.Require) > 0:
		if aux.Require, err = json.Marshal(u.Require); err != nil {
			return nil, err
		}
	}
	return json.Marshal(aux)
}
