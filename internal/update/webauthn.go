package update

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

// A webauthn release signature (sign.go): a WebAuthn assertion by a pinned credential. It is
// checked only when a webauthn key is pinned (update.fleet_keys) and counts only for "webauthn"
// in update.require.

// webauthnSig is the JSON payload of a "<id>:webauthn:<base64 JSON>" line (fields base64url).
type webauthnSig struct {
	AuthenticatorData string `json:"authenticator_data"`
	ClientDataJSON    string `json:"client_data_json"`
	Signature         string `json:"signature"`
}

// webauthnClientData is the part of clientDataJSON that is checked.
type webauthnClientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
}

// Authenticator data flags (WebAuthn §6.1).
const (
	waFlagUP = 0x01 // user present
	waFlagUV = 0x04 // user verified
)

func b64url(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// verifyWebAuthn checks one assertion: clientData.type "webauthn.get", its challenge equal to
// webauthnChallenge, its origin the pinned one (not cross-origin); authenticatorData's rpIdHash
// SHA-256(rp_id) with UP and UV set; and the signature over authenticatorData ||
// SHA-256(clientDataJSON) by the pinned credential key (ES256). The signature counter is not
// checked: a release signature is a stored artifact, verified many times by many machines.
func verifyWebAuthn(k pinnedKey, purpose, repo, tag string, sums, payload []byte) error {
	var p webauthnSig
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return errors.New("webauthn: payload is not the expected JSON")
	}
	authData, err1 := b64url(p.AuthenticatorData)
	clientJSON, err2 := b64url(p.ClientDataJSON)
	sig, err3 := b64url(p.Signature)
	if err1 != nil || err2 != nil || err3 != nil || len(authData) < 37 || len(clientJSON) == 0 || len(sig) == 0 {
		return errors.New("webauthn: payload fields are not base64url")
	}
	var cd webauthnClientData
	if err := json.Unmarshal(clientJSON, &cd); err != nil {
		return errors.New("webauthn: clientDataJSON does not parse")
	}
	if cd.Type != "webauthn.get" {
		return errors.New("webauthn: not an assertion")
	}
	want, ok := webauthnChallenge(purpose, repo, tag, sums)
	got, err := b64url(cd.Challenge)
	if !ok || err != nil || subtle.ConstantTimeCompare(got, want) != 1 {
		return errors.New("webauthn: challenge is not this release")
	}
	if cd.Origin != k.origin || cd.CrossOrigin {
		return errors.New("webauthn: wrong origin")
	}
	if subtle.ConstantTimeCompare(authData[:32], k.rpIDHash[:]) != 1 {
		return errors.New("webauthn: wrong rp_id")
	}
	if authData[32]&waFlagUP == 0 || authData[32]&waFlagUV == 0 {
		return errors.New("webauthn: user presence and verification are required")
	}
	ch := sha256.Sum256(clientJSON)
	signed := append(append([]byte(nil), authData...), ch[:]...)
	if ok, err := webauthncose.VerifySignature(k.wa, signed, sig); err != nil || !ok {
		return errors.New("webauthn: bad signature")
	}
	return nil
}
