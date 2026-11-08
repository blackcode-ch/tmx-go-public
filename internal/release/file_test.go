package release

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

func TestSignFileOffline(t *testing.T) {
	home := t.TempDir()
	pub, err := Keygen(home, config.LegacyFleetKeyID)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := KeyFile(home, pub.ID)
	sumsPath, outPath := filepath.Join(home, "SHA256SUMS"), filepath.Join(home, "SHA256SUMS.sig")
	sums := []byte(strings.Repeat("0", 64) + "  binary\n")
	if err := os.WriteFile(sumsPath, sums, 0o644); err != nil {
		t.Fatal(err)
	}
	sign := func(sumsPath, keyPath, outPath, repo, tag, id string) error {
		return signFile(sumsPath, keyPath, outPath, repo, tag, id, "source", "commit", func(source, tag, commit string) ([]byte, error) { return sums, nil })
	}
	if err := sign(sumsPath, keyPath, outPath, config.PublicUpdateRepo, "fleet-v1.0.0", pub.ID); err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	pins := []config.FleetKey{{ID: pub.ID, Type: config.KeyEd25519, Key: pub.Key}}
	if err := update.VerifyRelease(pins, []string{config.KeyEd25519}, config.PublicUpdateRepo, "fleet-v1.0.0", sums, sig); err != nil {
		t.Fatal(err)
	}
	if err := update.VerifyRelease(pins, []string{config.KeyEd25519}, config.DefaultUpdateRepo, "fleet-v1.0.0", sums, sig); err == nil {
		t.Fatal("signature not bound to repository")
	}
	if err := sign(sumsPath, keyPath, outPath, config.PublicUpdateRepo, "fleet-v1.0.0", pub.ID); err == nil {
		t.Fatal("overwrote signature")
	}
	if err := sign(sumsPath, filepath.Join(home, "missing.key"), outPath+"2", config.PublicUpdateRepo, "fleet-v1.0.0", pub.ID); err == nil {
		t.Fatal("missing key accepted")
	}
	// Corrupt key material is never reproduced in an error.
	secret := base64.StdEncoding.EncodeToString([]byte("deliberately invalid test seed"))
	if err := os.WriteFile(keyPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sign(sumsPath, keyPath, outPath+"2", config.PublicUpdateRepo, "fleet-v1.0.0", pub.ID); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("bad key accepted or echoed")
	}
}
