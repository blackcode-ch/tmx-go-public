package update

import (
	"fmt"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// FleetKeyID is the id of the first fleet key (and of the legacy update.fleet_public_key). Fleet
// private key material is provisioned separately; this repository deliberately ships no trusted key.
const FleetKeyID = config.LegacyFleetKeyID

// FleetSignatureContext separates fleet signatures from signatures of raw sums.
// The purpose, repository, and tag each precede SHA256SUMS as an LF-terminated line.
const FleetSignatureContext = "tmx-fleet-release-v1"

// FleetExtraSignatureContext isolates on-demand extras from daemon releases.
const FleetExtraSignatureContext = "tmx-fleet-extra-v1"

// FleetWebAuthnContext and FleetExtraWebAuthnContext start the preimage of a webauthn signature's
// challenge (webauthnChallenge), for releases and extras.
const (
	FleetWebAuthnContext      = "tmx-fleet-release-v2"
	FleetExtraWebAuthnContext = "tmx-fleet-extra-v2"
)

// trustedKeys reads the pinned key set from local configuration. Options.Keys (base64 Ed25519
// keys under FleetKeyID, require ["ed25519"]) is an explicit dependency injection seam for
// tests, never an upstream fallback.
func (u *Updater) trustedKeys() (keySet, error) {
	if u.o.Keys != nil {
		var ks []config.FleetKey
		for _, k := range u.o.Keys {
			ks = append(ks, config.FleetKey{ID: FleetKeyID, Type: config.KeyEd25519, Key: k})
		}
		return newKeySet(ks, []string{config.KeyEd25519})
	}
	s := u.settings()
	keys, require, err := s.PinnedKeys()
	if err != nil {
		return keySet{}, fmt.Errorf("update keys invalid: %w", err) // fail closed: every release is refused
	}
	ks, err := newKeySet(keys, require)
	if err != nil {
		return keySet{}, fmt.Errorf("update keys invalid: %w", err)
	}
	return ks, nil
}
