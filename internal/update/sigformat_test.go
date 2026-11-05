package update

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

const (
	tRepo   = "o/r"
	tTag    = "fleet-v1.2.3"
	tRPID   = "tmx.example"
	tOrigin = "https://tmx.example"
)

var tSums = []byte(strings.Repeat("0", 64) + "  fixture\n")

func edLine(priv ed25519.PrivateKey, id, purpose, repo, tag string, sums []byte, typed bool) string {
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signatureMessage(purpose, repo, tag, sums)))
	if typed {
		return id + ":ed25519:" + sig
	}
	return id + ":" + sig
}

// softAuthenticator is a software WebAuthn authenticator (a P-256 credential).
type softAuthenticator struct {
	priv *ecdsa.PrivateKey
}

func newSoftAuthenticator(t *testing.T) softAuthenticator {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return softAuthenticator{k}
}

// cose returns the credential public key as a base64 COSE_Key.
func (a softAuthenticator) cose(t *testing.T) string {
	b, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: int64(webauthncose.EllipticKey), Algorithm: int64(webauthncose.AlgES256)},
		Curve:         int64(webauthncose.P256),
		XCoord:        a.priv.X.FillBytes(make([]byte, 32)),
		YCoord:        a.priv.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// assertion lets a test change every part of a WebAuthn assertion before it is signed.
type assertion struct {
	typ, origin, rpID string
	challenge         []byte
	flags             byte
	crossOrigin       bool
	tamperSig         bool
}

func (a softAuthenticator) sign(t *testing.T, as assertion) string {
	rp := sha256.Sum256([]byte(as.rpID))
	authData := append(rp[:], as.flags)
	authData = binary.BigEndian.AppendUint32(authData, 7)
	cd, _ := json.Marshal(map[string]any{"type": as.typ, "challenge": base64.RawURLEncoding.EncodeToString(as.challenge),
		"origin": as.origin, "crossOrigin": as.crossOrigin})
	h := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte(nil), authData...), h[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if as.tamperSig {
		authData[33] ^= 1 // the counter changes after signing
	}
	p, _ := json.Marshal(webauthnSig{base64.RawURLEncoding.EncodeToString(authData), base64.RawURLEncoding.EncodeToString(cd), base64.RawURLEncoding.EncodeToString(sig)})
	return base64.StdEncoding.EncodeToString(p)
}

func goodAssertion(purpose string) assertion {
	ch, _ := webauthnChallenge(purpose, tRepo, tTag, tSums)
	return assertion{typ: "webauthn.get", origin: tOrigin, rpID: tRPID, challenge: ch, flags: waFlagUP | waFlagUV}
}

