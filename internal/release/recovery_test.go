package release

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// PublicOf gives back exactly the key keygen printed and never the seed; a missing or corrupt key
// file is an error that does not echo the file.
func TestPublicOfMatchesKeygen(t *testing.T) {
	home := t.TempDir()
	want, err := Keygen(home, "fleet-release-v1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := PublicOf(home, "fleet-release-v1")
	if err != nil || got != want {
		t.Fatalf("PublicOf = %+v, %v; want %+v", got, err, want)
	}
	seed, _ := os.ReadFile(KeyFile(home, "fleet-release-v1"))
	if s := strings.TrimSpace(string(seed)); strings.Contains(got.Key, s) || strings.Contains(got.Fingerprint, s) {
		t.Fatal("PublicOf carries the seed")
	}
	if _, err := PublicOf(home, "fleet-release-v2"); err == nil {
		t.Fatal("a missing key gave a public key")
	}
	const junk = "not-a-seed-but-secret-looking\n"
	// A plain write (the secrets folder exists from Keygen): no DACL change outside aclmutate tests.
	if err := os.WriteFile(KeyFile(home, "k-bad"), []byte(junk), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PublicOf(home, "k-bad"); err == nil || strings.Contains(err.Error(), "not-a-seed") {
		t.Fatalf("corrupt key: %v", err)
	}
}

// docs/RECOVERY.md is where an owner copies the fleet key from: its public key must be a valid
// ed25519 pin and its fingerprint must be that key's, so a typo there is caught here.
func TestRecoveryDocKeyMatchesItsFingerprint(t *testing.T) {
	// Public CI marks snapshots explicitly; keep the README fallback for local public checkouts.
	// Missing or malformed recovery docs in a private checkout must still fail.
	b, err := os.ReadFile("../../docs/RECOVERY.md")
	if os.IsNotExist(err) {
		if os.Getenv("TMX_PUBLIC_SNAPSHOT") == "1" {
			t.Skip("public CI snapshot omits the private recovery document")
		}
		readme, _ := os.ReadFile("../../README.md")
		if strings.Contains(string(readme), "*This is a scrubbed CI snapshot of a private repository, made automatically.") {
			t.Skip("public CI snapshot omits the private recovery document")
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	key := regexp.MustCompile("(?m)^\\| Public key \\| `([A-Za-z0-9+/=]+)` \\|").FindStringSubmatch(doc)
	fp := regexp.MustCompile("(?m)^\\| Fingerprint \\| `([0-9a-f]{12})`").FindStringSubmatch(doc)
	if key == nil || fp == nil {
		t.Fatal("docs/RECOVERY.md: no Public key / Fingerprint row")
	}
	pin := config.FleetKey{ID: config.LegacyFleetKeyID, Type: config.KeyEd25519, Key: key[1]}
	if err := pin.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(key[1])
	if got := Fingerprint(ed25519.PublicKey(raw)); got != fp[1] {
		t.Fatalf("fingerprint of the documented key is %s, the page says %s", got, fp[1])
	}
	// Every pin command and snippet on the page names the same key.
	for _, m := range regexp.MustCompile(`fleet-release-v1:([A-Za-z0-9+/=]{44})|"key": "([A-Za-z0-9+/=]{44})"`).FindAllStringSubmatch(doc, -1) {
		if k := m[1] + m[2]; k != key[1] {
			t.Errorf("the page also pins %s", k)
		}
	}
}
