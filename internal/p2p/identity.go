package p2p

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// LoadOrCreateKey reads this install's Ed25519 key from path (libp2p's protobuf encoding),
// creating it (a secret file, private to the user) on first use. The key is the node's
// identity: its peer ID never changes while the file stays.
func LoadOrCreateKey(path string) (crypto.PrivKey, error) {
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		k, err := crypto.UnmarshalPrivateKey(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if k.Type() != crypto.Ed25519 {
			return nil, fmt.Errorf("%s: not an Ed25519 key", path)
		}
		return k, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	k, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, err
	}
	b, err = crypto.MarshalPrivateKey(k)
	if err != nil {
		return nil, err
	}
	// Exclusive: two processes racing on first start must not end up with different keys.
	f, err := config.CreateSecretFile(path)
	if errors.Is(err, os.ErrExist) {
		return LoadOrCreateKey(path)
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return k, f.Close()
}

// PeerIDOf returns the peer ID of a private key as a string.
func PeerIDOf(k crypto.PrivKey) (string, error) {
	id, err := peer.IDFromPrivateKey(k)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
