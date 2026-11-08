package push

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func decode64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := b64.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Published RFC 8291 section 5 / Appendix A, also used by Python's port.
func TestRFC8291(t *testing.T) {
	key, err := ecdh.P256().NewPrivateKey(decode64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	plain := decode64(t, "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24")
	ua := decode64(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	auth := decode64(t, "BTBZMqHH6r4Tts7J_aSIgg")
	salt := decode64(t, "DGv6ra1nlYgDCS1FRnbzlw")
	want := decode64(t, "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN")
	got, err := encryptRecord(plain, ua, auth, key, salt)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("RFC 8291 vector mismatch: %v", err)
	}
	a, err := encrypt(plain, ua, auth)
	if err != nil {
		t.Fatal(err)
	}
	b, err := encrypt(plain, ua, auth)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a[:16], b[:16]) || bytes.Equal(a[21:86], b[21:86]) {
		t.Fatal("reused salt or ephemeral key")
	}
	if _, err := encrypt(plain, make([]byte, 65), auth); err == nil {
		t.Fatal("accepted invalid public key")
	}
}

func TestVAPIDSignature(t *testing.T) {
	key, _ := ecdh.P256().GenerateKey(rand.Reader)
	header, err := vapid(key, "https://fcm.googleapis.com/send/test", time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	jwt, pub, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	if !ok {
		t.Fatal("bad authorization")
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatal("bad JWT")
	}
	var claims struct {
		Aud string
		Exp int64
		Sub string
	}
	if json.Unmarshal(decode64(t, parts[1]), &claims) != nil || claims.Aud != "https://fcm.googleapis.com" || claims.Exp != 44200 || claims.Sub == "" {
		t.Fatal("wrong claims")
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), decode64(t, pub))
	sig := decode64(t, parts[2])
	if len(sig) != 64 {
		t.Fatal("bad ES256 signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("VAPID signature failed")
	}
}