func mustKeySet(t *testing.T, keys []config.FleetKey, require ...string) keySet {
	t.Helper()
	c := config.UpdateConfig{FleetKeys: keys, Require: require}
	k, r, err := c.PinnedKeys()
	if err != nil {
		t.Fatal(err)
	}
	ks, err := newKeySet(k, r)
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

func TestEd25519SignatureFormats(t *testing.T) {
	priv, pub := newKey(t)
	other, _ := newKey(t)
	ks := mustKeySet(t, []config.FleetKey{{ID: FleetKeyID, Type: "ed25519", Key: pub}})
	good := func(typed bool) string {
		return edLine(priv, FleetKeyID, FleetSignatureContext, tRepo, tTag, tSums, typed)
	}
	for _, tc := range []struct {
		name, sig string
		want      error // nil = valid
	}{
		{"legacy format", good(false), nil},
		{"typed format", good(true), nil},
		{"CRLF and blank lines", "\r\n" + good(true) + "\r\n\r\n", nil},
		{"unknown id next to a valid line", "fleet-release-v9:ed25519:" + strings.SplitN(good(true), ":", 3)[2] + "\n" + good(true), nil},
		{"wrong key", edLine(other, FleetKeyID, FleetSignatureContext, tRepo, tTag, tSums, true), errUnsigned},
		{"wrong tag", edLine(priv, FleetKeyID, FleetSignatureContext, tRepo, "fleet-v9.9.9", tSums, true), errUnsigned},
		{"wrong repo", edLine(priv, FleetKeyID, FleetSignatureContext, "x/y", tTag, tSums, true), errUnsigned},
		{"wrong context", edLine(priv, FleetKeyID, FleetExtraSignatureContext, tRepo, tTag, tSums, true), errUnsigned},
		{"wrong sums", edLine(priv, FleetKeyID, FleetSignatureContext, tRepo, tTag, append(tSums, 'x'), true), errUnsigned},
		{"unknown id", edLine(priv, "other-key", FleetSignatureContext, tRepo, tTag, tSums, true), errUnsigned},
		{"unknown type", FleetKeyID + ":rsa:" + strings.SplitN(good(true), ":", 3)[2], errUnsigned},
		{"type mismatch", FleetKeyID + ":webauthn:" + strings.SplitN(good(true), ":", 3)[2], errUnsigned},
		{"empty file", "", errBadSig},
		{"bare base64", strings.SplitN(good(true), ":", 3)[2], errBadSig},
		{"too many fields", good(true) + ":x", errBadSig},
		{"bad base64", FleetKeyID + ":ed25519:!!!", errBadSig},
		{"short signature", FleetKeyID + ":ed25519:" + base64.StdEncoding.EncodeToString([]byte("short")), errBadSig},
		{"empty id", ":ed25519:" + strings.SplitN(good(true), ":", 3)[2], errBadSig},
		{"malformed line next to a valid one", good(true) + "\ngarbage", errBadSig},
		{"too many lines", strings.Repeat(good(true)+"\n", maxSigLines+1), errBadSig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySums(FleetSignatureContext, tRepo, tTag, tSums, []byte(tc.sig), ks)
			if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	if verifySums(FleetSignatureContext, tRepo, tTag, tSums, []byte(good(true)), keySet{}) == nil {
		t.Fatal("an empty key set accepted a release")
	}
}

func TestWebAuthnSignature(t *testing.T) {
	auth := newSoftAuthenticator(t)
	other := newSoftAuthenticator(t)
	ks := mustKeySet(t, []config.FleetKey{{ID: "owner-passkey", Type: "webauthn", Key: auth.cose(t), RPID: tRPID, Origin: tOrigin}}, "webauthn")
	line := func(a softAuthenticator, mut func(*assertion)) string {
		as := goodAssertion(FleetSignatureContext)
		if mut != nil {
			mut(&as)
		}
		return "owner-passkey:webauthn:" + a.sign(t, as)
	}
	for _, tc := range []struct {
		name  string
		sig   string
		valid bool
	}{
		{"valid", line(auth, nil), true},
		{"wrong challenge", line(auth, func(a *assertion) { a.challenge = sha256.New().Sum(nil) }), false},
		{"extras challenge", line(auth, func(a *assertion) { a.challenge, _ = webauthnChallenge(FleetExtraSignatureContext, tRepo, tTag, tSums) }), false},
		{"wrong origin", line(auth, func(a *assertion) { a.origin = "https://evil.example" }), false},
		{"cross origin", line(auth, func(a *assertion) { a.crossOrigin = true }), false},
		{"wrong rp id", line(auth, func(a *assertion) { a.rpID = "evil.example" }), false},
		{"missing UP", line(auth, func(a *assertion) { a.flags = waFlagUV }), false},
		{"missing UV", line(auth, func(a *assertion) { a.flags = waFlagUP }), false},
		{"wrong type", line(auth, func(a *assertion) { a.typ = "webauthn.create" }), false},
		{"bad signature", line(auth, func(a *assertion) { a.tamperSig = true }), false},
		{"other credential", line(other, nil), false},
		{"payload not JSON", "owner-passkey:webauthn:" + base64.StdEncoding.EncodeToString([]byte("nope")), false},
		{"unknown id", strings.Replace(line(auth, nil), "owner-passkey", "someone", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySums(FleetSignatureContext, tRepo, tTag, tSums, []byte(tc.sig), ks)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
	// The release challenge does not verify for extras (and the reverse).
	if verifySums(FleetExtraSignatureContext, tRepo, tTag, tSums, []byte(line(auth, nil)), ks) == nil {
		t.Fatal("a release assertion accepted for extras")
	}
}

func TestRequirePolicy(t *testing.T) {
	priv, pub := newKey(t)
	auth := newSoftAuthenticator(t)
	keys := []config.FleetKey{
		{ID: FleetKeyID, Type: "ed25519", Key: pub},
		{ID: "owner-passkey", Type: "webauthn", Key: auth.cose(t), RPID: tRPID, Origin: tOrigin},
	}
	ed := edLine(priv, FleetKeyID, FleetSignatureContext, tRepo, tTag, tSums, true)
	wa := "owner-passkey:webauthn:" + auth.sign(t, goodAssertion(FleetSignatureContext))
	forged := "owner-passkey:webauthn:" + newSoftAuthenticator(t).sign(t, goodAssertion(FleetSignatureContext))
	for _, tc := range []struct {
		name    string
		require []string
		sig     string
		valid   bool
	}{
		{"default ed25519 only", nil, ed, true},
		{"default ignores a lone webauthn", nil, wa, false},
		{"webauthn only", []string{"webauthn"}, wa, true},
		{"webauthn required, ed25519 given", []string{"webauthn"}, ed, false},
		{"2 of 2", []string{"ed25519", "webauthn"}, ed + "\n" + wa, true},
		{"2 of 2, webauthn missing", []string{"ed25519", "webauthn"}, ed, false},
		{"2 of 2, ed25519 missing", []string{"ed25519", "webauthn"}, wa, false},
		{"2 of 2, webauthn by another credential", []string{"ed25519", "webauthn"}, ed + "\n" + forged, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySums(FleetSignatureContext, tRepo, tTag, tSums, []byte(tc.sig), mustKeySet(t, keys, tc.require...))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestUpdaterReadsPinnedSet(t *testing.T) {
	priv, pub := newKey(t)
	for _, tc := range []struct {
		name string
		cfg  config.UpdateConfig
	}{
		{"legacy fleet_public_key", config.UpdateConfig{FleetPublicKey: pub}},
		{"fleet_keys", config.UpdateConfig{FleetKeys: []config.FleetKey{{ID: FleetKeyID, Type: "ed25519", Key: pub}}}},
		{"both, same key", config.UpdateConfig{FleetPublicKey: pub, FleetKeys: []config.FleetKey{{ID: "k2", Type: "ed25519", Key: pub}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := New(Options{Paths: config.Paths{Home: t.TempDir()}, Settings: func() config.UpdateConfig { return tc.cfg }})
			ks, err := u.trustedKeys()
			if err != nil || ks.empty() {
				t.Fatalf("keys: %v", err)
			}
			id := tc.cfg.FleetKeys
			kid := FleetKeyID
			if len(id) > 0 {
				kid = id[0].ID
			}
			sig := edLine(priv, kid, FleetExtraSignatureContext, u.Repo(), "whisper-engine-v1", tSums, false)
			if _, err := u.VerifySums("whisper-engine-v1", tSums, []byte(sig)); err != nil {
				t.Fatal(err)
			}
		})
	}
	u := New(Options{Paths: config.Paths{Home: t.TempDir()}, Settings: func() config.UpdateConfig { return config.UpdateConfig{} }})
	if _, err := u.VerifySums("whisper-engine-v1", tSums, []byte(edLine(priv, FleetKeyID, FleetExtraSignatureContext, u.Repo(), "whisper-engine-v1", tSums, false))); err == nil {
		t.Fatal("no pinned keys accepted a signature")
	}
}

func TestStageRefusesDowngrade(t *testing.T) {
	_, pub := newKey(t)
	for _, tc := range []struct{ current, release string }{
		{"fleet-v1.2.3", "fleet-v1.2.3"},
		{"fleet-v1.2.3", "fleet-v1.2.2"},
		{"fleet-v1.2.3-4-gabcdef0", "fleet-v1.2.3"},
		{"dev", "fleet-v1.2.3"},
	} {
		u := &Updater{o: Options{Paths: config.Paths{Home: t.TempDir()}, Version: tc.current, Keys: []string{pub}}}
		_, err := u.stage(context.Background(), target{path: "unused", goos: "linux", goarch: "amd64"}, release{TagName: tc.release}, tc.release)
		if err == nil || !strings.Contains(err.Error(), "not newer") {
			t.Fatalf("%s -> %s: %v", tc.current, tc.release, err)
		}
	}
}

// An invalid pinned set turns updates off with a visible reason; it never fails anything else.
func TestInvalidKeysFailClosed(t *testing.T) {
	priv, pub := newKey(t)
	for _, cfg := range []config.UpdateConfig{
		{FleetKeys: []config.FleetKey{{ID: "a", Type: "ed25519", Key: "AAAA"}}},
		{FleetPublicKey: "PLACEHOLDER"},
		{FleetPublicKey: pub, Require: []string{"webauthn"}},
		{FleetPublicKey: pub, Require: []string{"rsa"}},
	} {
		u := New(Options{Paths: config.Paths{Home: t.TempDir()}, Version: "fleet-v1.0.0", Executable: "tmx.exe", GOOS: "windows", GOARCH: "amd64",
			Settings: func() config.UpdateConfig { return cfg }})
		st := u.Check(context.Background())
		if st.State != StateOff || !strings.HasPrefix(st.Reason, "update keys invalid: ") {
			t.Fatalf("%+v: state %s reason %q", cfg, st.State, st.Reason)
		}
		sig := edLine(priv, FleetKeyID, FleetExtraSignatureContext, u.Repo(), "whisper-engine-v1", tSums, false)
		if _, err := u.VerifySums("whisper-engine-v1", tSums, []byte(sig)); err == nil || !strings.HasPrefix(err.Error(), "update keys invalid: ") {
			t.Fatalf("%+v: VerifySums: %v", cfg, err)
		}
		if _, err := u.stage(context.Background(), target{path: "unused", goos: "windows", goarch: "amd64"}, release{TagName: "fleet-v2.0.0"}, "fleet-v2.0.0"); err == nil || !strings.HasPrefix(err.Error(), "update keys invalid: ") {
			t.Fatalf("%+v: stage: %v", cfg, err)
		}
	}
	u := New(Options{Paths: config.Paths{Home: t.TempDir()}, Version: "fleet-v1.0.0", Executable: "tmx.exe", GOOS: "windows", GOARCH: "amd64",
		Settings: func() config.UpdateConfig { return config.UpdateConfig{FleetPublicKey: pub} }})
	if st := u.Status(); st.State == StateOff {
		t.Fatalf("valid set turned updates off: %q", st.Reason)
	}
}
