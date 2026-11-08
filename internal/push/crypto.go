// Package push implements the paired phone's Web Push transport. The wire format
// follows Python tmx.webpush_crypto: RFC 8291 aes128gcm and RFC 8292 VAPID.
package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/url"
	"time"

	"golang.org/x/crypto/hkdf"
)

var b64 = base64.RawURLEncoding

func encrypt(plain, ua, auth []byte) ([]byte, error) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encryptRecord(plain, ua, auth, key, salt)
}

// Separate deterministic primitive permits the published RFC 8291 test vector.
func encryptRecord(plain, ua, auth []byte, key *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(auth) != 16 || len(salt) != 16 || len(plain) > 3993 {
		return nil, errors.New("invalid push record")
	}
	peer, err := ecdh.P256().NewPublicKey(ua)
	if err != nil {
		return nil, errors.New("invalid subscription key")
	}
	shared, err := key.ECDH(peer)
	if err != nil {
		return nil, err
	}
	pub := key.PublicKey().Bytes()
	info := append(append([]byte("WebPush: info\x00"), ua...), pub...)
	derive := func(secret, salt, info []byte, n int) []byte {
		out := make([]byte, n)
		_, _ = io.ReadFull(hkdf.New(sha256.New, secret, salt, info), out)
		return out
	}
	ikm := derive(shared, auth, info, 32)
	cek := derive(ikm, salt, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := derive(ikm, salt, []byte("Content-Encoding: nonce\x00"), 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 21, 86)
	copy(header, salt)
	binary.BigEndian.PutUint32(header[16:20], 4096)
	header[20] = byte(len(pub))
	header = append(header, pub...)
	text := append(append([]byte(nil), plain...), 2)
	return gcm.Seal(header, nonce, text, nil), nil
}

func vapid(key *ecdh.PrivateKey, endpoint string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", errors.New("invalid push endpoint")
	}
	head := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": "https://" + u.Host, "exp": now.Add(12 * time.Hour).Unix(), "sub": "https://github.com/blackcode-ch/tmx-go-fleet"})
	input := head + "." + b64.EncodeToString(claims)
	x, y := elliptic.Unmarshal(elliptic.P256(), key.PublicKey().Bytes())
	signing := &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, D: new(big.Int).SetBytes(key.Bytes())}
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, signing, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + input + "." + b64.EncodeToString(sig) + ", k=" + b64.EncodeToString(key.PublicKey().Bytes()), nil
}
