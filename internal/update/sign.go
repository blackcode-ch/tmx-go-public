package update

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

// Release integrity: CI writes SHA256SUMS (one "hash  name" line per file of the release) and
// SHA256SUMS.sig. An install accepts a download only when SHA256SUMS carries the signatures the
// require policy asks for, by keys pinned in update.fleet_keys (config.UpdateConfig.PinnedKeys),
// and the download's SHA-256 is the one SHA256SUMS lists.
//
// SHA256SUMS.sig holds one signature per line (at most maxSigLines):
//
//	<key id>:<type>:<base64 payload>   type "ed25519" or "webauthn"
//	<key id>:<base64 signature>        the original form, an ed25519 signature
//
// ed25519: the payload is the 64-byte Ed25519 signature of signatureMessage(purpose, repo, tag,
// SHA256SUMS): the purpose (FleetSignatureContext or FleetExtraSignatureContext), repository and
// tag, each LF-terminated, then the exact SHA256SUMS bytes (an OpenSSL "pkeyutl -sign -rawin").
//
// webauthn: the payload is JSON {authenticator_data, client_data_json, signature} (base64url), a
// WebAuthn assertion by the pinned credential whose challenge is webauthnChallenge (see there).
//
// A line that does not parse makes the whole file invalid. A line naming a key or type that is
// not pinned, or whose signature does not verify, counts for nothing; the release passes only
// when every required type has at least one valid signature by a pinned key of that type.

var errUnsigned = errors.New("the release is not signed with a key this tmx knows")

var errBadSig = errors.New("SHA256SUMS.sig is not a signature")

const maxSigLines = 8

// pinnedKey is a decoded config.FleetKey.
type pinnedKey struct {
	id, typ  string
	ed       ed25519.PublicKey
	wa       webauthncose.EC2PublicKeyData
	rpIDHash [32]byte
	origin   string
}

// keySet is the decoded pinned set plus the require policy.
type keySet struct {
	keys    []pinnedKey
	require []string
}

func (ks keySet) empty() bool { return len(ks.keys) == 0 }

const upstreamKeyHash = "507f6209f615997ed3deaa7391822230a1322893782946699173847af1ce7014"

// newKeySet decodes validated config keys.
func newKeySet(keys []config.FleetKey, require []string) (keySet, error) {
	ks := keySet{require: require}
	for _, k := range keys {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(k.Key))
		if err != nil {
			return keySet{}, errors.New("invalid fleet public key")
		}
		// Explicitly reject the known upstream signing identity, even if misconfigured.
		if fmt.Sprintf("%x", sha256.Sum256(raw)) == upstreamKeyHash {
			return keySet{}, errors.New("upstream release key is not a fleet key")
		}
		p := pinnedKey{id: k.ID, typ: k.Type}
		switch k.Type {
		case config.KeyEd25519:
			if len(raw) != ed25519.PublicKeySize {
				return keySet{}, errors.New("invalid fleet public key")
			}
			p.ed = ed25519.PublicKey(raw)
		case config.KeyWebAuthn:
			if p.wa, err = config.ParseWebAuthnKey(raw); err != nil {
				return keySet{}, err
			}
			p.rpIDHash, p.origin = sha256.Sum256([]byte(k.RPID)), k.Origin
		default:
			return keySet{}, fmt.Errorf("unknown fleet key type %q", k.Type)
		}
		ks.keys = append(ks.keys, p)
	}
	if len(ks.require) == 0 {
		ks.require = []string{config.KeyEd25519}
	}
	return ks, nil
}

// publicKeys builds a set of base64 Ed25519 keys under FleetKeyID (the Options.Keys seam).
func publicKeys(b64 []string) (keySet, error) {
	var ks []config.FleetKey
	for _, k := range b64 {
		ks = append(ks, config.FleetKey{ID: FleetKeyID, Type: config.KeyEd25519, Key: k})
	}
	return newKeySet(ks, []string{config.KeyEd25519})
}

func signatureMessage(purpose, repo, tag string, sums []byte) []byte {
	return append([]byte(purpose+"\n"+repo+"\n"+tag+"\n"), sums...)
}

// webauthnChallenge is the challenge a webauthn release signature must carry:
//
//	SHA-256(context + "\n" + repo + "\n" + tag + "\n" + SHA256SUMS bytes)
//
// with context FleetWebAuthnContext ("tmx-fleet-release-v2") for a release and
// FleetExtraWebAuthnContext ("tmx-fleet-extra-v2") for extras; clientDataJSON.challenge is its
// base64url encoding (no padding), as browsers write it.
func webauthnChallenge(purpose, repo, tag string, sums []byte) ([]byte, bool) {
	var ctx string
	switch purpose {
	case FleetSignatureContext:
		ctx = FleetWebAuthnContext
	case FleetExtraSignatureContext:
		ctx = FleetExtraWebAuthnContext
	case FleetKeysContext:
		ctx = FleetKeysWebAuthnContext
	default:
		return nil, false
	}
	h := sha256.Sum256(signatureMessage(ctx, repo, tag, sums))
	return h[:], true
}

// sigLine is one parsed SHA256SUMS.sig line.
type sigLine struct {
	id, typ string
	payload []byte
}

func parseSigFile(b []byte) ([]sigLine, error) {
	var out []sigLine
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		var l sigLine
		switch len(parts) {
		case 2:
			l = sigLine{id: parts[0], typ: config.KeyEd25519}
		case 3:
			l = sigLine{id: parts[0], typ: parts[1]}
		default:
			return nil, errBadSig
		}
		raw, err := base64.StdEncoding.DecodeString(parts[len(parts)-1])
		if err != nil || l.id == "" || l.typ == "" || len(raw) == 0 {
			return nil, errBadSig
		}
		if l.typ == config.KeyEd25519 && len(raw) != ed25519.SignatureSize {
			return nil, errBadSig
		}
		l.payload = raw
		out = append(out, l)
	}
	if len(out) == 0 || len(out) > maxSigLines {
		return nil, errBadSig
	}
	return out, nil
}

// verifySums checks sig over the fleet context and sums against the pinned set and its policy.
func verifySums(purpose, repo, tag string, sums, sig []byte, ks keySet) error {
	if repo == "" || tag == "" || strings.ContainsAny(repo+tag, "\r\n") || ks.empty() {
		return errUnsigned
	}
	lines, err := parseSigFile(sig)
	if err != nil {
		return err
	}
	valid := map[string]bool{}
	for _, l := range lines {
		for _, k := range ks.keys {
			if k.id != l.id || k.typ != l.typ || valid[k.typ] {
				continue
			}
			switch k.typ {
			case config.KeyEd25519:
				valid[k.typ] = ed25519.Verify(k.ed, signatureMessage(purpose, repo, tag, sums), l.payload)
			case config.KeyWebAuthn:
				valid[k.typ] = verifyWebAuthn(k, purpose, repo, tag, sums, l.payload) == nil
			}
		}
	}
	for _, t := range ks.require {
		if !valid[t] {
			return errUnsigned
		}
	}
	return nil
}

// parseSums reads SHA256SUMS (sha256sum's format: "<hex>  <name>" or "<hex> *<name>").
func parseSums(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 || len(f[0]) != 64 {
			continue
		}
		out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
	}
	return out
}
