package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// The key file holds the base64 32-byte Ed25519 seed and a newline. It is written once, through
// config.WriteSecretFile (owner-only, atomic), and never replaced: a new key is a new id.

// Public is what keygen prints: never the private key.
type Public struct {
	ID          string
	Key         string // base64 raw Ed25519 public key
	Fingerprint string // hex sha256(raw key)[:12]
}

// Fingerprint is the short form of a raw public key the tmx-go page publishes.
func Fingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:])[:12]
}

// Snippet is the config.json "update" fragment that pins the key.
func (p Public) Snippet() string {
	b, _ := json.MarshalIndent(map[string]any{"update": map[string]any{"fleet_keys": []config.FleetKey{
		{ID: p.ID, Type: config.KeyEd25519, Key: p.Key}}}}, "", "  ")
	return string(b)
}

func validID(id string) error {
	// The pin's own rule (config.FleetKey.Validate), checked with a placeholder key.
	return config.FleetKey{ID: id, Type: config.KeyEd25519, Key: base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))}.Validate()
}

// Keygen creates key id in home. It refuses when the key file exists: a key is never overwritten.
func Keygen(home, id string) (Public, error) {
	if err := validID(id); err != nil {
		return Public{}, err
	}
	path := KeyFile(home, id)
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return Public{}, fmt.Errorf("refusing: %s exists (a release key is never replaced; use a new --id)", path)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Public{}, err
	}
	data := []byte(base64.StdEncoding.EncodeToString(priv.Seed()) + "\n")
	// Write a private staging file, then hard-link it to the final name: a link never replaces an
	// existing file, so two keygens racing cannot overwrite each other's key.
	stage := filepath.Join(filepath.Dir(path), "."+id+".key.new")
	os.Remove(stage)
	if err := config.WriteSecretFile(stage, data); err != nil {
		return Public{}, err
	}
	defer os.Remove(stage)
	if err := os.Link(stage, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Public{}, fmt.Errorf("refusing: %s exists (a release key is never replaced; use a new --id)", path)
		}
		return Public{}, fmt.Errorf("key not written: %w", err)
	}
	return Public{ID: id, Key: base64.StdEncoding.EncodeToString(pub), Fingerprint: Fingerprint(pub)}, nil
}

// LoadKey reads key id from home.
func LoadKey(home, id string) (ed25519.PrivateKey, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(KeyFile(home, id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no release key %q on this machine (tmx release keygen --id %s)", id, id)
		}
		return nil, errors.New("release key unreadable")
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("release key file is corrupt") // never echo its content
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// PublicOf is the public half of key id in home (`tmx release pubkey`): it lets the owner check a
// key file, e.g. one restored from its backup, against the pinned key without printing a secret.
func PublicOf(home, id string) (Public, error) {
	key, err := LoadKey(home, id)
	if err != nil {
		return Public{}, err
	}
	pub := key.Public().(ed25519.PublicKey)
	return Public{ID: id, Key: base64.StdEncoding.EncodeToString(pub), Fingerprint: Fingerprint(pub)}, nil
}
