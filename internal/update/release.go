package update

import (
	"crypto/ed25519"
	"encoding/base64"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// The signer's side of sign.go (`tmx release sign`, internal/release). It reuses signatureMessage
// and verifySums so the signer and every daemon can never disagree on what a signature covers.

// SignReleaseLine returns the SHA256SUMS.sig line "<id>:ed25519:<base64>" for a daemon release
// (FleetSignatureContext) of repo at tag with these SHA256SUMS bytes.
func SignReleaseLine(id string, key ed25519.PrivateKey, repo, tag string, sums []byte) string {
	sig := ed25519.Sign(key, signatureMessage(FleetSignatureContext, repo, tag, sums))
	return id + ":" + config.KeyEd25519 + ":" + base64.StdEncoding.EncodeToString(sig)
}

// VerifyRelease checks a SHA256SUMS.sig for a daemon release exactly as an install does: keys and
// require as config.UpdateConfig.PinnedKeys returns them.
func VerifyRelease(keys []config.FleetKey, require []string, repo, tag string, sums, sig []byte) error {
	ks, err := newKeySet(keys, require)
	if err != nil {
		return err
	}
	return verifySums(FleetSignatureContext, repo, tag, sums, sig, ks)
}

// SignExtraLine is SignReleaseLine for an on-demand extra (FleetExtraSignatureContext), such as the
// voice engine's release: "<id>:ed25519:<base64>" over repo, tag and these SHA256SUMS bytes.
func SignExtraLine(id string, key ed25519.PrivateKey, repo, tag string, sums []byte) string {
	sig := ed25519.Sign(key, signatureMessage(FleetExtraSignatureContext, repo, tag, sums))
	return id + ":" + config.KeyEd25519 + ":" + base64.StdEncoding.EncodeToString(sig)
}

// VerifyExtra checks an extra's SHA256SUMS.sig exactly as Updater.VerifySums does on a daemon.
func VerifyExtra(keys []config.FleetKey, require []string, repo, tag string, sums, sig []byte) error {
	ks, err := newKeySet(keys, require)
	if err != nil {
		return err
	}
	return verifySums(FleetExtraSignatureContext, repo, tag, sums, sig, ks)
}

// KeysOK reports whether the pinned key set and require policy are valid, i.e. updates are not
// off because of them (GET /api/health "updates_ok"; no reason there: health is public). No
// pinned key at all is valid: updates are then off for another reason.
func (u *Updater) KeysOK() bool {
	_, err := u.trustedKeys()
	return err == nil
}
